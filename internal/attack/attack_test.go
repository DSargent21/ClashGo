package attack

import (
	"image"
	"testing"
)

func TestUnitIdentification(t *testing.T) {
	e := &Executor{}

	tests := []struct {
		name    string
		isHero  bool
		isSiege bool
		isSpell bool
	}{
		{"Barbarian King", true, false, false},
		{"Archer Queen", true, false, false},
		{"Grand Warden", true, false, false},
		{"Minion Prince", true, false, false},
		{"Dragon Duke", true, false, false},
		{"Stone Slammer", false, true, false},
		{"Battle Blimp", false, true, false},
		{"Wall Wrecker", false, true, false},
		{"Log Launcher", false, true, false},
		{"Siege Barracks", false, true, false},
		{"Rage Spell", false, false, true},
		{"Freeze Spell", false, false, true},
		{"Balloon", false, false, false},
		{"Electro Dragon", false, false, false},
	}

	for _, tt := range tests {
		if res := e.isHero(tt.name); res != tt.isHero {
			t.Errorf("isHero(%s) = %v, want %v", tt.name, res, tt.isHero)
		}
		if res := e.isSiege(tt.name); res != tt.isSiege {
			t.Errorf("isSiege(%s) = %v, want %v", tt.name, res, tt.isSiege)
		}
		if res := e.isSpell(tt.name); res != tt.isSpell {
			t.Errorf("isSpell(%s) = %v, want %v", tt.name, res, tt.isSpell)
		}
	}
}

func TestSiegeTappedCache(t *testing.T) {
	e := &Executor{
		tappedSiegeXs: make(map[int]bool),
	}
	w := 1000

	e.tappedSiegeXs[500] = true

	// Exact match
	if !e.isSiegeTapped(500, w) {
		t.Error("expected 500 to be tapped")
	}

	// Close match (within 6% of width = 60px)
	if !e.isSiegeTapped(540, w) {
		t.Error("expected 540 to be tapped (proximity)")
	}

	// Far match
	if e.isSiegeTapped(600, w) {
		t.Error("expected 600 to NOT be tapped")
	}
}

func TestCalculateInwardOffset(t *testing.T) {
	// Simulate the logic in deployUnit for line offset
	w, h := 1000, 1000
	centerX, centerY := w/2, h/2

	// TopRight Edge (outer corner)
	p1 := image.Pt(900, 100)
	p2 := image.Pt(800, 200)

	off := 30
	pct := float64(off) / 200.0 // 0.15 push

	np1 := image.Pt(int(float64(p1.X)+float64(centerX-p1.X)*pct), int(float64(p1.Y)+float64(centerY-p1.Y)*pct))
	np2 := image.Pt(int(float64(p2.X)+float64(centerX-p2.X)*pct), int(float64(p2.Y)+float64(centerY-p2.Y)*pct))

	// Original p1 distance to center: X: 400, Y: 400
	// 15% of 400 = 60.
	// Expected np1: X: 900 - 60 = 840, Y: 100 + 60 = 160

	if np1.X != 840 || np1.Y != 160 {
		t.Errorf("np1 = %v, want (840, 160)", np1)
	}

	// Original p2 distance to center: X: 300, Y: 300
	// 15% of 300 = 45.
	// Expected np2: X: 800 - 45 = 755, Y: 200 + 45 = 245

	if np2.X != 755 || np2.Y != 245 {
		t.Errorf("np2 = %v, want (755, 245)", np2)
	}
}

func TestPrecisionConfigSpellTargets(t *testing.T) {
	cfg := PrecisionConfig{
		SpellTargets: map[string]image.Point{
			"BottomLeft": image.Pt(100, 200),
		},
	}
	if pt, ok := cfg.SpellTargets["BottomLeft"]; !ok || pt.X != 100 || pt.Y != 200 {
		t.Errorf("expected BottomLeft spell target (100, 200), got %v", pt)
	}
}
