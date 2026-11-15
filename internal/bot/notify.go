// Package bot — notify.go
//
// Nothing about a 24/7 farm is visible from the machine it runs on, so the
// events worth a human's attention leave through internal/notify: attack
// results, village halts, and scheduler pauses. The wiring lives here so bot.go
// stays about the capture loop.
//
// The notifier is nil whenever notifications are off, which is every install
// that never configured them, so each call site is a nil check and nothing more.
package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/notify"
	"github.com/rs/zerolog"
)

// eventSender is the bot's whole view of the notification transport. Keeping it
// an interface lets a test observe what the bot would send without an endpoint.
type eventSender interface {
	Send(notify.Message)
}

// newEventNotifier builds the configured webhook sender. A disabled or
// misconfigured block yields nil, which every call site treats as "say nothing".
func newEventNotifier(ctx context.Context, cfg config.NotifyConfig, logger zerolog.Logger) eventSender {
	s := notify.New(ctx, notify.Config{
		Enabled:        cfg.Enabled,
		Format:         notify.Format(cfg.Format),
		WebhookURL:     cfg.WebhookURL,
		TelegramToken:  cfg.TelegramToken,
		TelegramChatID: cfg.TelegramChatID,
	}, logger)
	if s == nil {
		return nil
	}
	return s
}

// notifyEvent reports one event. Nil notifier (the common case) costs nothing.
func (b *Bot) notifyEvent(kind, title string, lines ...string) {
	if b.notifier == nil {
		return
	}
	b.notifier.Send(notify.Message{Kind: kind, Title: title, Lines: lines, At: time.Now()})
}

// notifyAttackResult is the per-battle report — the one event an operator
// actually wants on their phone, because it is the only proof the farm is
// earning rather than looping.
//
// A result panel that could not be read is called out rather than reported as a
// zero-loot raid: the numbers are absent, not measured, and an alert that says
// "0 gold" for a successful attack is worse than one that says it could not
// read the panel.
func (b *Bot) notifyAttackResult(rep AttackReport) {
	title := fmt.Sprintf("%d★ attack", rep.Stars)
	deploy := "all units deployed"
	switch {
	case rep.DeployError != "":
		deploy = "deploy error: " + rep.DeployError
	case !rep.DeploySuccess:
		deploy = fmt.Sprintf("%d slots undeployed", rep.UndeployedSlots)
	}

	lines := make([]string, 0, 5)
	lines = append(lines, fmt.Sprintf("Strategy: %s (%s)", rep.Strategy, rep.TargetEdge))
	if rep.ParsedResults {
		lines = append(lines, fmt.Sprintf("Loot: %d gold · %d elixir · %d DE", rep.GoldStolen, rep.ElixirStolen, rep.DarkElixirStolen))
		if rep.BonusGold+rep.BonusElixir+rep.BonusDE > 0 {
			lines = append(lines, fmt.Sprintf("Bonus: %d gold · %d elixir · %d DE", rep.BonusGold, rep.BonusElixir, rep.BonusDE))
		}
	} else {
		lines = append(lines, "Loot unknown: the result panel was not read")
	}
	lines = append(lines, "Deploy: "+deploy)
	if rep.TotalAttacks > 0 {
		lines = append(lines, fmt.Sprintf("Attacks this session: %d", rep.TotalAttacks))
	}
	b.notifyEvent("attack", title, lines...)
}
