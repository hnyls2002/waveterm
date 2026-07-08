// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

import {
    AgentsPanelModel,
    AgentStatus_Attention,
    AgentStatus_Ended,
    AgentStatus_Idle,
    AgentStatus_Working,
} from "@/app/agentspanel/agentspanel-model";
import { Tooltip } from "@/app/element/tooltip";
import { cn } from "@/util/util";
import { useAtomValue } from "jotai";
import { memo, useCallback } from "react";

const StatusOrder = [AgentStatus_Attention, AgentStatus_Working, AgentStatus_Idle, AgentStatus_Ended];
const StatusLabels: Record<string, string> = {
    [AgentStatus_Attention]: "Needs Attention",
    [AgentStatus_Working]: "Working",
    [AgentStatus_Idle]: "Idle",
    [AgentStatus_Ended]: "Ended",
};
const MaxEndedShown = 8;

function statusDotClass(status: string): string {
    switch (status) {
        case AgentStatus_Attention:
            return "bg-warning animate-pulse";
        case AgentStatus_Working:
            return "bg-accent animate-pulse";
        case AgentStatus_Idle:
            return "bg-secondary";
        default:
            return "bg-muted opacity-50";
    }
}

function formatRelTime(tsMs: number, nowMs: number): string {
    if (!tsMs) {
        return "";
    }
    const deltaSec = Math.max(0, Math.floor((nowMs - tsMs) / 1000));
    if (deltaSec < 60) {
        return `${deltaSec}s`;
    }
    if (deltaSec < 3600) {
        return `${Math.floor(deltaSec / 60)}m`;
    }
    if (deltaSec < 86400) {
        return `${Math.floor(deltaSec / 3600)}h`;
    }
    return `${Math.floor(deltaSec / 86400)}d`;
}

function baseName(path: string): string {
    if (!path) {
        return "";
    }
    const parts = path.split("/");
    return parts[parts.length - 1] || path;
}

const AgentSessionRow = memo(
    ({
        session,
        nowMs,
        knownWorkspaceIds,
    }: {
        session: AgentSessionInfo;
        nowMs: number;
        knownWorkspaceIds: string[];
    }) => {
        const model = AgentsPanelModel.getInstance();
        const isExternal = !!session.workspaceid && !knownWorkspaceIds.includes(session.workspaceid);
        const clickable = !!session.blockid && !isExternal && session.status !== AgentStatus_Ended;
        const title = session.lastprompt || `session ${session.sessionid.slice(0, 8)}`;
        return (
            <div
                className={cn(
                    "px-2 py-1.5 rounded-md",
                    clickable && "hover:bg-hoverbg cursor-pointer",
                    session.status === AgentStatus_Ended && "opacity-60"
                )}
                onClick={clickable ? () => model.focusSession(session) : undefined}
                title={isExternal ? `${session.cwd} (running in another Wave instance)` : session.cwd}
            >
                <div className="flex items-center gap-2 min-w-0">
                    <div className={cn("w-2 h-2 rounded-full shrink-0", statusDotClass(session.status))} />
                    <div className="text-sm text-primary truncate min-w-0 flex-1">{title}</div>
                    {isExternal && (
                        <div className="text-xxs text-muted border border-border rounded px-1 shrink-0">external</div>
                    )}
                </div>
                <div className="text-xs text-muted truncate pl-4">
                    {baseName(session.cwd)} · {formatRelTime(session.updatedts, nowMs)}
                </div>
                {session.status === AgentStatus_Attention && session.lastnotification && (
                    <div className="text-xs text-warning truncate pl-4">{session.lastnotification}</div>
                )}
            </div>
        );
    }
);
AgentSessionRow.displayName = "AgentSessionRow";

