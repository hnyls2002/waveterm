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
	"github.com/shirou/gopsutil/v4/process"
	"github.com/wavetermdev/waveterm/pkg/panichandler"
	"github.com/wavetermdev/waveterm/pkg/wps"
	"github.com/wavetermdev/waveterm/pkg/wshrpc"
)

const (
	Status_Working   = "working"
	Status_Idle      = "idle"
	Status_Attention = "attention"
	Status_Ended     = "ended"
)

const (
	HookEvent_SessionStart      = "SessionStart"
	HookEvent_UserPromptSubmit  = "UserPromptSubmit"
	HookEvent_Stop              = "Stop"
	HookEvent_Notification      = "Notification"
	HookEvent_PermissionRequest = "PermissionRequest"
	HookEvent_PostToolUse       = "PostToolUse"
	HookEvent_SessionEnd        = "SessionEnd"
)

const (
	EventsDirName    = ".claude/wave-agents"
	EventsFileName   = "events.jsonl"
	LivenessInterval = 30 * time.Second
	// grace period before a dead PID marks the session ended (hook PPID can be an
	// intermediate shell that exits immediately, so a fresh event resets the clock)
	LivenessGraceMs   = 60 * 1000
	EndedRetentionMs  = 24 * 60 * 60 * 1000
	MaxTextSnippetLen = 300
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
	numEvents := globalTracker.readNewEvents()
	log.Printf("agenttracker: initialized, replayed %d events, %d sessions\n", numEvents, len(ListSessions()))
	go watchLoop(eventsDir)
	go livenessLoop()
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
	t.lock.Lock()
	defer t.lock.Unlock()
	file, err := os.Open(t.eventsPath)
	if err != nil {
		return 0
	}
	defer file.Close()
	finfo, err := file.Stat()
	if err != nil {
		return 0
	}
	if finfo.Size() < t.readOffset {
		// file was truncated or replaced; start over
		t.readOffset = 0
	}
	if finfo.Size() == t.readOffset {
		return 0
	}
	if _, err := file.Seek(t.readOffset, io.SeekStart); err != nil {
		return 0
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return 0
	}
	numApplied := 0
	consumed := 0
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
		if t.applyEvent_withlock(&event) {
			numApplied++
		}
	}
	t.readOffset += int64(consumed)
	return numApplied
}

// applyEvent_withlock upserts the session so that hooks installed mid-session
// still register on the next prompt/stop, not just on SessionStart. Returns
// whether anything user-visible changed (used to skip pushing frontend updates
// for high-frequency events like PostToolUse that only refresh liveness).
func (t *AgentTracker) applyEvent_withlock(event *hookEvent) bool {
	if event.SessionId == "" {
		return false
	}
	visibleChange := false
	session := t.sessions[event.SessionId]
	if session == nil {
		session = &wshrpc.AgentSessionInfo{
			SessionId: event.SessionId,
			Status:    Status_Idle,
			StartTs:   event.Ts,
		}
		t.sessions[event.SessionId] = session
		visibleChange = true
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
		if session.Status != Status_Ended {
			session.Status = Status_Idle
		}
	case HookEvent_Notification, HookEvent_PermissionRequest:
		session.Status = Status_Attention
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
	if session.Status != oldStatus {
		visibleChange = true
	}
	return visibleChange
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
	globalTracker.lock.Lock()
	defer globalTracker.lock.Unlock()
	nowMs := time.Now().UnixMilli()
	changed := false
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
			session.Status = Status_Ended
			session.UpdatedTs = nowMs
			changed = true
		}
	}
	return changed
}

func truncateText(text string) string {
	runes := []rune(text)
	if len(runes) <= MaxTextSnippetLen {
		return text
	}
	return string(runes[:MaxTextSnippetLen]) + "..."
}
