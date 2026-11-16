# Progress

Single source of truth for "where did the last session stop". A fresh session
reads this first, then `BUILD_PLAN.md`. Never rely on chat history.

## Now

- Objective: reliable unattended farming + deeper offline tests.
- Next task: **F.1 Dead-base / collector detection** (`BUILD_PLAN.md`).
- Last green commit: see `git log -1`.
- Verify gate: `./tools/verify.sh` (full) / `./tools/verify.sh --fast`.

## Blockers

- None.

## Session log

Append one entry per session, newest at the bottom. Keep it short and factual.

Format:

```
### YYYY-MM-DD — <task id>: <title>
- Changed: <files/packages>
- Verified: <command> -> PASS|FAIL (<note>)
- Commit: <short sha or "none">
- Next: <task id or blocker>
```

### 2026-11-16 — H.1: persistent harness + verification gate
- Changed: `AGENTS.md`, `BUILD_PLAN.md`, `PROGRESS.md`, `DECISIONS.md`,
  `tools/verify.sh`, `Makefile`, `.agents/skills/*`
- Verified: `./tools/verify.sh` -> see baseline below
- Commit: none (worktree change)
- Next: F.1 dead-base detection

### Baseline at harness setup (2026-11-16)
- `go vet ./...` -> PASS
- `go test ./...` (with OpenCV rpath) -> PASS
- `go test -race ./...` (with OpenCV rpath) -> PASS
- `go build ./...` -> PASS

### 2026-11-16 — attack-polish: troop, hero and siege placement
- Changed: `internal/attack/{executor,hero_ground,hero_manager,verifier,orchestrator}.go`,
  `internal/attacklog/attacklog.go`, and the attack/attacklog tests.
- Fixed:
  - Measured card counts no longer get the `Amount:"All"` tap floor (a 10-unit
    card was spending 22 taps); a measured pass that cannot read its card hands
    the slot to the sweep instead of assuming deployment.
  - Hero wave: first-selection arm 200 ms (the sweep's proven timing), at most
    ONE rescue (one re-arm + one new ground), confirmation reads through the
    card animation (late beat 2.5 s) and is never aborted by the deploy budget.
    No repeated card taps.
  - Verifier re-aims a refused unit on the battle's own grounds (pin -> formula
    -> deploy line -> corner) and reports every unit still on the bar.
  - Siege retries are the same machine (`siege_retries`, not placements). A
    machine the game proved it never accepted is now failed honestly instead of
    `markDone`, and its stale tile is released so hero drops no longer step
    around an empty spot.
- Verified: `go test ./internal/attack ./internal/attacklog` PASS;
  `./tools/verify.sh` -> `VERIFY PASSED (full)`; live raids
  `logs/live_polish_run1..7.log` (run8 blocked: army empty).
- Live evidence: run6 siege placed after one refused ground; run7 both heroes
  took exactly one re-arm + one field retry, both abilities fired; every run
  spent 11 taps on a 10-detected troop card and swept nothing extra.
- Known issue: on some bases (TopRight pin) the Stone Slammer's card stays armed
  through all four grounds and the machine is reported failed. It is no longer
  retried forever or counted as placed; the ground-acceptance cause is open.
- Commit: this commit
- Next: F.1 dead-base detection; siege ground acceptance on TopRight.

### 2026-11-16 — attack-polish: siege ground fix + composition coverage
- Changed: `internal/attack/{hero_manager,deploy_completeness_test,siege_single_tap_test,spell_deployer_test}.go`,
  new `internal/attack/deploy_composition_test.go`.
- Siege root cause: the refusal is regional. The pin is a hero entry point and
  can sit inside the deployment line's red band, and `siegeRefusedGrounds`
  appended the outward walk AFTER four deploy-line points and then truncated the
  list to three — so the only untried ground was always cut off, and every retry
  re-aimed inside the pocket. The walk now leads (45/95/160 px away from the
  battlefield centre), with the troop line only as fallback. Live run 9 landed
  the Stone Slammer on its first tap at BottomLeft, `siege_placements=1`,
  `siege_retries=0`.
- Composition: table-driven `DeployTroops` tests cover a 4-unit small camp, a
  12-unit single-type card, 10-unit full mixed cards, and a partial live badge
  (3 of 10) — taps follow the measured count (+1 single-digit pad only), the
  card must drain before the slot is marked deployed. A refused-ground case
  proves the slot stays `SlotAttempted` and the verifier line parses as FAILED.
