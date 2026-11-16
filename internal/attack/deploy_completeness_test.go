package attack

import (
	"image"
	"testing"

	"github.com/Ducky705/ClashGO/pkg/formula"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

// The Amount:"All" line floor is for a pass with no measurement. When the card
// WAS measured, the floor is a fabricated count: live 2026-11-15 an 8-Balloon
// card measured at 8 was turned into 22 taps by the floor, costing ~14 dead taps
// per troop card. The measured count (9 after the safety pad) must drive the
// pass.
func TestDeployTroops_MeasuredAllDoesNotUseLineFloor(t *testing.T) {
	exec, probe := newTapProbe(t)
	hm := NewHeroManager(exec, &SlotManager{}, PrecisionConfig{}, "TopLeft", 1024, 768,
		nil, nil, DeployLine{}, zerolog.Nop())
	slot := &TrackedSlot{TroopSlot: TroopSlot{X: 153, Y: 705, Category: "Troop"}, UnitName: "balloon"}

	hm.pinnedGround = true
	hm.deployLine = lineFromPoints(image.Pt(461, 130), image.Pt(198, 305), "TopLeft", linePoints)

	// detectedTrusted=true, detectedCount=8 is the orchestrator's settled
	// multi-frame read. 9 taps arrive as 3+3+3: three batch calls. The old floor
	// fired 22 taps as eight calls.
	hm.DeployTroops(strategy.Unit{Name: "Balloon", Amount: "All"}, slot, "Line", 0, "Line",
		gocv.NewMat(), 8, true)

	if got := probe.tapCalls(); got != 3 {
		t.Fatalf("tap calls = %d, want 3 (9 measured taps as 3+3+3); the Amount:\"All\" floor must not override a measured count", got)
	}
}

// Verifier retries must fire on the ground the battle deployed on: the pin when
// one was used, else the formula's per-unit ground, else the battle line. The
// corner edge is only the last resort, because the deploy path may never have
// used it.
func TestVerifier_RetryGroundFollowsTheBattle(t *testing.T) {
	f := &formula.Formula{Units: map[string]formula.UnitEntry{
		"balloon": {
			Type: "line",
			P1:   &formula.Point{X: 10, Y: 20},
			P2:   &formula.Point{X: 30, Y: 40},
		},
	}}
	v := &Verifier{
		pCfg: PrecisionConfig{
			Width:  860,
			Height: 732,
			Edges: map[string]ManualEdge{
				"TopLeft": {P1: image.Pt(900, 100), P2: image.Pt(800, 200)},
			},
		},
		targetEdge: "TopLeft",
		w:          860,
		h:          732,
		formula:    f,
	}
	slot := &TrackedSlot{UnitName: "balloon"}

	// No pin, no deploy line: the formula's own ground wins over the corner edge.
	p1, p2, ok := v.retryLine(slot)
	if !ok || p1 != image.Pt(10, 20) || p2 != image.Pt(30, 40) {
		t.Fatalf("retryLine = (%v, %v, %v), want the formula line (10,20)->(30,40)", p1, p2, ok)
	}

	// A pinned battle outranks the formula, exactly like the deploy path.
	v.SetGround(f, DeployLine{Points: []image.Point{{X: 100, Y: 200}, {X: 300, Y: 400}}}, true)
	p1, p2, ok = v.retryLine(slot)
	if !ok || p1 != image.Pt(100, 200) || p2 != image.Pt(300, 400) {
		t.Fatalf("pinned retryLine = (%v, %v, %v), want the pinned deploy line", p1, p2, ok)
	}

	// With neither pin nor formula, the battle's deploy line still beats the
	// corner edge.
	v.SetGround(nil, DeployLine{Points: []image.Point{{X: 50, Y: 60}, {X: 70, Y: 80}}}, false)
	p1, p2, ok = v.retryLine(slot)
	if !ok || p1 != image.Pt(50, 60) || p2 != image.Pt(70, 80) {
		t.Fatalf("deploy-line retryLine = (%v, %v, %v), want the battle deploy line", p1, p2, ok)
	}

	// Last resort: the corner edge, unchanged for configs with no battle ground.
	v.SetGround(nil, DeployLine{}, false)
	p1, p2, ok = v.retryLine(slot)
	if !ok || p1 != image.Pt(900, 100) || p2 != image.Pt(800, 200) {
		t.Fatalf("fallback retryLine = (%v, %v, %v), want the corner edge", p1, p2, ok)
	}
}

// A machine the game proved it never accepted must release its tile and its
// placement count. Leaving either behind makes every later hero drop step around
// an empty spot (live 2026-11-16: two hero taps were nudged off their chosen
// ground by a Stone Slammer that never left its card) and the summary claim a
// machine that never entered the battle.
func TestForgetSiegeGround_ReleasesStaleTileAndCount(t *testing.T) {
	te := &TapExecutor{}
	te.siegeGround, te.siegeGroundSet = image.Pt(197, 435), true
	te.siegePlacements = 1

	te.ForgetSiegeGround()

	if _, ok := te.SiegeGround(); ok {
		t.Fatal("a proven-unplaced machine's tile must not stay reserved")
	}
	if got := te.SiegePlacements(); got != 0 {
		t.Fatalf("siege placements = %d, want 0: nothing entered the field", got)
	}
	te.ForgetSiegeGround() // idempotent
	if got := te.SiegePlacements(); got != 0 {
		t.Fatalf("siege placements after a second forget = %d, want 0 (never negative)", got)
	}
}

// A refused siege must retry the ground it has NOT tried. The outward walk used
// to be appended behind four deploy-line points and then dropped by the
// three-tap ceiling, so every retry re-aimed at the same refused pocket. Live
// 2026-11-16 BottomLeft: the pin (262,483) and all three line points were
// refused, while heroes landed ~30 px outward from the pin.
func TestSiegeRefusedGrounds_LeadsOutward(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 144, Y: 394}, {X: 359, Y: 558}}}
	hm := NewHeroManager(nil, &SlotManager{}, PrecisionConfig{}, "BottomLeft", 1024, 768,
		nil, nil, line, zerolog.Nop())

	pin := image.Pt(262, 483)
	cands := hm.siegeRefusedGrounds(pin)
	if len(cands) < 3 {
		t.Fatalf("candidates = %d, want at least three alternatives", len(cands))
	}
	for i, p := range cands[:3] {
		if p == pin {
			t.Fatalf("retry %d re-aims at the refused point %v", i+1, p)
		}
		if p.Y >= int(float64(hm.h)*0.85) || p.X < 8 || p.Y < 8 || p.Y < BandTop() {
			t.Fatalf("retry %v is outside the deployable band", p)
		}
	}
}

