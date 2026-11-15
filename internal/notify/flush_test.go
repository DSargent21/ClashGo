package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// An event queued just before the session ends must still be delivered. The
// attack report is produced as the last act of an attack, so cancelling the
// context (Stop, or --once after the first battle) used to drop exactly the
// notification the feature exists for: a 1-star attack with 532k loot was
// written to disk but produced no webhook at all.
func TestQueuedEventIsDeliveredWhenContextEnds(t *testing.T) {
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case got <- string(body):
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, Config{
		Enabled:    true,
		Format:     FormatDiscord,
		WebhookURL: srv.URL,
	}, zerolog.Nop())
	if s == nil {
		t.Fatal("notifier did not start")
	}

	// Queue, then end the session immediately: the worker goroutine may not
	// have picked the message up yet, so only the flush can deliver it.
	s.Send(Message{Kind: "attack", Title: "1★ attack", Lines: []string{"Loot: 532983 gold"}})
	cancel()

	select {
	case body := <-got:
		if body == "" {
			t.Fatal("delivered an empty body")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("queued notification was dropped when the context ended")
	}
}