- Test flake fixed: two spell line tests used a 12 px tolerance inside the
  deployer's own 10 px/axis jitter envelope (max 14.1 px); widened to 15 px.
- Verified: `go test ./internal/attack ./internal/attacklog` PASS;
  `./tools/verify.sh` -> `VERIFY PASSED (full)`.
- Live: `logs/live_polish_run9.log` — 3 stars, all troops left their cards
  (sweep found none), siege first-tap, heroes each one re-arm then sweep
  placement; `last_attack_report.json` `undeployed_slots=0`.
  `cmd/attack_report` honestly keeps Balloon/Electro Dragon/Royal Champion as
  UNVERIFIED (card animation / trusting-tap sweep), not FAILED.
- Open: hero-phase first tap still refuses on siege-adjacent ground and the
  ability is skipped when the rescue also fails; sweep recovers the placement.
- Next: verifier positive-confirmation line so UNVERIFIED cards can close as
  deployed without over-claiming; hero ability when the rescue is refused.

### 2026-11-16 — attack-polish: positive confirmation closes the report
- Changed: `internal/attack/verifier.go`,
  `internal/attack/verifier_failed_slot_test.go`, `internal/attacklog/attacklog.go`,
  `internal/attacklog/attacklog_test.go`.
- Defect: run 9's sweep proved zero troop cards left and the battle was 3 stars,
  but `checkRemainingSlots` closed an empty card with `IsEmpty=true` and no log
  or state change, so the report kept Balloon/ED/RC as UNVERIFIED.
- Fix: an empty card on a settled post-deploy frame now logs
  `verifier: slot visibly empty; deployment confirmed`, marks that slot deployed,
  and the parser maps the line to `deployed`. A slot nobody observed gets
  `verifier: positive confirmation impossible; no settled post-deploy frame
  showed the card empty or drained` and stays UNVERIFIED with that evidence
  named. A unit still on the bar is unchanged: FAILED, run flagged.
- Verified: `go test ./internal/attack ./internal/attacklog` PASS;
  `./tools/verify.sh` -> `VERIFY PASSED (full)`.
- Live run 10 (`logs/live_polish_run10.log`): 1 star, `undeployed_slots=0`,
  per-unit report `deployed=7 not in bar=4`, zero UNVERIFIED; both heroes one
  re-arm + one confirmed retry, both abilities; siege first tap,
  `siege_placements=1`, `siege_retries=0`; verifier confirmed ED and Balloon.
- Next: F.1 dead-base detection.

### 2026-11-16 — attack-polish: run 11's refused pin and the silent siege failure
- Changed: `internal/attack/hero_manager.go`, `internal/attack/verifier.go`,
  `internal/attack/deploy_completeness_test.go`,
  `internal/attack/verifier_failed_slot_test.go`,
  `internal/attack/hero_pin_ground_test.go`,
  `internal/attacklog/attacklog_test.go`.
- Live run 11 (`logs/live_hero_firsttap_run11.log`, full 350/352 army): the
  pinned ground (321,207) was refused for the siege on all three attempts and
  then refused for BOTH heroes; each hero landed on its first retry on the troop
  line (292,242). The siege never left its card (`siege_placements=0`,
  `siege_retries=3`), yet the verifier announced `all units successfully
  deployed`, the report omitted the machine entirely, and
  `last_attack_report.json` said `deploy_success: true`, `undeployed_slots: 0`.
- Siege ground fix: retries now lead with the troop line within ~85 px of the
  refused point (ground troops just used), and every candidate is clamped to
  the deployable band. Run 11's outward walk had produced y=94, behind the top
  HUD, and spent the three-tap ceiling before ever trying the line.
- Hero first tap: a ground the siege proved refused is no longer used as a
  hero's lead. `groundRefusedBySiege` / `noteSiegeRefused` make the hero phase
  lead with the nearest troop-line point instead; the pin keeps its lead
  everywhere else. Run 11 was the exact case: both first taps hit the pin the
  machine had just failed on.
- Report honesty: `checkRemainingSlots` now counts `Siege` slots, and
  `retryDeploy` refuses to fire more field taps at a machine still on its card.
  A refused machine is logged as `unit still on the bar at battle end` and the
  report marks it FAILED, which flags the run.
