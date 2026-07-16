#!/bin/bash
# Copyright 2026, Command Line Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Claude Code lifecycle hook for the Wave agents panel.
#
# wavesrv installs this script to ~/.claude/hooks/ and registers it in
# ~/.claude/settings.json (pkg/agenttracker/hookinstall.go) for the SessionStart,
# UserPromptSubmit, Stop, Notification, PermissionRequest, PostToolUse,
# SessionEnd, and StopFailure hook events. PermissionRequest drives the
# attention status and PostToolUse flips attention back to working (and acts
# as a liveness heartbeat) -- without them the state machine sticks on stale
# states. StopFailure drives the error status: on an API-error turn abort Stop
# never fires, so it is the only signal that the session stopped working. The
# script appends one JSON line per event to ~/.claude/wave-agents/events.jsonl,
# which wavesrv (pkg/agenttracker) tails.
#
# Only sessions running inside Wave terminals are tracked: outside Wave,
# WAVETERM_BLOCKID is unset and the hook exits immediately.

[ -z "$WAVETERM_BLOCKID" ] && exit 0
command -v jq >/dev/null 2>&1 || exit 0

dir="$HOME/.claude/wave-agents"
mkdir -p "$dir" || exit 0

# $PPID is the process that spawned this hook shell, i.e. the claude process
# (used by the tracker for PID-liveness reconciliation).
jq -c \
    --arg blockid "$WAVETERM_BLOCKID" \
    --arg tabid "$WAVETERM_TABID" \
    --arg workspaceid "$WAVETERM_WORKSPACEID" \
    --argjson pid "$PPID" \
    '{
        ts: (now * 1000 | floor),
        event: .hook_event_name,
        sessionid: .session_id,
        transcriptpath: (.transcript_path // ""),
        cwd: (.cwd // ""),
        prompt: ((.prompt // "") | .[0:2000]),
        message: ((.message // "") | .[0:2000]),
        error: ((((.error // "") | tostring)
            + (if .error_details == null then "" else " - " + (.error_details | tostring) end))
            | .[0:2000]),
        blockid: $blockid,
        tabid: $tabid,
        workspaceid: $workspaceid,
        pid: $pid
    }' >> "$dir/events.jsonl" 2>/dev/null

exit 0
