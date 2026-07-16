// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttracker

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSettingsFixture = `{
  "env": {
    "CLAUDE_CODE_DISABLE_TERMINAL_TITLE": "1"
  },
  "permissions": {
    "allow": [
      "Read",
      "Bash(ls:*)"
    ]
  },
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/bin/sh -c '[ -x \"$HOME/bridge\" ] && \"$HOME/bridge\" --source claude; exit 0'"
          }
        ]
      }
    ],
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {
            "type": "command",
            "command": "/bin/sh -c 'echo pre'"
          }
        ]
      }
    ]
  },
  "theme": "light"
}
`

func writeTestSettings(t *testing.T, homeDir string, content string) string {
	t.Helper()
	claudeDir := filepath.Join(homeDir, ClaudeDirName)
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return settingsPath
}

func topLevelKeys(t *testing.T, data []byte) []string {
	t.Helper()
	members, err := parseObjMembers(data)
	if err != nil {
		t.Fatalf("result is not a valid JSON object: %v", err)
	}
	var keys []string
	for _, member := range members {
		keys = append(keys, member.Key)
	}
	return keys
}

func TestInstallMergesAndPreserves(t *testing.T) {
	homeDir := t.TempDir()
	settingsPath := writeTestSettings(t, homeDir, testSettingsFixture)
	if err := installClaudeHooksInHome(homeDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	registered := registeredHookEventsFromData(data)
	for _, event := range trackedHookEvents {
		if !registered[event] {
			t.Errorf("event %s not registered after install", event)
		}
	}
	keys := topLevelKeys(t, data)
	wantKeys := []string{"env", "permissions", "hooks", "theme"}
	for idx, want := range wantKeys {
		if keys[idx] != want {
			t.Fatalf("top-level key order changed: got %v, want %v", keys, wantKeys)
		}
	}
	if !strings.Contains(string(data), `&& \"$HOME/bridge\"`) {
		t.Error("existing hook command was altered (HTML escaping or content drift)")
	}
	if !strings.Contains(string(data), `"command": "/bin/sh -c 'echo pre'"`) {
		t.Error("unrelated PreToolUse hook was altered")
	}
	if strings.Contains(string(data), `\u0026`) {
		t.Error("output contains HTML-escaped characters")
	}
	if !bytes.HasSuffix(data, []byte("}\n")) {
		t.Error("output missing trailing newline")
	}
	scriptPath := filepath.Join(homeDir, filepath.FromSlash(HookScriptPath))
	scriptInfo, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("hook script not written: %v", err)
	}
	if scriptInfo.Mode().Perm()&0o111 == 0 {
		t.Error("hook script is not executable")
	}
	if hookInstallNeeded(homeDir) {
		t.Error("hookInstallNeeded still true after install")
	}
}

func TestInstallIdempotent(t *testing.T) {
	homeDir := t.TempDir()
	settingsPath := writeTestSettings(t, homeDir, testSettingsFixture)
	if err := installClaudeHooksInHome(homeDir); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := installClaudeHooksInHome(homeDir); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("second install modified settings.json")
	}
}

func TestInstallCreatesSettings(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(homeDir, ClaudeDirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := installClaudeHooksInHome(homeDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(homeDir, filepath.FromSlash(ClaudeSettingsPath)))
	if err != nil {
		t.Fatal(err)
	}
	registered := registeredHookEventsFromData(data)
	for _, event := range trackedHookEvents {
		if !registered[event] {
			t.Errorf("event %s not registered in fresh settings.json", event)
		}
	}
}

func TestInstallAppendsOnlyMissing(t *testing.T) {
	homeDir := t.TempDir()
	scriptPath := filepath.Join(homeDir, filepath.FromSlash(HookScriptPath))
	fixture := `{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "` + scriptPath + `"
          }
        ]
      }
    ]
  }
}
`
	settingsPath := writeTestSettings(t, homeDir, fixture)
	if err := installClaudeHooksInHome(homeDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(data), HookScriptName); count != len(trackedHookEvents) {
		t.Errorf("expected %d hook registrations, got %d", len(trackedHookEvents), count)
	}
}

func TestInstallPreservesSymlink(t *testing.T) {
	homeDir := t.TempDir()
	repoDir := t.TempDir()
	targetPath := filepath.Join(repoDir, "settings.json")
	if err := os.WriteFile(targetPath, []byte(testSettingsFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(homeDir, ClaudeDirName)
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.Symlink(targetPath, settingsPath); err != nil {
		t.Fatal(err)
	}
	if err := installClaudeHooksInHome(homeDir); err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Lstat(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("settings.json symlink was replaced by a regular file")
	}
	targetData, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	registered := registeredHookEventsFromData(targetData)
	for _, event := range trackedHookEvents {
		if !registered[event] {
			t.Errorf("event %s not registered in symlink target", event)
		}
	}
}

func TestInstallRespectsLocalSettings(t *testing.T) {
	homeDir := t.TempDir()
	claudeDir := filepath.Join(homeDir, ClaudeDirName)
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var localHooks []string
	for _, event := range trackedHookEvents {
		localHooks = append(localHooks,
			`"`+event+`": [{"hooks": [{"type": "command", "command": "/home/user/.claude/hooks/wave-agents-hook.sh"}]}]`)
	}
	local := `{"hooks": {` + strings.Join(localHooks, ",") + `}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.local.json"), []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installClaudeHooksInHome(homeDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(claudeDir, "settings.json")); !os.IsNotExist(err) {
		t.Error("settings.json created even though settings.local.json covers all events")
	}
	if hookInstallNeeded(homeDir) {
		t.Error("hookInstallNeeded true although local settings register all events")
	}
}

func TestInstallRejectsInvalidSettings(t *testing.T) {
	homeDir := t.TempDir()
	settingsPath := writeTestSettings(t, homeDir, "{not valid json")
	err := installClaudeHooksInHome(homeDir)
	if err == nil {
		t.Fatal("expected error on invalid settings.json")
	}
	data, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "{not valid json" {
		t.Error("invalid settings.json was modified")
	}
}

func TestMergedOutputIsValidJson(t *testing.T) {
	merged, err := mergeHookRegistrations([]byte(testSettingsFixture), "/home/user/.claude/hooks/wave-agents-hook.sh", trackedHookEvents)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(merged) {
		t.Fatal("merged output is not valid JSON")
	}
	var parsed struct {
		Hooks map[string][]claudeHookGroup `json:"hooks"`
	}
	if err := json.Unmarshal(merged, &parsed); err != nil {
		t.Fatal(err)
	}
	var stopCommands []string
	for _, group := range parsed.Hooks["Stop"] {
		for _, hook := range group.Hooks {
			stopCommands = append(stopCommands, hook.Command)
		}
	}
	if len(stopCommands) != 2 {
		t.Fatalf("expected 2 Stop hooks (existing + wave), got %v", stopCommands)
	}
	if !strings.Contains(stopCommands[0], "$HOME/bridge") {
		t.Error("existing Stop hook not first / not preserved")
	}
}