- Verified: `go test ./internal/attack ./internal/attacklog` PASS;
  `go run ./cmd/attack_report -log tmp/siege_fail.log` prints
  `Stone Slammer FAILED` and exits 1.
- Live gap: run 11 spent the army, so the new ground and honesty fixes are
  gate/test-proven only; no fresh battle yet. Run 11's own report was produced
  before the verifier change and still shows the old false success.

### 2026-11-16 — attack-polish: run 12 disproves the ground-only theory for hero first tap
- Changed: `internal/attack/hero_manager.go`, `internal/attack/executor.go`,
  `internal/attack/hero_first_tap_test.go` (new),
  `internal/attack/hero_ground_test.go`, `internal/attack/hero_retry_test.go`,
  `internal/attack/deploy_completeness_test.go`, `internal/attacklog/*`,
  `internal/attack/verifier.go` (siege naming).
- Hero first tap now always leads with troop-line ground:
  `heroLineGrounds` returns up to three clamped line points near the authored
  intent (pin/formula), capped one slot short so the authored point survives as
  the next alternative. Y is clamped to `BandTop..UICutoff` after jitter too.
  A measured red zone removes line points it covers (run 12's stale edge).
- Run 12 (`logs/live_hero_firsttap_run12.log`, full army, 1 star) tested it:
  `ground_source=troop_line`, first tap (293,507), **still refused**; both
  heroes' retries on line ground (266,487) landed. That disproves "the authored
  ground was the cause": a line tile was refused on the FIRST selection but
  accepted on the second. Same pattern as runs 10/11 (first selection refused
  at pin, formula and line ground; next selection always landed).
- Fix: `PrimeSlot` sends one extra SELECTION tap (not a placement, not a
  re-arm, spends no budget) before the first field tap, so the first drop uses
  the selection behaviour that has landed every time. Physical card taps become
  prime + placement + ability (tests updated). NOT live-proven: run 12 spent
  the army.
- Siege run 12: all three line-near retries refused (`siege_placements=0`,
  `siege_retries=3`). The verifier now counts the failed machine (JSON
  `deploy_success: false`, `undeployed_slots: 1`) but logged `unit=` blank, so
  `cmd/attack_report` still printed a clean verdict; `markSiegeFailed` now fills
  the fallback slot's `UnitName`, and the test asserts the name.
- Tests: new `TestHeroFirstTap_*` cover run-11 pin, run-10 formula, HUD clamp,
  reduced tap budget and no-acceptable-ground FAILED through
  `deploySingleHero`/`deployHeroBatch`; parser covers the no-ground FAILED line.
- Verified: `go test ./internal/attack ./internal/attacklog` PASS;
  `./tools/verify.sh` -> `VERIFY PASSED (full)`.
- Live gap: army spent by run 12; hero prime and siege naming are gate-proven
  only. Next live raid must confirm both first taps land with no rescue.

### 2026-11-16 — attack-polish: prime selection pinned; blank siege now fails the report
- Added `TestHeroFirstTap_FirstSelectionRefusedAt{Pin,Formula,TroopLine}`. Each
  models the live game: a field tap only lands after TWO card selections. The
  tests deploy through `deploySingleHero`, require exactly one field tap on the
  troop line, `SpotTaps==1`, `SpotRearms==0`.
- Revert proof: removing `hm.executor.PrimeSlot(...)` makes all three fail
  (pin/formula: 2 field taps, line: first tap refused). Restored.
- Parser fix: a `verifier: unit still on the bar at battle end` line with
  `category=Siege unit=` now produces a `Siege Machine` FAILED unit. Test
  `TestParseBlankSiegeOnBarStillFails`; re-running `cmd/attack_report` on run
  12's own log now prints `FAILED=1`, verdict must-not-call-success, exit 1.
- No acceptable ground still reports FAILED through `deployHeroBatch`
  (`TestHeroFirstTap_NoAcceptableGroundReportsFailed`); reduced budget case
  lands its only placement (`TestHeroFirstTap_ReducedTapBudget`).
- Army: run 12 spent the full 350-troop army at 23:03; no raid's worth existed
  afterwards and training would outlast the session. Hero first-tap and siege
  naming rest on gate/unit coverage plus run 12's negative evidence, not a
  fresh battle.
- Verified: `go test ./internal/attack ./internal/attacklog` PASS;
  `./tools/verify.sh` -> `VERIFY PASSED (full)`.
