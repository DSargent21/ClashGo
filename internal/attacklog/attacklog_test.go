package attacklog

import (
	"strings"
	"testing"
)

// runLog is a trimmed version of a real 1280x720 run, keeping the lines the
// parser keys on and their exact shapes — including the case drift between
// phases (the slot manager logs "balloon", the planner "Balloon", the sweep
// "balloon" again), which is the bug that made the first version of the report
// count one unit as three.
const runLog = `15:52:05 | INF | random edge selected component=attack_executor edge=BottomRight
15:52:05 | WRN | formula points projected outside the deployable band (behind the top HUD or under the troop bar) and were clamped into it component=attack_executor band_bottom=612 band_top=110 points_clamped=3
15:52:05 | INF | formula.json loaded; per-unit explicit coordinates will override edge-based deploy component=attack_executor display_scale=1.325 projection="uniform display scale about the viewport centre"
15:52:06 | INF | detected troop counts component=attack_executor counts={"134":0,"212":0,"287":0}
15:52:06 | WRN | unit not found in bar component=deploy_planner unit="Minion Prince"
15:52:06 | INF | deploying troop component=attack_executor unit=Balloon x=212
15:52:06 | INF | deploying troop (formula-driven live-count) component=hero_manager count=8 detected_count=0 live_count=0 p1={"X":795,"Y":612} p2={"X":1032,"Y":461} src=formula unit=Balloon
15:52:07 | WRN | reconcile exhausted; leaving slot in SlotAttempted for sweep to retry component=hero_manager initial_count=8 reconcile_rounds=3 unit=Balloon
15:52:10 | INF | deploying main hero component=hero_manager conf=0.9386861324310303 unit="Archer Queen" x=433
15:52:10 | INF | hero deployed (slot-selected + single tap sent) component=hero_manager delta=0.2065 slot_x=433 slot_y=682 target={"X":913,"Y":551} unit="Archer Queen"
15:52:10 | WRN | hero slot did not visibly transition to cooldown (capture failed or delta below threshold); deployment may have failed - sweep will retry captured_post=true component=hero_manager unit="Barbarian King"
15:52:11 | INF | tapping spell formula point component=spell_deployer idx=0 pt={"X":736,"Y":532} series="formula line outer" unit="Rage Spell"
15:52:11 | INF | tapping spell formula point component=spell_deployer idx=1 pt={"X":816,"Y":467} series="formula line outer" unit="Rage Spell"
15:52:12 | INF | reconcile: spell slot confirmed empty component=spell_deployer extra_fired=0 rounds=1 unit="Rage Spell"
15:52:12 | INF | tapping spell formula point component=spell_deployer idx=0 pt={"X":724,"Y":431} series="formula line" unit="Ice Spell"
15:52:12 | INF | tapping spell formula point component=spell_deployer idx=1 pt={"X":734,"Y":426} series="formula line" unit="Ice Spell"
15:52:12 | INF | tapping spell formula point component=spell_deployer idx=2 pt={"X":746,"Y":421} series="formula line" unit="Ice Spell"
15:52:16 | INF | sweep deploying component=sweeper count=3 event_troop=false p1={"X":795,"Y":612} p2={"X":1032,"Y":461} unit=balloon x=212
15:52:16 | WRN | sweep reconcile: OCR reads zero for consecutive reconciles after firing; card is spent or locked — marking deployed attempt=2 component=sweeper unit=balloon zero_reads=2
15:52:18 | WRN | hero sweep retry did not visibly transition slot; will be marked failed component=sweeper delta=-0.37 unit="barbarian king" x=584
15:52:22 | INF | all units successfully deployed component=verifier
15:52:22 | INF | evidence capture: saved last_attack_overview.png + last_attack_taps.png component=attack_executor remaining=0 taps_outside_band=2
`

func parseFixture(t *testing.T) *Report {
	t.Helper()
	rep, err := parse(strings.NewReader(runLog), "fixture.log")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return rep
}

func unit(t *testing.T, rep *Report, name string) Unit {
	t.Helper()
	for _, u := range rep.Units {
		if strings.EqualFold(u.Name, name) {
			return u
		}
	}
	t.Fatalf("unit %q not in report (%d units)", name, len(rep.Units))
	return Unit{}
}