const AgentsPanel = memo(() => {
    const model = AgentsPanelModel.getInstance();
    const sessions = useAtomValue(model.sessionsAtom);
    const nowMs = useAtomValue(model.nowAtom);
    const width = useAtomValue(model.widthAtom);
    const attentionCount = useAtomValue(model.attentionCountAtom);
    const knownWorkspaceIds = useAtomValue(model.knownWorkspaceIdsAtom);

    const handleResizeMouseDown = useCallback((e: React.MouseEvent) => {
        e.preventDefault();
        const m = AgentsPanelModel.getInstance();
        const startX = e.clientX;
        const startWidth = (document.getElementById("agents-panel-root")?.offsetWidth ?? 340) as number;
        const onMove = (ev: MouseEvent) => {
            m.setWidth(startWidth + (ev.clientX - startX));
        };
        const onUp = () => {
            document.removeEventListener("mousemove", onMove);
            document.removeEventListener("mouseup", onUp);
        };
        document.addEventListener("mousemove", onMove);
        document.addEventListener("mouseup", onUp);
    }, []);

    const groups = StatusOrder.map((status) => {
        let items = sessions.filter((s) => s.status === status).sort((a, b) => b.updatedts - a.updatedts);
        if (status === AgentStatus_Ended) {
            items = items.slice(0, MaxEndedShown);
        }
        return { status, items };
    }).filter((g) => g.items.length > 0);

    return (
        <div
            id="agents-panel-root"
            style={{ width }}
            className="h-full flex flex-col shrink-0 relative border-r border-border bg-panel"
        >
            <div
                className="absolute right-0 top-0 bottom-0 w-1 cursor-col-resize hover:bg-accentbg z-10"
                onMouseDown={handleResizeMouseDown}
            />
            <div className="flex items-center gap-2 px-3 py-2 border-b border-border shrink-0">
                <i className="fa-sharp fa-solid fa-robot text-secondary" />
                <div className="text-sm font-semibold text-primary">Agents</div>
                {attentionCount > 0 && (
                    <div className="text-xxs px-1.5 py-0.5 rounded-full bg-warning text-black font-semibold">
                        {attentionCount}
                    </div>
                )}
                <div className="flex-1" />
                <i
                    className="fa-sharp fa-solid fa-arrows-rotate text-muted hover:text-primary cursor-pointer p-1"
                    title="Refresh"
                    onClick={() => model.refresh()}
                />
                <i
                    className="fa-sharp fa-solid fa-xmark text-muted hover:text-primary cursor-pointer p-1"
                    title="Close (Cmd:Shift:G)"
                    onClick={() => model.setVisible(false)}
                />
            </div>
            <div className="flex-1 overflow-y-auto px-2 py-2 flex flex-col gap-3">
                {groups.length === 0 && (
                    <div className="text-sm text-muted px-2 py-4 flex flex-col gap-2">
                        <div>No Claude Code sessions tracked yet.</div>
                        <div className="text-xs">
                            Sessions started in Wave terminals register here automatically via Claude Code hooks.
                        </div>
                    </div>
                )}
                {groups.map((group) => (
                    <div key={group.status}>
                        <div className="text-xxs font-semibold uppercase tracking-wide text-muted px-2 pb-1">
                            {StatusLabels[group.status]} ({group.items.length})
                        </div>
                        {group.items.map((session) => (
                            <AgentSessionRow
                                key={session.sessionid}
                                session={session}
                                nowMs={nowMs}
                                knownWorkspaceIds={knownWorkspaceIds}
                            />
                        ))}
                    </div>
                ))}
            </div>
        </div>
    );
});
AgentsPanel.displayName = "AgentsPanel";

const AgentsButton = memo(() => {
    const model = AgentsPanelModel.getInstance();
    const panelOpen = useAtomValue(model.visibleAtom);
    const attentionCount = useAtomValue(model.attentionCountAtom);

    return (
        <Tooltip
            content="Toggle Agents Panel (Cmd:Shift:G)"
            placement="bottom"
            hideOnClick
            divClassName={`relative flex h-[22px] px-3.5 justify-end mb-1 items-center rounded-md mr-1 box-border cursor-pointer bg-hover hover:bg-hoverbg transition-colors text-[12px] ${panelOpen ? "text-accent" : "text-secondary"}`}
            divStyle={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
            divOnClick={() => model.toggle()}
        >
            <i className="fa fa-robot" />
            {attentionCount > 0 && (
                <div className="absolute top-0 right-1 w-2 h-2 rounded-full bg-warning animate-pulse" />
            )}
        </Tooltip>
    );
});
AgentsButton.displayName = "AgentsButton";

export { AgentsButton, AgentsPanel };
