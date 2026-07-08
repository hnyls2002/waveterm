// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

import { globalStore } from "@/app/store/jotaiStore";
import { RpcApi } from "@/app/store/wshclientapi";
import { TabRpcClient } from "@/app/store/wshrpcutil";
import { getLayoutModelForStaticTab } from "@/layout/index";
import { atoms, getApi } from "@/store/global";
import { waveEventSubscribeSingle } from "@/store/wps";
import * as jotai from "jotai";

const MinPanelWidth = 260;
const MaxPanelWidth = 640;
const DefaultPanelWidth = 340;
const WidthStorageKey = "agentspanel:width";
const RefreshTickMs = 30000;
// delay before focusing a block after switching tabs (tab content needs to mount)
const CrossTabFocusDelayMs = 350;

export const AgentStatus_Attention = "attention";
export const AgentStatus_Working = "working";
export const AgentStatus_Idle = "idle";
export const AgentStatus_Ended = "ended";

function loadStoredWidth(): number {
    const raw = localStorage.getItem(WidthStorageKey);
    const parsed = parseInt(raw);
    if (isNaN(parsed)) {
        return DefaultPanelWidth;
    }
    return Math.max(MinPanelWidth, Math.min(parsed, MaxPanelWidth));
}

export class AgentsPanelModel {
    private static instance: AgentsPanelModel = null;

    visibleAtom = jotai.atom(false) as jotai.PrimitiveAtom<boolean>;
    sessionsAtom = jotai.atom<AgentSessionInfo[]>([]) as jotai.PrimitiveAtom<AgentSessionInfo[]>;
    nowAtom = jotai.atom(Date.now()) as jotai.PrimitiveAtom<number>;
    // workspace oids known to THIS instance; sessions from other Wave instances
    // (e.g. prod vs dev sharing the same hooks file) are shown but not navigable
    knownWorkspaceIdsAtom = jotai.atom<string[]>([]) as jotai.PrimitiveAtom<string[]>;
    widthAtom: jotai.PrimitiveAtom<number>;
    attentionCountAtom!: jotai.Atom<number>;

    private constructor() {
        this.widthAtom = jotai.atom(loadStoredWidth());
        this.attentionCountAtom = jotai.atom((get) => {
            return get(this.sessionsAtom).filter((s) => s.status === AgentStatus_Attention).length;
        });
        waveEventSubscribeSingle({
            eventType: "agenttracker:update",
            handler: () => this.refresh(),
        });
        setInterval(() => {
            globalStore.set(this.nowAtom, Date.now());
        }, RefreshTickMs);
        this.refresh();
    }

    static getInstance(): AgentsPanelModel {
        if (!AgentsPanelModel.instance) {
            AgentsPanelModel.instance = new AgentsPanelModel();
        }
        return AgentsPanelModel.instance;
    }

    async refresh() {
        try {
            const sessions = await RpcApi.AgentTrackerListCommand(TabRpcClient);
            globalStore.set(this.sessionsAtom, sessions ?? []);
        } catch (e) {
            console.log("agentspanel: error fetching sessions", e);
        }
        try {
            const workspaces = await RpcApi.WorkspaceListCommand(TabRpcClient);
            globalStore.set(
                this.knownWorkspaceIdsAtom,
                (workspaces ?? []).map((w) => w.workspacedata?.oid).filter((oid) => oid != null)
            );
        } catch (e) {
            console.log("agentspanel: error fetching workspace list", e);
        }
    }

    getVisible(): boolean {
        return globalStore.get(this.visibleAtom);
    }

    setVisible(visible: boolean) {
        globalStore.set(this.visibleAtom, visible);
        if (visible) {
            this.refresh();
        }
    }

    toggle() {
        this.setVisible(!this.getVisible());
    }

    setWidth(width: number) {
        const clamped = Math.max(MinPanelWidth, Math.min(width, MaxPanelWidth));
        globalStore.set(this.widthAtom, clamped);
        localStorage.setItem(WidthStorageKey, String(clamped));
    }

    canFocusSession(session: AgentSessionInfo): boolean {
        if (!session.blockid || !session.tabid) {
            return false;
        }
        return globalStore.get(this.knownWorkspaceIdsAtom).includes(session.workspaceid);
    }

    focusSession(session: AgentSessionInfo) {
        if (!this.canFocusSession(session)) {
            return;
        }
        const workspace = globalStore.get(atoms.workspace);
        if (session.workspaceid !== workspace?.oid) {
            // workspace switch either swaps this window's workspace or focuses the
            // window that already owns it; tab/block focus after that is racy, so
            // the user clicks again once the workspace is up
            getApi().switchWorkspace(session.workspaceid);
            return;
        }
        const currentTabId = globalStore.get(atoms.staticTabId);
        const sameTab = session.tabid === currentTabId;
        if (!sameTab) {
            getApi().setActiveTab(session.tabid);
        }
        setTimeout(
            () => {
                const layoutModel = getLayoutModelForStaticTab();
                const node = layoutModel?.getNodeByBlockId(session.blockid);
                if (node) {
                    layoutModel.focusNode(node.id);
                }
            },
            sameTab ? 0 : CrossTabFocusDelayMs
        );
    }
}