func TestParse_SessionFacts(t *testing.T) {
	rep := parseFixture(t)
	if rep.Edge != "BottomRight" {
		t.Errorf("edge = %q, want BottomRight (the log writes it unquoted)", rep.Edge)
	}
	if rep.DisplayK != 1.325 {
		t.Errorf("display scale = %v, want 1.325", rep.DisplayK)
	}
	if !rep.BandKnown || rep.ClampedPts != 3 || rep.ClampedTop != 110 || rep.ClampedBtm != 612 {
		t.Errorf("band = known:%v clamped:%d %d..%d, want true/3/110/612",
			rep.BandKnown, rep.ClampedPts, rep.ClampedTop, rep.ClampedBtm)
	}
	if rep.TotalSlots != 3 || rep.ZeroCountSlots != 3 {
		t.Errorf("counts: %d slots, %d zero, want 3 and 3", rep.TotalSlots, rep.ZeroCountSlots)
	}
	if len(rep.CountFabricated) != 1 || rep.CountFabricated[0] != "Balloon" {
		t.Errorf("fabricated counts = %v, want [Balloon]", rep.CountFabricated)
	}
}

// The report's first version listed "Balloon" and "balloon" as separate units,
// which splits one failure across two rows and understates it.
func TestParse_FoldsUnitNamesCaseInsensitively(t *testing.T) {
	rep := parseFixture(t)
	n := 0
	for _, u := range rep.Units {
		if strings.EqualFold(u.Name, "balloon") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("balloon appears %d times in the report, want 1", n)
	}
}

func TestParse_Verdicts(t *testing.T) {
	rep := parseFixture(t)
	cases := []struct {
		name    string
		verdict Verdict
		why     string
	}{
		// The sweep's write-off is the one that matters: the same check called
		// the slot still non-empty and then believed a zero count.
		{"Balloon", Unverified, "zero troop-count OCR"},
		{"Archer Queen", ConfirmedDeploy, "transitioned"},
		{"Barbarian King", Failed, "marked failed"},
		{"Minion Prince", NotInBar, "bar does not carry"},
		{"Rage Spell", ConfirmedDeploy, "drained"},
		// Five taps, no reconcile line at all — the run simply moved on.
		{"Ice Spell", Unverified, "no completion check"},
	}
	for _, c := range cases {
		u := unit(t, rep, c.name)
		if u.Verdict != c.verdict {
			t.Errorf("%s: verdict = %s, want %s", c.name, u.Verdict, c.verdict)
		}
		if !strings.Contains(u.Reason, c.why) {
			t.Errorf("%s: reason = %q, want it to mention %q", c.name, u.Reason, c.why)
		}
	}
}

func TestParse_SpellOverlap(t *testing.T) {
	rep := parseFixture(t)
	byUnit := map[string]Placement{}
	for _, p := range rep.Placements() {
		byUnit[p.Unit] = p
	}

	ice, ok := byUnit["Ice Spell"]
	if !ok {
		t.Fatal("Ice Spell placement missing")
	}
	if !ice.Overlapped {
		t.Errorf("Ice Spell taps %v are ~11 px apart and must be reported as overlapped", ice.Taps)
	}
	if ice.Stacked != len(ice.Taps) {
		t.Errorf("Ice Spell stacked = %d, want all %d taps", ice.Stacked, len(ice.Taps))
	}

	rage := byUnit["Rage Spell"]
	if rage.MinSpacing < 70 {
		t.Fatalf("rage spacing decoded wrong: %.0f px", rage.MinSpacing)
	}
	if rage.Overlapped {
		t.Errorf("rage taps %.0f px apart are not stacked, and calling that overlapped would make the report noise", rage.MinSpacing)
	}
}

