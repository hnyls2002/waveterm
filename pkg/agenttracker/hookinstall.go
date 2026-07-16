// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttracker

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wavetermdev/waveterm/pkg/panichandler"
	"github.com/wavetermdev/waveterm/pkg/userinput"
	"github.com/wavetermdev/waveterm/pkg/util/utilfn"
	"github.com/wavetermdev/waveterm/pkg/waveobj"
	"github.com/wavetermdev/waveterm/pkg/wconfig"
)

const (
	ClaudeDirName           = ".claude"
	ClaudeSettingsPath      = ".claude/settings.json"
	ClaudeSettingsLocalPath = ".claude/settings.local.json"
	HookScriptPath          = ".claude/hooks/wave-agents-hook.sh"
	HookScriptName          = "wave-agents-hook.sh"
	// wait for frontend windows to connect before prompting; a userinput
	// request fired with no window listening just times out silently
	InstallPromptDelay   = 15 * time.Second
	InstallPromptTimeout = 5 * time.Minute
)

//go:embed wave-agents-hook.sh
var hookScriptContent []byte

var trackedHookEvents = []string{
	HookEvent_SessionStart,
	HookEvent_UserPromptSubmit,
	HookEvent_Stop,
	HookEvent_Notification,
	HookEvent_PermissionRequest,
	HookEvent_PostToolUse,
	HookEvent_SessionEnd,
	HookEvent_StopFailure,
}

type claudeHookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type claudeHookGroup struct {
	Hooks []claudeHookCommand `json:"hooks"`
}

// jsonObjMember preserves JSON object member order; unmarshaling the user's
// settings.json into a map and re-marshaling would shuffle keys and churn a
// file many users keep under version control.
type jsonObjMember struct {
	Key string
	Val json.RawMessage
}

func registeredHookEventsFromData(data []byte) map[string]bool {
	rtn := make(map[string]bool)
	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return rtn
	}
	for event, groups := range parsed.Hooks {
		for _, group := range groups {
			for _, hook := range group.Hooks {
				if strings.Contains(hook.Command, HookScriptName) {
					rtn[event] = true
				}
			}
		}
	}
	return rtn
}

func registeredHookEvents(settingsPath string) map[string]bool {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return make(map[string]bool)
	}
	return registeredHookEventsFromData(data)
}

func missingHookEvents(homeDir string) []string {
	registered := registeredHookEvents(filepath.Join(homeDir, filepath.FromSlash(ClaudeSettingsPath)))
	for event := range registeredHookEvents(filepath.Join(homeDir, filepath.FromSlash(ClaudeSettingsLocalPath))) {
		registered[event] = true
	}
	var missing []string
	for _, event := range trackedHookEvents {
		if !registered[event] {
			missing = append(missing, event)
		}
	}
	return missing
}

func hookInstallNeeded(homeDir string) bool {
	if _, err := os.Stat(filepath.Join(homeDir, filepath.FromSlash(HookScriptPath))); err != nil {
		return true
	}
	return len(missingHookEvents(homeDir)) > 0
}

func parseObjMembers(data []byte) ([]jsonObjMember, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	var members []jsonObjMember
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected object key token %v", keyTok)
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		members = append(members, jsonObjMember{Key: key, Val: val})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return members, nil
}

func joinRaw(openDelim, closeDelim byte, elems []json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte(openDelim)
	for idx, elem := range elems {
		if idx > 0 {
			buf.WriteByte(',')
		}
		buf.Write(elem)
	}
	buf.WriteByte(closeDelim)
	return buf.Bytes()
}

// encoding goes through utilfn.MarshalIndentNoHTMLString (SetEscapeHTML false):
// plain json.Marshal would rewrite &, <, > inside the user's existing hook
// commands as \u0026 etc.
func encodeObjMembers(members []jsonObjMember) (json.RawMessage, error) {
	elems := make([]json.RawMessage, 0, len(members))
	for _, member := range members {
		key, err := utilfn.MarshalIndentNoHTMLString(member.Key, "", "")
		if err != nil {
			return nil, err
		}
		elems = append(elems, append(json.RawMessage(key+":"), member.Val...))
	}
	return joinRaw('{', '}', elems), nil
}

func findObjMember(members []jsonObjMember, key string) int {
	for idx, member := range members {
		if member.Key == key {
			return idx
		}
	}
	return -1
}

func setObjMember(members []jsonObjMember, key string, val json.RawMessage) []jsonObjMember {
	if idx := findObjMember(members, key); idx >= 0 {
		members[idx].Val = val
		return members
	}
	return append(members, jsonObjMember{Key: key, Val: val})
}

func appendArrayElem(arrData json.RawMessage, elem json.RawMessage) (json.RawMessage, error) {
	var elems []json.RawMessage
	if err := json.Unmarshal(arrData, &elems); err != nil {
		return nil, err
	}
	return joinRaw('[', ']', append(elems, elem)), nil
}

