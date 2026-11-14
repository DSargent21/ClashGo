package main

import (
	"testing"

	"github.com/Ducky705/ClashGO/internal/bot"
)

// Pacing fields describe the live scheduler, not a session total. Summing them
// (the way loot is accumulated) would make "attacks today" climb past its cap
// after a restart and make a halted bot report a stale reason forever.
func TestMergeStatsPassesPacingThrough(t *testing.T) {
	acc := bot.BotStats{AttacksCompleted: 5, TotalGold: 1000}
	cur := bot.BotStats{
		AttacksCompleted: 2,
		TotalGold:        500,
		ScheduleEnabled:  true,
		ScheduleState:    "BREAK",
		HaltedReason:     "scheduler",
		AttacksToday:     7,
		MaxAttacksPerDay: 50,
		NextChangeAt:     "2026-11-14T01:00:00Z",
	}

	got := mergeStats(acc, cur)

	if got.AttacksCompleted != 7 || got.TotalGold != 1500 {
		t.Fatalf("session totals not accumulated: attacks=%d gold=%d", got.AttacksCompleted, got.TotalGold)
	}
	if got.AttacksToday != 7 {
		t.Fatalf("attacks_today = %d, want the live value 7 (not accumulated)", got.AttacksToday)
	}
	if !got.ScheduleEnabled || got.ScheduleState != "BREAK" || got.HaltedReason != "scheduler" {
		t.Fatalf("pacing fields lost in merge: %+v", got)
	}
	if got.MaxAttacksPerDay != 50 || got.NextChangeAt != "2026-11-14T01:00:00Z" {
		t.Fatalf("cap/next-change lost in merge: %+v", got)
	}
}
