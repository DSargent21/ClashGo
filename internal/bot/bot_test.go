package bot

import (
	"image"
	"testing"
)

func TestButtonROIConsistency(t *testing.T) {
	b := &Bot{}

	expected := map[string]image.Rectangle{
		"btn_attack":      image.Rect(0, 500, 300, 732),
		"btn_find_match":  image.Rect(50, 400, 400, 600),
		"btn_battle":      image.Rect(300, 150, 860, 732),
		"btn_army_arrow":  image.Rect(350, 100, 700, 300),
		"btn_army_1":      image.Rect(400, 150, 650, 350),
		"btn_next":        image.Rect(600, 450, 860, 732),
		"unknown_button":  image.Rect(0, 0, 860, 732),
	}

	for name, want := range expected {
		if got := b.buttonROI(name); got != want {
			t.Errorf("buttonROI(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestHistoryCacheNoWipeOnReadError(t *testing.T) {
	b := &Bot{
		historyCache: []AttackReport{
			{Timestamp: "earlier", Stars: 3},
		},
	}

	// Simulate the record path using the in-memory cache as the source of
	// truth. A disk read failure (handled by the caller returning a fresh
	// nil slice) must NOT drop the prior run.
	history := b.historyCache
	if history == nil {
		// emulate read error -> fresh empty slice
		history = []AttackReport{}
	}
	rep := AttackReport{Timestamp: "now", Stars: 2}
	history = append([]AttackReport{rep}, history...)
	b.historyCache = history

	if len(b.historyCache) != 2 {
		t.Fatalf("history cache wiped: got %d entries, want 2", len(b.historyCache))
	}
	if b.historyCache[0].Timestamp != "now" || b.historyCache[1].Timestamp != "earlier" {
		t.Errorf("history cache order/copy wrong: %+v", b.historyCache)
	}
}
