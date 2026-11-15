package bot

import (
	"strings"
	"sync"
	"testing"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/notify"
	"github.com/rs/zerolog"
)

type recordingSender struct {
	mu   sync.Mutex
	msgs []notify.Message
}

func (r *recordingSender) Send(m notify.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
}

func (r *recordingSender) all() []notify.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Message(nil), r.msgs...)
}

// Each finished attack has to leave the machine: an unattended farm that only
// writes attack_history.json is invisible until someone opens the app.
func TestRecordAttackReportNotifies(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())

	rec := &recordingSender{}
	b := &Bot{cfg: config.DefaultConfig(), logger: zerolog.Nop(), notifier: rec}

	b.recordAttackReport(AttackReport{
		Strategy:         "Auto EDrag Rush",
		TargetEdge:       "TopLeft",
		Stars:            2,
		GoldStolen:       812345,
		ElixirStolen:     640000,
		DarkElixirStolen: 4200,
		DeploySuccess:    true,
		ParsedResults:    true,
		TotalAttacks:     7,
	})

	msgs := rec.all()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want exactly one per battle", len(msgs))
	}
	got := msgs[0]
	if got.Kind != "attack" {
		t.Fatalf("kind = %q, want attack", got.Kind)
	}
	if !strings.Contains(got.Title, "2★") {
		t.Fatalf("title = %q, want the star count", got.Title)
	}
	body := strings.Join(got.Lines, "\n")
	for _, want := range []string{"Auto EDrag Rush", "812345", "all units deployed", "7"} {
		if !strings.Contains(body, want) {
			t.Fatalf("message body %q is missing %q", body, want)
		}
	}
}

// An unread result panel is reported as unknown: "0 gold" would be a number the
// bot never measured, and an operator reading it would think the raid failed.
func TestAttackNotificationSaysWhenLootIsUnknown(t *testing.T) {
	rec := &recordingSender{}
	b := &Bot{cfg: config.DefaultConfig(), logger: zerolog.Nop(), notifier: rec}

	b.notifyAttackResult(AttackReport{Strategy: "Valkyrie Earthquake Spam", Stars: 1, ParsedResults: false, DeploySuccess: true})

	msgs := rec.all()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	body := strings.Join(msgs[0].Lines, "\n")
	if !strings.Contains(body, "unknown") {
		t.Fatalf("body = %q, want the unread-panel notice", body)
	}
	if strings.Contains(body, "Loot: 0") {
		t.Fatalf("body = %q must not claim a measured zero", body)
	}
}

// Notifications off is the default, so the recording helper must survive a nil
// notifier on every path that can fire during a battle.
func TestNotifyIsNilSafe(t *testing.T) {
	t.Setenv("CLASHGO_CONFIG_DIR", t.TempDir())
	b := &Bot{cfg: config.DefaultConfig(), logger: zerolog.Nop()}

	b.notifyEvent("halt", "nothing configured")
	b.notifyAttackResult(AttackReport{Stars: 3, DeploySuccess: true})
	b.recordAttackReport(AttackReport{Stars: 3, DeploySuccess: true})
}
