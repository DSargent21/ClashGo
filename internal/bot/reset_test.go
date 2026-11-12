package bot

import (
	"testing"
	"time"
)

func TestResetStatsClearsCountersAndUptime(t *testing.T) {
	b := &Bot{startedAt: time.Now().Add(-time.Minute)}
	b.attackCount.Store(9)
	b.skipsCount.Store(4)
	b.totalGold.Store(1000)
	b.totalElixir.Store(2000)
	b.totalDE.Store(30)
	b.totalStars.Store(12)
	b.stars0.Store(1)
	b.stars1.Store(2)
	b.stars2.Store(3)
	b.stars3.Store(4)

	b.ResetStats()

	if b.attackCount.Load() != 0 || b.skipsCount.Load() != 0 ||
		b.totalGold.Load() != 0 || b.totalElixir.Load() != 0 ||
		b.totalDE.Load() != 0 || b.totalStars.Load() != 0 ||
		b.stars0.Load() != 0 || b.stars1.Load() != 0 ||
		b.stars2.Load() != 0 || b.stars3.Load() != 0 {
		t.Fatal("ResetStats left a session counter non-zero")
	}
	if b.uptime() > time.Second {
		t.Fatalf("ResetStats left uptime at %s", b.uptime())
	}
}
