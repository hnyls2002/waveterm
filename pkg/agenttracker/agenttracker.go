// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

// Package agenttracker maintains a registry of Claude Code sessions running inside
// Wave terminal blocks. Claude Code lifecycle hooks append JSON events to
// ~/.claude/wave-agents/events.jsonl; this package tails that file, applies a small
// status state machine, and notifies the frontend via a WPS event.
package agenttracker

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
	"github.com/shirou/gopsutil/v4/process"
	"github.com/wavetermdev/waveterm/pkg/baseds"
	"github.com/wavetermdev/waveterm/pkg/panichandler"
	"github.com/wavetermdev/waveterm/pkg/waveobj"
	"github.com/wavetermdev/waveterm/pkg/wps"
	"github.com/wavetermdev/waveterm/pkg/wshrpc"
)

const (
	Status_Working   = "working"
	Status_Idle      = "idle"
	Status_Attention = "attention"
	Status_Error     = "error"
	Status_Ended     = "ended"
)

const (
	BadgeColor_Error     = "#ef4444"
	BadgeColor_Attention = "#f59e0b"
	BadgeColor_Done      = "#22c55e"
	BadgeColor_Working   = "#3b82f6"
	BadgeColor_Idle      = "#6b7280"
	// priorities only order badges across blocks in the tab aggregation
	// (broken beats act-now beats result-ready beats in-progress beats
	// at-prompt); within a block the tracker replaces unconditionally via Force
	BadgePriority_Error     = 4
	BadgePriority_Attention = 3
	BadgePriority_Done      = 2
	BadgePriority_Working   = 1
	BadgePriority_Idle      = 0
)

const (
	HookEvent_SessionStart      = "SessionStart"
	HookEvent_UserPromptSubmit  = "UserPromptSubmit"
	HookEvent_Stop              = "Stop"
	HookEvent_Notification      = "Notification"
	HookEvent_PermissionRequest = "PermissionRequest"
	HookEvent_PostToolUse       = "PostToolUse"
	HookEvent_SessionEnd        = "SessionEnd"
	HookEvent_StopFailure       = "StopFailure"
)

// Claude Code fires this notification 60s after every Stop while sitting at
// the prompt; it carries no new information (the session is already idle/done),
// so it must not escalate the session to attention.
const IdleNotificationMessage = "Claude is waiting for your input"

const (
	EventsDirName    = ".claude/wave-agents"
	EventsFileName   = "events.jsonl"
	LivenessInterval = 30 * time.Second
	// grace period before a dead PID marks the session ended (hook PPID can be an
	// intermediate shell that exits immediately, so a fresh event resets the clock)
	LivenessGraceMs   = 60 * 1000
	EndedRetentionMs  = 24 * 60 * 60 * 1000
	MaxTextSnippetLen = 300
	// hooks append to events.jsonl forever; bound startup replay cost and disk growth
	MaxReplayBytes = 2 * 1024 * 1024
	RotateFileSize = 8 * 1024 * 1024
)

