package bus

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"google.golang.org/protobuf/proto"
)

func startTestNATS(t *testing.T) string {
	t.Helper()
	ns, err := server.NewServer(&server.Options{
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ns.Start()
	if !ns.ReadyForConnections(time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(func() { ns.Shutdown() })
	return ns.ClientURL()
}

func TestEventBusPublishesState(t *testing.T) {
	url := startTestNATS(t)
	ring := NewRingBuffer(time.Second, 10, 100)
	e, err := NewEventBus(url, "test-device", WithNoopOnFail(false), WithRingBuffer(ring), WithRetention(time.Minute))
	if err != nil {
		t.Fatalf("new event bus: %v", err)
	}
	defer e.Close()

	var seen atomic.Bool
	sub, err := e.SubscribeState(func(ev *GameStateEvent) {
		if ev.State == GameState_BATTLE {
			seen.Store(true)
		}
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	if err := e.PublishState(&GameStateEvent{TimestampMs: time.Now().UnixMilli(), State: GameState_BATTLE}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := e.Flush(time.Second); err != nil {
		t.Fatalf("flush: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if seen.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("state event not received")
}

func TestRingBufferReplay(t *testing.T) {
	r := NewRingBuffer(time.Minute, 10, 100)
	now := time.Now()
	r.PushFrame(FrameEntry{Timestamp: now, JPEG: []byte("jpeg"), State: GameState_BATTLE})
	r.PushEvent(now.Add(10*time.Millisecond), Subject("dev", SubjectLoot), mustMarshalLoot(123))

	items := r.Replay(now, now.Add(time.Second))
	if len(items) != 2 {
		t.Fatalf("items=%d", len(items))
	}
	if items[0].Type != "frame" || items[1].Type != "event" {
		t.Fatalf("unexpected timeline: %#v", items)
	}
}

func mustMarshalLoot(gold int32) []byte {
	data, err := proto.Marshal(&LootEvent{Gold: gold})
	if err != nil {
		panic(err)
	}
	return data
}
