// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

import { sortBadgesForTab } from "@/app/store/badge";
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
