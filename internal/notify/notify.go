// Package notify delivers an unattended run's events to an external webhook:
// Discord, Telegram, or any endpoint that accepts a JSON `text` field.
//
// The farm runs with nobody watching it, so the events that matter — an attack
// result, a village that halted, a scheduler pause — are only useful if they
// leave the machine. Desktop notifications already cover a human sitting at the
// same Mac (tools/run_bot_keepalive.sh); this covers the phone.
//
// Delivery is deliberately fire-and-forget. Send never blocks the capture loop
// or the attack goroutine, and a webhook that is down must never change what
// the bot does: messages queue on one worker goroutine, and a full queue drops
// the event with a log line rather than applying backpressure to the bot.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// Format is the wire shape of the configured endpoint.
type Format string

const (
	// FormatDiscord posts {"content": "..."} to a Discord webhook URL.
	FormatDiscord Format = "discord"
	// FormatTelegram posts sendMessage to the Bot API.
	FormatTelegram Format = "telegram"
	// FormatGeneric posts {"text","kind","at"} — Slack and most self-hosted
	// receivers accept the text field and ignore the rest.
	FormatGeneric Format = "generic"
)

// Config is the notify block of config.json, already flattened.
type Config struct {
	Enabled        bool
	Format         Format
	WebhookURL     string
	TelegramToken  string
	TelegramChatID string
}

// Message is one event. Lines are rendered in order under the title.
type Message struct {
	Kind  string // "attack", "halt", "resume"
	Title string
	Lines []string
	At    time.Time
}

// Sender is what the bot calls. It is non-blocking and reports nothing back:
// a failed notification is a log line, not a bot failure.
type Sender interface {
	Send(Message)
}

const (
	// queueSize bounds the messages waiting on the worker. Attacks are minutes
	// apart, so this only ever fills during a delivery outage — exactly when
	// dropping the oldest is better than growing without bound.
	queueSize = 16

	requestTimeout = 10 * time.Second

	// retryDelay is the pause before the single retry. A webhook's 5xx is
	// usually its own hiccup; an attack report lost to one blip is gone.
	retryDelay = 2 * time.Second

	telegramAPI = "https://api.telegram.org"

	// maxTextLen keeps a message inside Discord's 2000-character limit and
	// realistic phone screens. Truncation is by rune so a multi-byte character
	// is never split.
	maxTextLen = 1800

	// errBodyLimit caps how much of an error response is read (and thrown
	// away) before the connection is reused. The payload is never surfaced.
	errBodyLimit int64 = 512
)

// New returns a Sender, or nil when notifications are off or misconfigured.
//
// A nil return is the normal, silent case for every install that never enabled
// this, so callers only have to nil-check. A misconfigured block is logged
// once at construction and then behaves as disabled, because a bad URL must not
// take a farming session down.
func New(ctx context.Context, cfg Config, logger zerolog.Logger) Sender {
	cfg = cfg.normalized()
	if !cfg.Enabled {
		return nil
	}
	if err := cfg.validate(); err != nil {
		logger.Warn().Err(err).Msg("webhook notifications are enabled but misconfigured; running without them")
		return nil
	}
	s := &sender{
		cfg:    cfg,
		logger: logger.With().Str("component", "notify").Logger(),
		http:   &http.Client{Timeout: requestTimeout},
		queue:  make(chan Message, queueSize),
	}
	logger.Info().Str("format", string(cfg.Format)).Msg("webhook notifications enabled")
	go s.run(ctx)
	return s
}

// normalized trims and lowercases the user-editable fields so "Discord" in a
// hand-edited config.json is the same format as "discord".
func (c Config) normalized() Config {
	c.Format = Format(strings.ToLower(strings.TrimSpace(string(c.Format))))
	c.WebhookURL = strings.TrimSpace(c.WebhookURL)
	c.TelegramToken = strings.TrimSpace(c.TelegramToken)
	c.TelegramChatID = strings.TrimSpace(c.TelegramChatID)
	return c
}

// validate reports why a config cannot address an endpoint. Each message names
// the config key the operator has to fix.
func (c Config) validate() error {
	switch c.Format {
	case FormatTelegram:
		if c.TelegramToken == "" {
			return errors.New("notify.format=telegram needs notify.telegram_token")
		}
		if c.TelegramChatID == "" {
			return errors.New("notify.format=telegram needs notify.telegram_chat_id")
		}
		return nil
	case FormatDiscord, FormatGeneric:
		u, err := url.Parse(c.WebhookURL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("notify.format=%s needs notify.webhook_url to be an absolute http(s) URL", c.Format)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("notify.webhook_url must be http(s), not %q", u.Scheme)
		}
		return nil
	default:
		return fmt.Errorf("notify.format %q is not one of discord/telegram/generic", c.Format)
	}
}

