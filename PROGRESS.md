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
