//go:build livesmoke

package main

// Live smoke: starts the Wails App backend (the same StartBot/StopBot
// bindings the window calls) against the connected BlueStacks device,
// waits for exactly one attack, stops the bot, and verifies both the
// live counters and the persisted stats/history files. It does not open
// a Wails window, so it is safe to run from a terminal or CI host that
// has a display-less Go test process.
//
// This spends one real army. Run it explicitly:
//
//	make smoke-live

import (
	"testing"

	"github.com/Ducky705/ClashGO/internal/logger"
	"github.com/Ducky705/ClashGO/internal/paths"
)

func TestLiveWailsSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("live smoke spends a real attack")
	}

	a := NewApp()
	logger.Init(false, &WailsLogWriter{app: a})

	store, err := a.ensureAccounts()
	if err != nil {
		t.Fatalf("load accounts: %v", err)
	}
	acct, ok := store.Active()
	if !ok {
		t.Fatal("no active account")
	}
	if err := paths.SetProfile(acct.ID); err != nil {
		t.Fatalf("select active profile %q: %v", acct.ID, err)
	}
	a.lastStats = readAccountStats(acct.ID)

	t.Logf("live smoke: account=%s (%s) device=%s:%d", acct.Name, acct.ID, acct.Device.ADBHost, acct.Device.ADBPort)
	report, err := a.runLiveSmokeOneRaid(liveSmokeTimeout)
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("baseline: attacks=%d history=%d gold=%d elixir=%d de=%d",
		report.Start.AttacksCompleted, report.HistoryBefore,
		report.Start.TotalGold, report.Start.TotalElixir, report.Start.TotalDE)
	t.Logf("result: attacks=%d->%d loot=%d gold/%d elixir/%d de history=%d->%d",
		report.Start.AttacksCompleted, report.End.AttacksCompleted,
		report.End.TotalGold, report.End.TotalElixir, report.End.TotalDE,
		report.HistoryBefore, report.HistoryAfter)
	t.Logf("persisted: attacks=%d history=%d",
		report.PersistedStats.AttacksCompleted, len(report.PersistedHistory))
}
