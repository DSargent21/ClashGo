package bus

import (
	"testing"
	"time"
)

func BenchmarkRingBuffer_PushAndLatest(b *testing.B) {
	rb := NewRingBuffer(30*time.Second, 60, 4096)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rb.PushFrame(FrameEntry{Timestamp: time.Now(), JPEG: make([]byte, 50000), State: GameState_BATTLE})
		_ = rb.LatestFrame()
	}
}
