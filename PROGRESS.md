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
