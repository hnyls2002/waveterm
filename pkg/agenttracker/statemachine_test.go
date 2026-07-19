// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package agenttracker

import (
	"testing"

	"github.com/wavetermdev/waveterm/pkg/wshrpc"
)

func applyTestEvent(t *testing.T, tracker *AgentTracker, event *hookEvent) *statusTransition {
	t.Helper()
	_, transition := tracker.applyEvent_withlock(event)
	return transition
}

func TestStopFailureTransitions(t *testing.T) {
	tracker := &AgentTracker{sessions: make(map[string]*wshrpc.AgentSessionInfo)}
	sessionId := "sess-1"
	applyTestEvent(t, tracker, &hookEvent{Event: HookEvent_SessionStart, SessionId: sessionId, BlockId: "blk-1", Pid: 123})
	applyTestEvent(t, tracker, &hookEvent{Event: HookEvent_UserPromptSubmit, SessionId: sessionId, Prompt: "do stuff"})
	if got := tracker.sessions[sessionId].Status; got != Status_Working {
		t.Fatalf("after prompt: status = %s, want %s", got, Status_Working)
	}

	transition := applyTestEvent(t, tracker, &hookEvent{Event: HookEvent_StopFailure, SessionId: sessionId, Message: "API Error: 529 Overloaded"})
	session := tracker.sessions[sessionId]
	if session.Status != Status_Error {
		t.Fatalf("after StopFailure: status = %s, want %s", session.Status, Status_Error)
	}
	if session.LastNotification != "API Error: 529 Overloaded" {
		t.Errorf("LastNotification = %q, want the error text", session.LastNotification)
	}
	if transition == nil {
		t.Fatal("StopFailure produced no transition")
	}
	badge := badgeForTransition(*transition)
	if badge == nil {
		t.Fatal("error transition produced no badge")
	}
	if badge.Color != BadgeColor_Error || badge.Priority != BadgePriority_Error {
		t.Errorf("error badge = color %s priority %v, want %s / %v", badge.Color, badge.Priority, BadgeColor_Error, BadgePriority_Error)
	}

	applyTestEvent(t, tracker, &hookEvent{Event: HookEvent_UserPromptSubmit, SessionId: sessionId, Prompt: "retry"})
	if got := tracker.sessions[sessionId].Status; got != Status_Working {
		t.Fatalf("after retry prompt: status = %s, want %s", got, Status_Working)
	}
}

func TestStopAfterErrorShowsDoneBadge(t *testing.T) {
	tracker := &AgentTracker{sessions: make(map[string]*wshrpc.AgentSessionInfo)}
	sessionId := "sess-2"
	applyTestEvent(t, tracker, &hookEvent{Event: HookEvent_SessionStart, SessionId: sessionId, BlockId: "blk-2", Pid: 0})
	applyTestEvent(t, tracker, &hookEvent{Event: HookEvent_StopFailure, SessionId: sessionId, Message: "unknown"})
	transition := applyTestEvent(t, tracker, &hookEvent{Event: HookEvent_Stop, SessionId: sessionId})
	if transition == nil {
		t.Fatal("Stop after error produced no transition")
	}
	badge := badgeForTransition(*transition)
	if badge == nil || badge.Color != BadgeColor_Done {
		t.Errorf("Stop after error should show the done badge, got %+v", badge)
	}
}

func TestTerminalBadgesCarryUnseenFade(t *testing.T) {
	testCases := []struct {
		transition statusTransition
		wantIcon   string
	}{
		{statusTransition{blockId: "blk", oldStatus: Status_Idle, newStatus: Status_Working}, "spinner+spin"},
		{statusTransition{blockId: "blk", oldStatus: Status_Working, newStatus: Status_Attention}, "bell+fade"},
		{statusTransition{blockId: "blk", oldStatus: Status_Working, newStatus: Status_Error}, "triangle-exclamation+fade"},
		{statusTransition{blockId: "blk", oldStatus: Status_Working, newStatus: Status_Idle}, "check+fade"},
		{statusTransition{blockId: "blk", oldStatus: "", newStatus: Status_Idle}, "robot"},
	}
	for _, tc := range testCases {
		badge := badgeForTransition(tc.transition)
		if badge == nil {
			t.Fatalf("transition %s->%s produced no badge", tc.transition.oldStatus, tc.transition.newStatus)
		}
		if badge.Icon != tc.wantIcon {
			t.Errorf("transition %s->%s: icon = %q, want %q", tc.transition.oldStatus, tc.transition.newStatus, badge.Icon, tc.wantIcon)
		}
	}
}
