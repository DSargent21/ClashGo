package agent

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Ducky705/ClashGO/internal/bus"
	"github.com/nats-io/nats.go"
)

type Client struct {
	eventBus *bus.EventBus
	sub      *nats.Subscription
	deviceID string
	seq      atomic.Uint64
	latest   atomic.Value
}

type Option func(*options)

type options struct {
	natsURL string
}

func WithNATSURL(url string) Option {
	return func(o *options) { o.natsURL = url }
}

func NewBotClient(natsURL, deviceID string, opts ...Option) (*Client, error) {
	cfg := options{natsURL: natsURL}
	for _, opt := range opts {
		opt(&cfg)
	}
	e, err := bus.NewEventBus(cfg.natsURL, deviceID, bus.WithNoopOnFail(false))
	if err != nil {
		return nil, err
	}
	c := &Client{eventBus: e, deviceID: deviceID}
	sub, err := e.SubscribeState(func(ev *bus.GameStateEvent) {
		c.latest.Store(*ev)
	})
	if err != nil {
		_ = e.Close()
		return nil, err
	}
	c.sub = sub
	return c, nil
}

func (c *Client) EventBus() *bus.EventBus { return c.eventBus }

func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	if c.sub != nil {
		_ = c.sub.Unsubscribe()
	}
	if c.eventBus != nil {
		_ = c.eventBus.Flush(2 * time.Second)
		return c.eventBus.Close()
	}
	return nil
}

func (c *Client) GetState() *bus.GameStateEvent {
	if c == nil {
		return nil
	}
	v := c.latest.Load()
	if v == nil {
		return nil
	}
	ev, _ := v.(bus.GameStateEvent)
	return &ev
}

func (c *Client) WaitForState(target bus.GameState, timeout time.Duration) error {
	return c.WaitForCondition(func(ev *bus.GameStateEvent) bool {
		return ev != nil && ev.State == target
	}, timeout)
}

func (c *Client) WaitForCondition(fn func(*bus.GameStateEvent) bool, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn(c.GetState()) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for condition after %s", timeout)
}

func (c *Client) Tap(x, y int) error {
	return c.sendCommand("tap", map[string]interface{}{"x": x, "y": y})
}

func (c *Client) Swipe(x1, y1, x2, y2, durationMS int) error {
	return c.sendCommand("swipe", map[string]interface{}{"x": x1, "y": y1, "x2": x2, "y2": y2, "duration_ms": durationMS})
}

func (c *Client) Back() error {
	return c.sendCommand("back", nil)
}

func (c *Client) ZoomOut() error {
	return c.sendCommand("zoom_out", nil)
}

func (c *Client) ZoomIn() error {
	return c.sendCommand("zoom_in", nil)
}

func (c *Client) WaitForBotState(state string, timeout time.Duration) error {
	return c.sendCommand("wait_for_state", map[string]interface{}{"target_state": state, "duration_ms": timeout.Milliseconds()})
}

func (c *Client) Deploy(strategy string) error {
	return c.sendCommand("deploy", map[string]interface{}{"strategy": strategy})
}

func (c *Client) SkipBase() error {
	return c.sendCommand("skip_base", nil)
}

func (c *Client) ReturnHome() error {
	return c.sendCommand("return_home", nil)
}

func (c *Client) sendCommand(cmdType string, params map[string]interface{}) error {
	if c == nil || c.eventBus == nil {
		return fmt.Errorf("agent client not connected")
	}
	id := fmt.Sprintf("%s-%d-%d", c.deviceID, time.Now().UnixNano(), c.seq.Add(1))
	cmd := &bus.Command{
		TimestampMs: time.Now().UnixMilli(),
		CommandId:   id,
		Type:        cmdType,
	}
	for k, v := range params {
		switch key := k; key {
		case "x":
			cmd.X = toInt32(v)
		case "y":
			cmd.Y = toInt32(v)
		case "x2":
			cmd.X2 = toInt32(v)
		case "y2":
			cmd.Y2 = toInt32(v)
		case "duration_ms":
			cmd.DurationMs = toInt32(v)
		case "target_state":
			cmd.TargetState = fmt.Sprint(v)
		case "strategy":
			cmd.Strategy = fmt.Sprint(v)
		}
	}
	return c.eventBus.PublishRaw(bus.SubjectCommand, cmd)
}

func toInt32(v interface{}) int32 {
	switch n := v.(type) {
	case int:
		return int32(n)
	case int32:
		return n
	case int64:
		return int32(n)
	case float64:
		return int32(n)
	default:
		return 0
	}
}