// Run 11's refusal: the pin sat 16 px above the troop line, and the outward
// walk produced points up-left of it, two of them beyond the line and the last
// at y=94 behind the top HUD. The retries must lead with ground on the troop
// line near the pin and never leave the deployable band.
func TestSiegeRefusedGrounds_LeadsWithTroopLineAndStaysInBand(t *testing.T) {
	line := DeployLine{Points: []image.Point{{X: 461, Y: 130}, {X: 198, Y: 305}}}
	hm := NewHeroManager(nil, &SlotManager{}, PrecisionConfig{}, "TopLeft", 1024, 768,
		nil, nil, line, zerolog.Nop())

	pin := image.Pt(321, 207)
	cands := hm.siegeRefusedGrounds(pin)
	if len(cands) < 3 {
		t.Fatalf("candidates = %d, want three", len(cands))
	}
	for i, p := range cands {
		if p.Y < BandTop() {
			t.Fatalf("retry %d %v sits behind the top HUD (band top %d)", i+1, p, BandTop())
		}
		if p.Y > int(float64(hm.h)*0.85) || p.X < 8 || p.X >= hm.w-8 {
			t.Fatalf("retry %d %v is outside the deployable band", i+1, p)
		}
	}
	if _, d, ok := nearestDeployLinePoint(cands[0], line); !ok || d > 3 {
		t.Fatalf("first retry %v is not on the troop line (distance %.1f); the line is the ground troops just used", cands[0], d)
	}
	if dist(cands[0], pin) > 100 {
		t.Fatalf("first retry %v is %.0f px from the refused pin; should be beside it", cands[0], dist(cands[0], pin))
	}
}

// A refused machine must be failed honestly: not marked deployed, and its slot
// released so nothing treats it as a live machine.
func TestMarkSiegeFailed_SlotIsFailedNotDeployed(t *testing.T) {
	slot := &TrackedSlot{TroopSlot: TroopSlot{Category: "Siege"}, UnitName: "stone slammer", SiegePlaced: true, State: SlotDeployed}
	sm := &SlotManager{
		slots:     []*TrackedSlot{slot},
		unitIndex: map[string]*TrackedSlot{"stone slammer": slot},
		logger:    zerolog.Nop(),
	}
	hm := NewHeroManager(nil, sm, PrecisionConfig{}, "BottomLeft", 1024, 768, nil, nil, DeployLine{}, zerolog.Nop())

	hm.markSiegeFailed(strategy.Unit{Name: "Stone Slammer"}, slot)

	if slot.State != SlotFailed {
		t.Fatalf("slot state = %v, want failed", slot.State)
	}
	if slot.SiegePlaced {
		t.Fatal("a failed machine must not stay on the one-placement contract")
	}
}

// A refused siege retry is the SAME machine. Counting it as a placement made a
// normal one-machine battle report "siege_placements: 3", which is treated as
// evidence of a double placement.
func TestSiegeRetry_CountsAsRetryNotAnotherMachine(t *testing.T) {
	exec, probe := newTapProbe(t)
	slot := siegeSlot("Stone Slammer", 360)

	if !exec.PlaceSiege(slot, image.Pt(900, 300), 12.0) {
		t.Fatal("first placement must fire")
	}
	slot.SiegePlaced = false // retryRefusedSiege releases the tap after a proven refusal
	if !exec.RetrySiege(slot, image.Pt(920, 320), 12.0) {
		t.Fatal("retry must fire")
	}

	if got := exec.SiegePlacements(); got != 1 {
		t.Fatalf("siege placements = %d, want 1 (a retry is the same machine)", got)
	}
	if got := exec.SiegeRetries(); got != 1 {
		t.Fatalf("siege retries = %d, want 1", got)
	}
	if got := len(probe.fields()); got != 2 {
		t.Fatalf("field taps = %d, want 2 (placement + retry)", got)
	}
}
