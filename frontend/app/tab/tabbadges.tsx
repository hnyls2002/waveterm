// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

import { sortBadgesForTab } from "@/app/store/badge";
import { validateCssColor } from "@/util/color-validator";
import { cn, makeIconClass } from "@/util/util";
import { useMemo } from "react";
import { v7 as uuidv7 } from "uuid";

export interface TabBadgesProps {
    badges?: Badge[] | null;
    flagColor?: string | null;
    className?: string;
}

const DefaultClassName =
    "pointer-events-none absolute left-[4px] top-1/2 z-[3] flex h-[20px] -translate-y-1/2 items-center justify-center gap-[3px] px-[2px] py-[1px]";

const MaxTabBadges = 3;
// keep in sync with DefaultClassName metrics: 12px icon + 3px gap per slot,
// plus the left offset and padding around the row
const BadgeIconSlotPx = 15;
const BadgeAreaBasePx = 6;

export function sanitizeFlagColor(rawColor: unknown): string | null {
    if (!rawColor || typeof rawColor !== "string") {
        return null;
    }
    try {
        validateCssColor(rawColor);
        return rawColor;
    } catch {
        return null;
    }
}

// extra horizontal space the badge row occupies inside a tab; the tab bar
// widens each tab by this amount so badges never eat into the title
export function getTabBadgeExtraWidth(badges: Badge[] | null, flagColor: string | null): number {
    const count = Math.min((badges?.length ?? 0) + (flagColor ? 1 : 0), MaxTabBadges);
    if (count === 0) {
        return 0;
    }
    return BadgeAreaBasePx + count * BadgeIconSlotPx;
}

export function TabBadges({ badges, flagColor, className }: TabBadgesProps) {
    const flagBadgeId = useMemo(() => uuidv7(), []);
    const allBadges = useMemo(() => {
        const base = badges ?? [];
        if (!flagColor) {
            return base;
        }
        const flagBadge: Badge = { icon: "flag", color: flagColor, priority: 0, badgeid: flagBadgeId };
        return sortBadgesForTab([...base, flagBadge]);
    }, [badges, flagColor, flagBadgeId]);
    if (!allBadges[0]) {
        return null;
    }
    const shownBadges = allBadges.slice(0, MaxTabBadges);
    return (
        <div className={cn(DefaultClassName, className)}>
            {shownBadges.map((badge, idx) => (
                <i
                    key={badge.badgeid ?? idx}
                    className={makeIconClass(badge.icon, true, { defaultIcon: "circle-small" }) + " text-[12px]"}
                    style={{ color: badge.color || "#fbbf24" }}
                />
            ))}
        </div>
    );
}
