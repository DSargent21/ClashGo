package main

// Live smoke mode for the packaged app. It is opt-in via --live-smoke (or
// CLASHGO_LIVE_SMOKE=1), so a normal Finder launch never starts a raid.
//
// The mode runs the same Wails-bound StartBot/StopBot path as the window,
// waits for exactly one attack, verifies stats.json + attack_history.json,
// then asks Wails to quit. A terminal launch gets the real exit code:
//
//	ClashGO.app/Contents/MacOS/ClashGO --live-smoke

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const liveSmokeTimeout = 12 * time.Minute

var liveSmokeExitCode atomic.Int32

// liveSmokeRequested reports whether this launch is an explicit self-test.
// The env form exists so launch scripts can opt in without rebuilding argv.
func liveSmokeRequested() bool {
	if os.Getenv("CLASHGO_LIVE_SMOKE") == "1" {
		return true
	}
	for _, arg := range os.Args[1:] {
		if arg == "--live-smoke" || arg == "-live-smoke" {
			return true
		}
	}
	return false
}

type liveSmokeReport struct {
	Start            bot.BotStats
	End              bot.BotStats
	HistoryBefore    int
	HistoryAfter     int
	PersistedStats   bot.BotStats
	PersistedHistory []bot.AttackReport
}

// runLiveSmoke is the goroutine started from Wails OnStartup. It owns the
// quit so the process exits only after StopBot and the persistence checks.
func (a *App) runLiveSmoke(ctx context.Context) {
	defer runtime.Quit(ctx)

	report, err := a.runLiveSmokeOneRaid(liveSmokeTimeout)
	if err != nil {
		liveSmokeExitCode.Store(1)
		fmt.Fprintf(os.Stderr, "LIVE SMOKE FAIL: %v\n", err)
		return
	}

	liveSmokeExitCode.Store(0)
	fmt.Printf("LIVE SMOKE PASS account=%s attacks=%d->%d history=%d->%d gold=%d elixir=%d de=%d persisted-attacks=%d persisted-history=%d\n",
		paths.Profile(), report.Start.AttacksCompleted, report.End.AttacksCompleted,
		report.HistoryBefore, report.HistoryAfter,
		report.End.TotalGold, report.End.TotalElixir, report.End.TotalDE,
		report.PersistedStats.AttacksCompleted, len(report.PersistedHistory))
}

// runLiveSmokeOneRaid starts the bot, waits for one completed attack, stops
// it, and validates both the live counters and the files on disk. The caller
// must have selected the active profile and initialized the logger.
func (a *App) runLiveSmokeOneRaid(timeout time.Duration) (liveSmokeReport, error) {
	var report liveSmokeReport

	cfg := config.LoadOrDefault("config.json")
	report.Start = a.GetStats()
	report.HistoryBefore = len(a.GetAttackHistory())

	status := a.StartBot(
		cfg.Search.MinLootGold,
		cfg.Search.MinLootElixir,
		cfg.Search.MinLootDarkElixir,
		cfg.Upgrade.UpgradeWalls,
		cfg.Search.Enabled,
	)
	if !status.Running {
		return report, fmt.Errorf("StartBot did not start: %s", status.Message)
	}

	stopped := false
	defer func() {
		if !stopped {
			a.StopBot()
			time.Sleep(time.Second)
		}
	}()

	deadline := time.Now().Add(timeout)
	for {
		current := a.GetStats()
		if current.AttacksCompleted > report.Start.AttacksCompleted {
			break
		}
		if !a.IsRunning() && !a.isStarting() {
			return report, fmt.Errorf("bot stopped before completing an attack\n%s", liveSmokeLogTail(a))
		}
		if time.Now().After(deadline) {
			return report, fmt.Errorf("timed out after %s waiting for one attack\n%s", timeout, liveSmokeLogTail(a))
		}
		time.Sleep(time.Second)
	}

	stop := a.StopBot()
	if stop.Running {
		return report, fmt.Errorf("StopBot still reports running: %s", stop.Message)
	}
	for i := 0; i < 100 && a.IsRunning(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if a.IsRunning() {
		return report, fmt.Errorf("bot still running after StopBot\n%s", liveSmokeLogTail(a))
	}
	stopped = true

	// Let the detached teardown bank the session stats and invalidate the
	// history cache before checking the final values.
	time.Sleep(time.Second)
	a.refreshHistory()

	report.End = a.GetStats()
	report.HistoryAfter = len(a.GetAttackHistory())
	delta := report.End.AttacksCompleted - report.Start.AttacksCompleted
	if delta != 1 {
		return report, fmt.Errorf("expected exactly one attack, got %d\n%s", delta, liveSmokeLogTail(a))
	}
	if report.HistoryAfter <= report.HistoryBefore {
		return report, fmt.Errorf("attack history did not grow: %d -> %d\n%s",
			report.HistoryBefore, report.HistoryAfter, liveSmokeLogTail(a))
	}

	var err error
	report.PersistedStats, err = waitForPersistedStats(report.End.AttacksCompleted)
	if err != nil {
		return report, err
	}
	report.PersistedHistory, err = waitForPersistedHistory(report.HistoryBefore)
	if err != nil {
		return report, err
	}
	return report, nil
}

func liveSmokeLogTail(a *App) string {
	logs := a.GetLogs()
	if len(logs) > 20 {
		logs = logs[len(logs)-20:]
	}
	return strings.Join(logs, "\n")
}

func waitForPersistedStats(wantAttacks int32) (bot.BotStats, error) {
	path := paths.ResolveConfig("stats.json")
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			var s bot.BotStats
			if err := json.Unmarshal(data, &s); err == nil && s.AttacksCompleted >= wantAttacks {
				return s, nil
			} else if err != nil {
				lastErr = err
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return bot.BotStats{}, fmt.Errorf("stats.json did not persist attacks=%d: %v", wantAttacks, lastErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func waitForPersistedHistory(wantAtLeast int) ([]bot.AttackReport, error) {
	path := paths.ResolveConfig("attack_history.json")
	deadline := time.Now().Add(10 * time.Second)
	var lastErr error
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			var history []bot.AttackReport
			if err := json.Unmarshal(data, &history); err == nil && len(history) > wantAtLeast {
				return history, nil
			} else if err != nil {
				lastErr = err
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("attack_history.json did not grow past %d: %v", wantAtLeast, lastErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