type hookEvent struct {
	Ts             int64  `json:"ts"`
	Event          string `json:"event"`
	SessionId      string `json:"sessionid"`
	TranscriptPath string `json:"transcriptpath,omitempty"`
	Cwd            string `json:"cwd,omitempty"`
	BlockId        string `json:"blockid,omitempty"`
	TabId          string `json:"tabid,omitempty"`
	WorkspaceId    string `json:"workspaceid,omitempty"`
	Pid            int    `json:"pid,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	Message        string `json:"message,omitempty"`
}

type statusTransition struct {
	blockId   string
	oldStatus string
	newStatus string
}

type AgentTracker struct {
	lock       sync.Mutex
	sessions   map[string]*wshrpc.AgentSessionInfo
	readOffset int64
	eventsPath string
}

var globalTracker = &AgentTracker{sessions: make(map[string]*wshrpc.AgentSessionInfo)}

func InitAgentTracker() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("agenttracker: cannot resolve home dir: %v\n", err)
		return
	}
	eventsDir := filepath.Join(homeDir, filepath.FromSlash(EventsDirName))
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		log.Printf("agenttracker: cannot create events dir: %v\n", err)
		return
	}
	globalTracker.eventsPath = filepath.Join(eventsDir, EventsFileName)
	rotateEventsFileIfLarge(globalTracker.eventsPath)
	// bound the replay: start at the last MaxReplayBytes; the partial first
	// line fails json parsing and is skipped
	if finfo, err := os.Stat(globalTracker.eventsPath); err == nil && finfo.Size() > MaxReplayBytes {
		globalTracker.readOffset = finfo.Size() - MaxReplayBytes
	}
	// replay must not publish per-event badges (it would re-fire every historical
	// transition); instead publish each live session's final status once, so
	// badges survive a wavesrv restart
	numEvents, _ := globalTracker.readNewEventsInternal()
	for _, session := range ListSessions() {
		if session.Status == Status_Ended {
			continue
		}
		publishBadgeForTransition(statusTransition{blockId: session.BlockId, newStatus: session.Status})
	}
	log.Printf("agenttracker: initialized, replayed %d events, %d sessions\n", numEvents, len(ListSessions()))
	go watchLoop(eventsDir)
	go livenessLoop()
	go hookInstallPromptIfNeeded()
}

// rotateEventsFileIfLarge renames an oversized log aside (one .old generation
// kept). Live sessions re-register on their next event (applyEvent upserts),
// so rotation only costs ended-session history.
func rotateEventsFileIfLarge(eventsPath string) {
	finfo, err := os.Stat(eventsPath)
	if err != nil || finfo.Size() <= RotateFileSize {
		return
	}
	oldPath := eventsPath + ".old"
	if err := os.Rename(eventsPath, oldPath); err != nil {
		log.Printf("agenttracker: cannot rotate events file: %v\n", err)
		return
	}
	log.Printf("agenttracker: rotated events file (%d bytes)\n", finfo.Size())
}

func ListSessions() []wshrpc.AgentSessionInfo {
	globalTracker.lock.Lock()
	defer globalTracker.lock.Unlock()
	rtn := make([]wshrpc.AgentSessionInfo, 0, len(globalTracker.sessions))
	for _, session := range globalTracker.sessions {
		rtn = append(rtn, *session)
	}
	return rtn
}

func publishUpdate() {
	wps.Broker.Publish(wps.WaveEvent{Event: wps.Event_AgentTrackerUpdate})
}

func watchLoop(eventsDir string) {
	defer func() {
		panichandler.PanicHandler("agenttracker:watchLoop", recover())
	}()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("agenttracker: fsnotify unavailable (%v), falling back to polling\n", err)
		pollLoop()
		return
	}
	defer watcher.Close()
	if err := watcher.Add(eventsDir); err != nil {
		log.Printf("agenttracker: cannot watch events dir (%v), falling back to polling\n", err)
		pollLoop()
		return
	}
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(event.Name) != EventsFileName {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			if globalTracker.readNewEvents() > 0 {
				publishUpdate()
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func pollLoop() {
	defer func() {
		panichandler.PanicHandler("agenttracker:pollLoop", recover())
	}()
	for {
		time.Sleep(2 * time.Second)
		if globalTracker.readNewEvents() > 0 {
			publishUpdate()
		}
	}
}

// readNewEvents reads complete lines appended since the last read offset and
// applies them to the registry. Returns the number of events applied.
func (t *AgentTracker) readNewEvents() int {
	numApplied, transitions := t.readNewEventsInternal()
	for _, transition := range coalesceTransitions(transitions) {
		publishBadgeForTransition(transition)
	}
	return numApplied
}

func (t *AgentTracker) readNewEventsInternal() (int, []statusTransition) {
	t.lock.Lock()
	defer t.lock.Unlock()
	file, err := os.Open(t.eventsPath)
	if err != nil {
		return 0, nil
	}
	defer file.Close()
	finfo, err := file.Stat()
	if err != nil {
		return 0, nil
	}
	if finfo.Size() < t.readOffset {
		// file was truncated or replaced; start over
		t.readOffset = 0
	}
	if finfo.Size() == t.readOffset {
		return 0, nil
	}
	if _, err := file.Seek(t.readOffset, io.SeekStart); err != nil {
		return 0, nil
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return 0, nil
	}
	numApplied := 0
	consumed := 0
	var transitions []statusTransition
	for {
		idx := bytes.IndexByte(data[consumed:], '\n')
		if idx < 0 {
			// trailing partial line (writer mid-append); leave it for the next read
			break
		}
		line := data[consumed : consumed+idx]
		consumed += idx + 1
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var event hookEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		applied, transition := t.applyEvent_withlock(&event)
		if applied {
			numApplied++
		}
		if transition != nil {
			transitions = append(transitions, *transition)
		}
	}
	t.readOffset += int64(consumed)
	return numApplied, transitions
}

// applyEvent_withlock upserts the session so that hooks installed mid-session
// still register on the next prompt/stop, not just on SessionStart. Returns
// whether anything user-visible changed (used to skip pushing frontend updates
// for high-frequency events like PostToolUse that only refresh liveness) and
// the status transition, if any, so the caller can publish badges outside the lock.
func (t *AgentTracker) applyEvent_withlock(event *hookEvent) (bool, *statusTransition) {
	if event.SessionId == "" {
		return false, nil
	}
	visibleChange := false
	isNewSession := false
	session := t.sessions[event.SessionId]
	if session == nil {
		session = &wshrpc.AgentSessionInfo{
			SessionId: event.SessionId,
			Status:    Status_Idle,
			StartTs:   event.Ts,
		}
		t.sessions[event.SessionId] = session
		visibleChange = true
		isNewSession = true
	}
	if event.Cwd != "" && event.Cwd != session.Cwd {
		session.Cwd = event.Cwd
		visibleChange = true
	}
	if event.TranscriptPath != "" {
		session.TranscriptPath = event.TranscriptPath
	}
	if event.BlockId != "" && event.BlockId != session.BlockId {
		session.BlockId = event.BlockId
		session.TabId = event.TabId
		session.WorkspaceId = event.WorkspaceId
		visibleChange = true
	}
	if event.Pid > 0 {
		session.Pid = event.Pid
	}
	session.UpdatedTs = event.Ts
	oldStatus := session.Status
	if isNewSession {
		// a brand-new session counts as a transition so its badge shows up
		// even though the initial status (idle) never "changed"
		oldStatus = ""
	}
	switch event.Event {
	case HookEvent_SessionStart:
		// resume of an ended session revives it in place (StartTs preserved)
		session.Status = Status_Idle
	case HookEvent_UserPromptSubmit:
		session.Status = Status_Working
		if event.Prompt != "" && event.Prompt != session.LastPrompt {
			session.LastPrompt = truncateText(event.Prompt)
			visibleChange = true
		}
	case HookEvent_Stop:
		// don't resurrect a truly-exited claude (Stop can trail SessionEnd),
		// but let a liveness false-positive heal if the process is alive
		if session.Status != Status_Ended || pidAlive(session.Pid) {
			session.Status = Status_Idle
		}
	case HookEvent_Notification, HookEvent_PermissionRequest, HookEvent_StopFailure:
		if event.Event == HookEvent_Notification && event.Message == IdleNotificationMessage {
			break
		}
		if event.Event == HookEvent_StopFailure {
			// the turn died on an API error; Stop never fires on this path, so
			// without this the session would spin as working forever
			session.Status = Status_Error
		} else {
			session.Status = Status_Attention
		}
		if event.Message != "" && event.Message != session.LastNotification {
			session.LastNotification = truncateText(event.Message)
			visibleChange = true
		}
	case HookEvent_PostToolUse:
		// a tool completing means a pending permission request was approved
		if session.Status == Status_Attention {
			session.Status = Status_Working
		}
	case HookEvent_SessionEnd:
		session.Status = Status_Ended
	}
	var transition *statusTransition
	if session.Status != oldStatus {
		visibleChange = true
		transition = &statusTransition{blockId: session.BlockId, oldStatus: oldStatus, newStatus: session.Status}
	}
	return visibleChange, transition
}

func livenessLoop() {
	defer func() {
		panichandler.PanicHandler("agenttracker:livenessLoop", recover())
	}()
	for {
		time.Sleep(LivenessInterval)
		if reconcileLiveness() {
			publishUpdate()
		}
	}
}

// reconcileLiveness ends sessions whose claude process is gone (covers kill -9 /
// crashes where the SessionEnd hook never fires) and prunes old ended sessions.
func reconcileLiveness() bool {
	changed, transitions := reconcileLivenessInternal()
	for _, transition := range coalesceTransitions(transitions) {
		publishBadgeForTransition(transition)
	}
	return changed
}

func reconcileLivenessInternal() (bool, []statusTransition) {
	globalTracker.lock.Lock()
	defer globalTracker.lock.Unlock()
	nowMs := time.Now().UnixMilli()
	changed := false
	var transitions []statusTransition
	for sessionId, session := range globalTracker.sessions {
		if session.Status == Status_Ended {
			if nowMs-session.UpdatedTs > EndedRetentionMs {
				delete(globalTracker.sessions, sessionId)
				changed = true
			}
			continue
		}
		if session.Pid <= 0 || nowMs-session.UpdatedTs < LivenessGraceMs {
			continue
		}
		exists, err := process.PidExists(int32(session.Pid))
		if err == nil && !exists {
			transitions = append(transitions, statusTransition{blockId: session.BlockId, oldStatus: session.Status, newStatus: Status_Ended})
			session.Status = Status_Ended
			session.UpdatedTs = nowMs
			changed = true
		}
	}
	return changed, transitions
}

// publishBadgeForTransition mirrors the session status onto its block's badge:
// working -> spinner, attention -> bell, finished working -> check; session
// start/end clears. Exactly one Force-set (or clear) event per transition:
// WPS delivery is not ordered, so a clear+set pair can arrive as set+clear
// and wipe the badge it just set.
func publishBadgeForTransition(transition statusTransition) {
	if transition.blockId == "" {
		return
	}
	oref := waveobj.MakeORef(waveobj.OType_Block, transition.blockId).String()
	badge := badgeForTransition(transition)
	if badge == nil {
		publishBadgeEvent(baseds.BadgeEvent{ORef: oref, Clear: true})
		return
	}
	publishBadgeEvent(baseds.BadgeEvent{ORef: oref, Badge: badge, Force: true})
}

// coalesceTransitions keeps only the last transition per block within one
// batch, so exactly one badge event per block is published per batch
// (unordered WPS delivery could apply a rapid pair reversed). The last
// transition carries the block's true end state and its immediate
// predecessor, so a same-batch prompt+stop still yields the done badge.
func coalesceTransitions(transitions []statusTransition) []statusTransition {
	if len(transitions) <= 1 {
		return transitions
	}
	var blockOrder []string
	byBlock := make(map[string]statusTransition)
	for _, transition := range transitions {
		if _, ok := byBlock[transition.blockId]; !ok {
			blockOrder = append(blockOrder, transition.blockId)
		}
		byBlock[transition.blockId] = transition
	}
	rtn := make([]statusTransition, 0, len(blockOrder))
	for _, blockId := range blockOrder {
		rtn = append(rtn, byBlock[blockId])
	}
	return rtn
}

// isActiveStatus reports whether the session is mid-turn; a transition from an
// active status to idle means the turn finished and earns the done badge
func isActiveStatus(status string) bool {
	return status == Status_Working || status == Status_Attention || status == Status_Error
}

func badgeForTransition(transition statusTransition) *baseds.Badge {
	badgeId, err := uuid.NewV7()
	if err != nil {
		return nil
	}
	// PidLinked exempts these badges from the focus auto-clear: they are status
	// indicators owned by the tracker, which clears them on the next transition
	// (new prompt, session end, liveness reconcile), not on being seen. The
	// "+fade" modifier on the terminal states (done/attention/error) encodes
	// unseen: focusing the block marks the badge seen (MarkSeenById), which
	// strips the fade so the badge stops blinking but stays visible.
	switch {
	case transition.newStatus == Status_Working:
		return &baseds.Badge{BadgeId: badgeId.String(), Icon: "spinner+spin", Color: BadgeColor_Working, Priority: BadgePriority_Working, PidLinked: true}
	case transition.newStatus == Status_Attention:
		return &baseds.Badge{BadgeId: badgeId.String(), Icon: "bell+fade", Color: BadgeColor_Attention, Priority: BadgePriority_Attention, PidLinked: true}
	case transition.newStatus == Status_Error:
		return &baseds.Badge{BadgeId: badgeId.String(), Icon: "triangle-exclamation+fade", Color: BadgeColor_Error, Priority: BadgePriority_Error, PidLinked: true}
	case transition.newStatus == Status_Idle && isActiveStatus(transition.oldStatus):
		return &baseds.Badge{BadgeId: badgeId.String(), Icon: "check+fade", Color: BadgeColor_Done, Priority: BadgePriority_Done, PidLinked: true}
	case transition.newStatus == Status_Idle:
		// fresh session sitting at the prompt (or revived by resume)
		return &baseds.Badge{BadgeId: badgeId.String(), Icon: "robot", Color: BadgeColor_Idle, Priority: BadgePriority_Idle, PidLinked: true}
	}
	return nil
}

func publishBadgeEvent(data baseds.BadgeEvent) {
	wps.Broker.Publish(wps.WaveEvent{
		Event:  wps.Event_Badge,
		Scopes: []string{data.ORef},
		Data:   data,
	})
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	exists, err := process.PidExists(int32(pid))
	return err == nil && exists
}

func truncateText(text string) string {
	runes := []rune(text)
	if len(runes) <= MaxTextSnippetLen {
		return text
	}
	return string(runes[:MaxTextSnippetLen]) + "..."
}