// request renders a message for the configured format: the endpoint to POST to
// and the JSON body.
func (c Config) request(m Message) (string, []byte, error) {
	text := formatText(m)
	switch c.Format {
	case FormatTelegram:
		body, err := json.Marshal(struct {
			ChatID                string `json:"chat_id"`
			Text                  string `json:"text"`
			DisableWebPagePreview bool   `json:"disable_web_page_preview"`
		}{ChatID: c.TelegramChatID, Text: text, DisableWebPagePreview: true})
		if err != nil {
			return "", nil, err
		}
		return telegramAPI + "/bot" + c.TelegramToken + "/sendMessage", body, nil
	case FormatDiscord:
		body, err := json.Marshal(struct {
			Content string `json:"content"`
		}{Content: text})
		return c.WebhookURL, body, err
	default:
		body, err := json.Marshal(struct {
			Text string `json:"text"`
			Kind string `json:"kind"`
			At   string `json:"at"`
		}{Text: text, Kind: m.Kind, At: m.At.UTC().Format(time.RFC3339)})
		return c.WebhookURL, body, err
	}
}

// formatText is the human-readable body every format carries. Plain text, no
// markdown: Telegram's default parse mode renders "**" literally, so a single
// shape that reads correctly everywhere beats per-format styling.
func formatText(m Message) string {
	title := strings.TrimSpace(m.Title)
	if title == "" {
		title = "event"
	}
	var b strings.Builder
	b.WriteString("ClashGO: ")
	b.WriteString(title)
	for _, line := range m.Lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			continue
		}
		b.WriteString("\n")
		b.WriteString(line)
	}
	out := b.String()
	if runes := []rune(out); len(runes) > maxTextLen {
		out = string(runes[:maxTextLen-1]) + "…"
	}
	return out
}

// sender is the one implementation: a queue, one worker, and an HTTP client.
type sender struct {
	cfg    Config
	logger zerolog.Logger
	http   *http.Client
	queue  chan Message
	// dropped counts events lost to a full queue. Read atomically so a Send on
	// the capture loop never contends with the worker.
	dropped atomic.Uint64
}

// Send queues an event and returns immediately.
func (s *sender) Send(m Message) {
	if m.At.IsZero() {
		m.At = time.Now()
	}
	select {
	case s.queue <- m:
	default:
		n := s.dropped.Add(1)
		s.logger.Warn().
			Str("kind", m.Kind).
			Uint64("dropped_total", n).
			Msg("notification queue is full; dropping this event rather than delaying the bot")
	}
}

func (s *sender) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.flush()
			return
		case m := <-s.queue:
			s.deliver(ctx, m)
		}
	}
}

// flush delivers what is already queued after the session context ends.
// The attack report is written as the last act of an attack, so cancelling on
// Stop or on --once dropped exactly the notification an operator cares about:
// measured live, a 1-star attack with 532k loot produced no webhook at all while
// the same event delivered fine mid-session. Each message gets its own short
// timeout; nothing here can extend a shutdown past a few seconds.
func (s *sender) flush() {
	for {
		select {
		case m := <-s.queue:
			dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			s.deliver(dctx, m)
			cancel()
		default:
			return
		}
	}
}

// deliver posts one message, retrying once on a transient failure. Runs only on
// the worker goroutine, so the retry never touches the bot's own path.
func (s *sender) deliver(ctx context.Context, m Message) {
	endpoint, body, err := s.cfg.request(m)
	if err != nil {
		s.logger.Warn().Err(err).Str("kind", m.Kind).Msg("notification could not be encoded; dropped")
		return
	}

	err = s.post(ctx, endpoint, body)
	if err == nil {
		return
	}
	if !retryable(err) {
		s.logger.Warn().Err(err).Str("kind", m.Kind).Msg("webhook rejected the notification; dropped")
		return
	}

	select {
	case <-ctx.Done():
		return
	case <-time.After(retryDelay):
	}
	if err := s.post(ctx, endpoint, body); err != nil {
		s.logger.Warn().Err(err).Str("kind", m.Kind).Msg("webhook delivery failed twice; notification dropped")
	}
}

// statusError is a non-2xx response. The body is never carried: a webhook can
// echo the request, and the Telegram URL contains the bot token.
type statusError struct{ Code int }

func (e statusError) Error() string { return fmt.Sprintf("webhook HTTP %d", e.Code) }

// retryable decides whether the single retry is worth spending: 5xx and 429 are
// the provider's own trouble, a dropped connection is ambiguous, and a 4xx
// means the URL or payload is wrong — retrying that only wastes the queue.
func retryable(err error) bool {
	var se statusError
	if errors.As(err, &se) {
		return se.Code >= 500 || se.Code == http.StatusTooManyRequests
	}
	return true
}

func (s *sender) post(ctx context.Context, endpoint string, body []byte) error {
	// WithoutCancel: the session context ends the moment a run is stopped, and
	// the attack report is written just before that. Inheriting the cancelled
	// context aborted the POST mid-flight and the notification vanished even
	// though it was already out of the queue. Delivery keeps its own request
	// timeout, so a shutdown is still bounded.
	reqCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		// A *url.Error quotes the full URL, and for Telegram that URL is
		// ".../bot<token>/sendMessage". Unwrap to the cause so a token can never
		// reach a log line.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, errBodyLimit))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError{Code: resp.StatusCode}
	}
	return nil
}
