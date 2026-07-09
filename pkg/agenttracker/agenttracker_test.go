// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttracker

import (
	"os"
	"testing"

	"github.com/wavetermdev/waveterm/pkg/wshrpc"
)

func resetTracker() {
	globalTracker.lock.Lock()
	defer globalTracker.lock.Unlock()
	globalTracker.sessions = make(map[string]*wshrpc.AgentSessionInfo)
}

func applyEvents(t *testing.T, events []hookEvent) {
	t.Helper()
	globalTracker.lock.Lock()
	defer globalTracker.lock.Unlock()
	for i := range events {
		globalTracker.applyEvent_withlock(&events[i])
	}
}

func TestResumeCandidateKilledSession(t *testing.T) {
	resetTracker()
	// interactive session killed with the terminal: SIGHUP produces
	// SessionEnd reason "other", which must keep candidacy
	applyEvents(t, []hookEvent{
		{Ts: 100, Event: HookEvent_SessionStart, SessionId: "s1", BlockId: "b1", Model: "m"},
		{Ts: 200, Event: HookEvent_SessionEnd, SessionId: "s1", BlockId: "b1", Reason: "other"},
	})
	if got := GetResumeCandidate("b1"); got != "s1" {
		t.Errorf("killed session should be resume candidate, got %q", got)
	}
	if got := GetResumeCandidate("b2"); got != "" {
		t.Errorf("other block should have no candidate, got %q", got)
	}
}

func TestResumeCandidateDeliberateExit(t *testing.T) {
	resetTracker()
	applyEvents(t, []hookEvent{
		{Ts: 100, Event: HookEvent_SessionStart, SessionId: "s1", BlockId: "b1", Model: "m"},
		{Ts: 200, Event: HookEvent_SessionEnd, SessionId: "s1", BlockId: "b1", Reason: "prompt_input_exit"},
	})
	if got := GetResumeCandidate("b1"); got != "" {
		t.Errorf("deliberately exited session must not resume, got %q", got)
	}
}

func TestResumeCandidateIgnoresPrintMode(t *testing.T) {
	resetTracker()
	// a claude -p run (no model in SessionStart) inside the block, more recent
	// than the interactive session, must not steal or clear candidacy
	applyEvents(t, []hookEvent{
		{Ts: 100, Event: HookEvent_SessionStart, SessionId: "s1", BlockId: "b1", Model: "m"},
		{Ts: 200, Event: HookEvent_SessionStart, SessionId: "s2", BlockId: "b1"},
		{Ts: 300, Event: HookEvent_SessionEnd, SessionId: "s2", BlockId: "b1", Reason: "other"},
		{Ts: 400, Event: HookEvent_SessionEnd, SessionId: "s1", BlockId: "b1", Reason: "other"},
	})
	if got := GetResumeCandidate("b1"); got != "s1" {
		t.Errorf("print-mode session must be ignored, want s1, got %q", got)
	}
}

func TestResumeCandidateReviveClearsEndReason(t *testing.T) {
	resetTracker()
	// deliberate exit followed by a resume of the same session id (resume
	// keeps the id) re-arms candidacy; the next kill keeps it armed
	applyEvents(t, []hookEvent{
		{Ts: 100, Event: HookEvent_SessionStart, SessionId: "s1", BlockId: "b1", Model: "m"},
		{Ts: 200, Event: HookEvent_SessionEnd, SessionId: "s1", BlockId: "b1", Reason: "prompt_input_exit"},
		{Ts: 300, Event: HookEvent_SessionStart, SessionId: "s1", BlockId: "b1", Model: "m"},
		{Ts: 400, Event: HookEvent_SessionEnd, SessionId: "s1", BlockId: "b1", Reason: "other"},
	})
	if got := GetResumeCandidate("b1"); got != "s1" {
		t.Errorf("revived session should be candidate again, got %q", got)
	}
}

func TestResumeCandidateLatestWins(t *testing.T) {
	resetTracker()
	// two interactive sessions in one block: the most recently active one
	// decides; if it was deliberately exited, do not fall back to the older one
	applyEvents(t, []hookEvent{
		{Ts: 100, Event: HookEvent_SessionStart, SessionId: "s1", BlockId: "b1", Model: "m"},
		{Ts: 200, Event: HookEvent_SessionStart, SessionId: "s2", BlockId: "b1", Model: "m"},
		{Ts: 300, Event: HookEvent_SessionEnd, SessionId: "s2", BlockId: "b1", Reason: "prompt_input_exit"},
	})
	if got := GetResumeCandidate("b1"); got != "" {
		t.Errorf("latest session deliberately exited, no fallback expected, got %q", got)
	}
}

func TestResumeCandidateSkipsLiveProcess(t *testing.T) {
	resetTracker()
	// session whose pid is still alive (e.g. claude inside tmux survived the
	// Wave restart) must not get a second client attached
	applyEvents(t, []hookEvent{
		{Ts: 100, Event: HookEvent_SessionStart, SessionId: "s1", BlockId: "b1", Model: "m", Pid: os.Getpid()},
	})
	if got := GetResumeCandidate("b1"); got != "" {
		t.Errorf("live-pid session must not be resumed, got %q", got)
	}
}
