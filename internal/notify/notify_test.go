package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestNewReturnsNilWhenDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if s := New(ctx, Config{Format: FormatDiscord, WebhookURL: "https://example.com/x"}, zerolog.Nop()); s != nil {
		t.Fatal("notifications are off by default; New must return nil so the bot does no work")
	}
}

func TestNewRejectsMisconfigured(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cases := []struct {
		name string
		cfg  Config
	}{
		{"telegram without token", Config{Enabled: true, Format: FormatTelegram, TelegramChatID: "1"}},
		{"telegram without chat", Config{Enabled: true, Format: FormatTelegram, TelegramToken: "t"}},
		{"discord without url", Config{Enabled: true, Format: FormatDiscord}},
		{"relative url", Config{Enabled: true, Format: FormatDiscord, WebhookURL: "/just/a/path"}},
		{"non-http scheme", Config{Enabled: true, Format: FormatGeneric, WebhookURL: "ftp://example.com/hook"}},
		{"unknown format", Config{Enabled: true, Format: "carrier-pigeon", WebhookURL: "https://example.com/hook"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if s := New(ctx, tc.cfg, zerolog.Nop()); s != nil {
				t.Fatal("a config that cannot address an endpoint must disable notifications, not crash the run")
			}
		})
	}
}

func TestFormatIsNormalized(t *testing.T) {
	got := Config{Format: "  Discord "}.normalized()
	if got.Format != FormatDiscord {
		t.Fatalf("format = %q, want %q: a hand-edited config must not be silently ignored", got.Format, FormatDiscord)
	}
}

func TestRequestPerFormat(t *testing.T) {
	msg := Message{Kind: "attack", Title: "2★ attack", Lines: []string{"Loot: 800000 gold"}, At: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}

	t.Run("discord", func(t *testing.T) {
		cfg := Config{Format: FormatDiscord, WebhookURL: "https://discord.example/hook"}
		endpoint, body, err := cfg.request(msg)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if endpoint != cfg.WebhookURL {
			t.Fatalf("endpoint = %q, want the webhook URL", endpoint)
		}
		var got struct{ Content string }
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("body is not JSON: %v", err)
		}
		if !strings.Contains(got.Content, "2★ attack") || !strings.Contains(got.Content, "Loot: 800000 gold") {
			t.Fatalf("content = %q, want title and line", got.Content)
		}
	})

	t.Run("telegram", func(t *testing.T) {
		cfg := Config{Format: FormatTelegram, TelegramToken: "TOKEN", TelegramChatID: "42"}
		endpoint, body, err := cfg.request(msg)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if endpoint != telegramAPI+"/botTOKEN/sendMessage" {
			t.Fatalf("endpoint = %q, want the Bot API sendMessage URL", endpoint)
		}
		var got struct {
			ChatID                string `json:"chat_id"`
			Text                  string `json:"text"`
			DisableWebPagePreview bool   `json:"disable_web_page_preview"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("body is not JSON: %v", err)
		}
		if got.ChatID != "42" || !got.DisableWebPagePreview || !strings.Contains(got.Text, "ClashGO: 2★ attack") {
			t.Fatalf("payload = %+v, want chat id, no preview, and the rendered text", got)
		}
	})

	t.Run("generic", func(t *testing.T) {
		cfg := Config{Format: FormatGeneric, WebhookURL: "https://hooks.example/x"}
		_, body, err := cfg.request(msg)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		var got map[string]string
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("body is not JSON: %v", err)
		}
		if got["kind"] != "attack" || got["at"] != "2026-11-14T12:00:00Z" || !strings.Contains(got["text"], "Loot: 800000 gold") {
			t.Fatalf("payload = %v, want text, kind and an RFC3339 timestamp", got)
		}
	})
}

func TestFormatTextCapsRuneLength(t *testing.T) {
	msg := Message{Title: "big", Lines: []string{strings.Repeat("é", 5000)}}
	out := formatText(msg)
	if n := len([]rune(out)); n > maxTextLen {
		t.Fatalf("rendered %d runes, want <= %d", n, maxTextLen)
	}
	if !strings.HasSuffix(out, "…") {
		t.Fatal("truncated text should say so with an ellipsis")
	}
}

func TestSendDelivers(t *testing.T) {
	var (
		mu   sync.Mutex
		body string
		hit  = make(chan struct{}, 1)
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = string(b)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		select {
		case hit <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, Config{Enabled: true, Format: FormatDiscord, WebhookURL: srv.URL}, zerolog.Nop())
	if s == nil {
		t.Fatal("a valid discord config must produce a sender")
	}

	s.Send(Message{Kind: "attack", Title: "3★ attack", Lines: []string{"Loot: 1 gold"}})
	select {
	case <-hit:
	case <-time.After(5 * time.Second):
		t.Fatal("message never reached the endpoint")
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(body, "3★ attack") {
		t.Fatalf("delivered body = %q, want the rendered message", body)
	}
}

func TestRetriesOnceOnServerError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &sender{
		cfg:    Config{Format: FormatDiscord, WebhookURL: srv.URL},
		logger: zerolog.Nop(),
		http:   srv.Client(),
		queue:  make(chan Message, 1),
	}

	s.deliver(ctx, Message{Kind: "attack", Title: "retry me"})
	if got := hits.Load(); got != 2 {
		t.Fatalf("server saw %d requests, want 2 (one retry after a 5xx)", got)
	}
}

func TestDoesNotRetryClientError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &sender{
		cfg:    Config{Format: FormatDiscord, WebhookURL: srv.URL},
		logger: zerolog.Nop(),
		http:   srv.Client(),
		queue:  make(chan Message, 1),
	}

	s.deliver(ctx, Message{Kind: "attack", Title: "bad payload"})
	if got := hits.Load(); got != 1 {
		t.Fatalf("server saw %d requests, want 1: a 4xx will not fix itself", got)
	}
}

func TestQueueFullDropsWithoutBlocking(t *testing.T) {
	s := &sender{
		cfg:    Config{Format: FormatDiscord, WebhookURL: "https://example.com/x"},
		logger: zerolog.Nop(),
		http:   &http.Client{},
		queue:  make(chan Message, 1),
	}
	// No worker running: the second Send must not block the caller.
	s.Send(Message{Kind: "attack", Title: "first"})
	done := make(chan struct{})
	go func() {
		s.Send(Message{Kind: "attack", Title: "second"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Send blocked on a full queue; the capture loop would stall with it")
	}
	if got := s.dropped.Load(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
}

func TestTransportErrorNeverLeaksToken(t *testing.T) {
	s := &sender{
		cfg:    Config{Format: FormatTelegram, TelegramToken: "SECRET-TOKEN", TelegramChatID: "1"},
		logger: zerolog.Nop(),
		http:   &http.Client{Timeout: time.Second},
		queue:  make(chan Message, 1),
	}
	// Port 1 on loopback refuses immediately; the URL still carries the token,
	// which the error must not echo.
	err := s.post(context.Background(), "http://127.0.0.1:1/botSECRET-TOKEN/sendMessage", []byte(`{"text":"x"}`))
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("error leaks the bot token: %v", err)
	}
}
