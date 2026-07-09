#!/bin/bash
# Copyright 2026, Command Line Inc.
# SPDX-License-Identifier: Apache-2.0
#
# Claude Code lifecycle hook for the Wave agents panel.
#
# Register this script in ~/.claude/settings.json for the SessionStart,
# UserPromptSubmit, Stop, Notification, and SessionEnd hook events. It appends
# one JSON line per event to ~/.claude/wave-agents/events.jsonl, which wavesrv
# (pkg/agenttracker) tails to drive the Agents panel.
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
        prompt: (.prompt // ""),
        message: (.message // ""),
        source: (.source // ""),
        reason: (.reason // ""),
        model: (.model // ""),
        blockid: $blockid,
        tabid: $tabid,
        workspaceid: $workspaceid,
        pid: $pid
    }' >> "$dir/events.jsonl" 2>/dev/null

exit 0
