package bot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/rs/zerolog"
)

// End-to-end proof of the notification feature: a real config.json on disk, read
// by the real loader, reaching a real HTTP endpoint through the real battle
// report path. The unit tests cover payloads; this covers the wiring in between,
// which is where a feature like this usually dies (config block dropped on load,
// sender never constructed, call site missing).
func TestNotifyEndToEndFromConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLASHGO_CONFIG_DIR", dir)

	delivered := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case delivered <- string(body):
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfgJSON := `{"notify":{"enabled":true,"format":"discord","webhook_url":"` + srv.URL + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := config.Load("config.json")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !cfg.Notify.Enabled || cfg.Notify.WebhookURL != srv.URL {
		t.Fatalf("loaded notify block = %+v, want the enabled webhook", cfg.Notify)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := &Bot{cfg: cfg, logger: zerolog.Nop()}
	b.notifier = newEventNotifier(ctx, cfg.Notify, zerolog.Nop())
	if b.notifier == nil {
		t.Fatal("a valid notify config produced no sender")
	}

	b.recordAttackReport(AttackReport{
		Strategy:         "Auto EDrag Rush",
		TargetEdge:       "BottomRight",
		Stars:            3,
		GoldStolen:       500000,
		ElixirStolen:     400000,
		DarkElixirStolen: 3000,
		DeploySuccess:    true,
		ParsedResults:    true,
	})

	select {
	case body := <-delivered:
		if !strings.Contains(body, "3★") || !strings.Contains(body, "500000") {
			t.Fatalf("delivered %q, want the star count and the loot", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("attack report never reached the configured webhook")
	}
}
