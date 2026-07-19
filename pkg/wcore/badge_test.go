// Copyright 2026, Command Line Inc.
// SPDX-License-Identifier: Apache-2.0

package wcore

import (
	"testing"

	"github.com/wavetermdev/waveterm/pkg/baseds"
	"github.com/wavetermdev/waveterm/pkg/waveobj"
)

func TestMarkSeenById(t *testing.T) {
	defer clearAllBadges()
	oref := waveobj.ORef{OType: waveobj.OType_Block, OID: "11111111-1111-7111-8111-111111111111"}
	badgeId := "01900000-0000-7000-8000-000000000001"
	setBadge(oref, baseds.BadgeEvent{
		ORef:  oref.String(),
		Badge: &baseds.Badge{BadgeId: badgeId, Icon: "check+fade", Priority: 2},
	})

	setBadge(oref, baseds.BadgeEvent{ORef: oref.String(), MarkSeenById: "01900000-0000-7000-8000-000000000002"})
	if got := globalBadgeStore.transient[oref.String()].Icon; got != "check+fade" {
		t.Errorf("markseen with wrong id changed icon to %q", got)
	}

	setBadge(oref, baseds.BadgeEvent{ORef: oref.String(), MarkSeenById: badgeId})
	badge, ok := globalBadgeStore.transient[oref.String()]
	if !ok {
		t.Fatal("markseen removed the badge")
	}
	if badge.Icon != "check" {
		t.Errorf("markseen icon = %q, want %q", badge.Icon, "check")
	}
	if badge.BadgeId != badgeId {
		t.Errorf("markseen changed badgeid to %q", badge.BadgeId)
	}
}