// mergeHookRegistrations appends a wave hook group to each of the given events
// in the settings document, touching nothing else (member order, other hooks,
// and unrelated sections all come through byte-identical modulo re-indent).
func mergeHookRegistrations(settingsData []byte, hookCmd string, events []string) ([]byte, error) {
	group, err := utilfn.MarshalIndentNoHTMLString(claudeHookGroup{
		Hooks: []claudeHookCommand{{Type: "command", Command: hookCmd}},
	}, "", "")
	if err != nil {
		return nil, err
	}
	groupRaw := json.RawMessage(group)
	top, err := parseObjMembers(settingsData)
	if err != nil {
		return nil, fmt.Errorf("parsing claude settings: %w", err)
	}
	hooksVal := json.RawMessage("{}")
	if idx := findObjMember(top, "hooks"); idx >= 0 {
		hooksVal = top[idx].Val
	}
	hooksMembers, err := parseObjMembers(hooksVal)
	if err != nil {
		return nil, fmt.Errorf("parsing hooks section: %w", err)
	}
	for _, event := range events {
		arrData := json.RawMessage("[]")
		if idx := findObjMember(hooksMembers, event); idx >= 0 {
			arrData = hooksMembers[idx].Val
		}
		newVal, err := appendArrayElem(arrData, groupRaw)
		if err != nil {
			return nil, fmt.Errorf("appending to hooks.%s: %w", event, err)
		}
		hooksMembers = setObjMember(hooksMembers, event, newVal)
	}
	newHooksVal, err := encodeObjMembers(hooksMembers)
	if err != nil {
		return nil, err
	}
	merged, err := encodeObjMembers(setObjMember(top, "hooks", newHooksVal))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, merged, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func InstallClaudeHooks() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot resolve home dir: %w", err)
	}
	return installClaudeHooksInHome(homeDir)
}

func installClaudeHooksInHome(homeDir string) error {
	scriptPath := filepath.Join(homeDir, filepath.FromSlash(HookScriptPath))
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		return fmt.Errorf("creating hooks dir: %w", err)
	}
	existing, readErr := os.ReadFile(scriptPath)
	if readErr != nil || !bytes.Equal(existing, hookScriptContent) {
		if err := os.WriteFile(scriptPath, hookScriptContent, 0o755); err != nil {
			return fmt.Errorf("writing hook script: %w", err)
		}
	}
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		return fmt.Errorf("chmod hook script: %w", err)
	}
	missing := missingHookEvents(homeDir)
	if len(missing) == 0 {
		return nil
	}
	settingsPath := filepath.Join(homeDir, filepath.FromSlash(ClaudeSettingsPath))
	settingsData, err := os.ReadFile(settingsPath)
	if os.IsNotExist(err) {
		settingsData = []byte("{}")
	} else if err != nil {
		return fmt.Errorf("reading claude settings: %w", err)
	}
	merged, err := mergeHookRegistrations(settingsData, scriptPath, missing)
	if err != nil {
		return err
	}
	registered := registeredHookEventsFromData(merged)
	for _, event := range trackedHookEvents {
		if !registered[event] {
			return fmt.Errorf("merged settings failed validation (missing %s), aborting write", event)
		}
	}
	// settings.json is commonly a symlink into a dotfiles repo; write through
	// it directly (a tmp-file+rename would replace the symlink with a regular
	// file and orphan the repo copy)
	if err := os.WriteFile(settingsPath, merged, 0o644); err != nil {
		return fmt.Errorf("writing claude settings: %w", err)
	}
	return nil
}

func hookInstallPromptIfNeeded() {
	defer func() {
		panichandler.PanicHandler("agenttracker:hookInstallPromptIfNeeded", recover())
	}()
	time.Sleep(InstallPromptDelay)
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	// no ~/.claude means Claude Code isn't in use; nothing to track
	if _, err := os.Stat(filepath.Join(homeDir, ClaudeDirName)); err != nil {
		return
	}
	if !hookInstallNeeded(homeDir) {
		return
	}
	if wconfig.GetWatcher().GetFullConfig().Settings.AgentsHookInstallDismissed {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), InstallPromptTimeout)
	defer cancel()
	request := &userinput.UserInputRequest{
		ResponseType: "confirm",
		Title:        "Claude Code Status Badges",
		QueryText: "Wave can show live Claude Code session status on tab and block badges.\n\n" +
			"This requires registering a lifecycle hook in `~/.claude/settings.json` " +
			"(existing hooks and settings are preserved).\n\nInstall it now?",
		Markdown:    true,
		OkLabel:     "Install",
		CancelLabel: "Not Now",
		CheckBoxMsg: "Don't ask again",
	}
	resp, err := userinput.GetUserInput(ctx, request)
	if err != nil || resp == nil {
		return
	}
	if resp.CheckboxStat {
		setErr := wconfig.SetBaseConfigValue(waveobj.MetaMapType{wconfig.ConfigKey_AgentsHookInstallDismissed: true})
		if setErr != nil {
			log.Printf("agenttracker: cannot persist hook prompt dismissal: %v\n", setErr)
		}
	}
	if !resp.Confirm {
		return
	}
	installErr := InstallClaudeHooks()
	feedback := &userinput.UserInputRequest{
		ResponseType: "confirm",
		Title:        "Claude Code Status Badges",
		Markdown:     true,
		OkLabel:      "OK",
	}
	if installErr != nil {
		log.Printf("agenttracker: hook install failed: %v\n", installErr)
		feedback.QueryText = fmt.Sprintf("Hook install failed:\n\n```\n%v\n```", installErr)
	} else {
		log.Printf("agenttracker: claude hooks installed\n")
		feedback.QueryText = "Hooks installed. New Claude Code sessions started inside Wave will show " +
			"status badges. Sessions already running pick up hooks after a restart."
	}
	feedbackCtx, cancelFeedback := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancelFeedback()
	_, _ = userinput.GetUserInput(feedbackCtx, feedback)
}