// The property that made the gap: a log can contain "all units successfully
// deployed" and still be full of unestablished deployments.
func TestReport_ProblemContradictsTheAggregateSuccessLine(t *testing.T) {
	rep := parseFixture(t)
	if !strings.Contains(runLog, "all units successfully deployed") {
		t.Fatal("fixture lost the aggregate success line it exists to contradict")
	}
	if !rep.Problem() {
		t.Error("Problem() = false, but two heroes failed and a spell cast was stacked")
	}
	bad := rep.FailedUnits()
	if len(bad) != 3 {
		t.Errorf("failed/unverified units = %d, want 3 (barbarian king failed; balloon and ice spell unverified)", len(bad))
	}
	names := map[string]bool{}
	for _, u := range bad {
		names[u.Name] = true
	}
	for _, want := range []string{"Barbarian King", "Balloon", "Ice Spell"} {
		if !names[want] {
			t.Errorf("%s should be reported as not established; got %v", want, names)
		}
	}
}

func TestReport_CleanRunHasNoProblem(t *testing.T) {
	clean := `15:50:00 | INF | random edge selected edge=BottomLeft
15:50:00 | INF | detected troop counts component=attack_executor counts={"134":8,"212":6}
15:50:01 | INF | deploying main hero component=hero_manager unit="Archer Queen" x=433
15:50:01 | INF | hero deployed (slot-selected + single tap sent) component=hero_manager unit="Archer Queen"
15:50:02 | INF | tapping spell formula point component=spell_deployer idx=0 pt={"X":400,"Y":300} unit="Rage Spell"
15:50:02 | INF | tapping spell formula point component=spell_deployer idx=1 pt={"X":700,"Y":500} unit="Rage Spell"
15:50:03 | INF | reconcile: spell slot confirmed empty component=spell_deployer unit="Rage Spell"
`
	rep, err := parse(strings.NewReader(clean), "clean.log")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rep.Problem() {
		t.Errorf("Problem() = true on a clean run: failures=%v overlapped=%v fabricated=%v",
			rep.FailedUnits(), rep.OverlappedSpells(), rep.CountFabricated)
	}
	if len(rep.FailedUnits()) != 0 {
		t.Errorf("failed/unverified = %v, want none", rep.FailedUnits())
	}
}

// The troop deploy phase's own success line ("reconcile confirmed slot empty;
// deploy complete") must count as a confirmed deploy. Live 720p runs emitted it
// for whole waves of troops and the report still called them UNVERIFIED, which
// made a working deploy look unproven.
func TestParse_DeployPhaseDrainCountsAsConfirmed(t *testing.T) {
	log := `15:50:00 | INF | random edge selected edge=BottomLeft
15:50:00 | INF | detected troop counts component=attack_executor counts={"212":6}
15:50:00 | INF | deploying troop (live-count-driven) component=hero_manager count=6 detected_count=6 p1={"X":200,"Y":400} p2={"X":500,"Y":500} src=formula unit=Balloon
15:50:01 | INF | reconcile confirmed slot empty; deploy complete component=hero_manager rounds=1 unit=Balloon
`
	rep, err := parse(strings.NewReader(log), "drain.log")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	u := unit(t, rep, "Balloon")
	if u.Verdict != ConfirmedDeploy {
		t.Errorf("Balloon verdict = %q (%s), want %q", u.Verdict, u.Reason, ConfirmedDeploy)
	}
}

// The sweep writing a card off on a trusted zero while the visual check still
// saw content is a judgement, not a drain: it must not be reported as a
// confirmed deploy.
func TestParse_SweepZeroWriteOffIsUnverified(t *testing.T) {
	log := `15:50:00 | INF | random edge selected edge=BottomLeft
15:50:00 | INF | sweep deploying component=sweeper count=3 p1={"X":136,"Y":404} p2={"X":446,"Y":554} unit=valkyrie x=62
15:50:01 | WRN | sweep reconcile: card shows no count while the rest of the bar is readable; card is spent or locked — marking deployed attempt=2 component=sweeper unit=valkyrie zero_reads=2
`
	rep, err := parse(strings.NewReader(log), "writeoff.log")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	u := unit(t, rep, "valkyrie")
	if u.Verdict == ConfirmedDeploy {
		t.Errorf("valkyrie verdict = %q (%s); a zero-count write-off is not a confirmed drain", u.Verdict, u.Reason)
	}
	if !strings.Contains(u.Reason, "written off") {
		t.Errorf("valkyrie reason = %q, want the zero-count write-off explained", u.Reason)
	}
}
