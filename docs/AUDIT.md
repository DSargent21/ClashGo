# ClashGO codebase audit and improvement brainstorm

Date: 2026-11-18. This is an analysis-only rewrite of the previous audit. No
feature, fix or refactor described below is implemented here.

Scope read this turn: the root Go package (`app.go`, `app_launch.go`, `cli.go`),
`internal/{attack,adb,bot,config,game,vision,updater,paths,notify}`, `web/`
(`src/App.tsx`, `src/components/ConfigView.tsx`, `package.json`),
`tools/verify.sh`, and the test files cited below. Live-device paths, actual gameplay, the packaged GUI and
browser rendering were not exercised. No coverage percentage or performance
benchmark is claimed here; the test durations in the verification section are
`go test` output, not a benchmark.

Line numbers are from the working tree at audit time. The worktree is dirty
from earlier sessions, so re-derive a line before editing if code has moved.

## Ranked summary

| Rank | Axis | Proposal | Effort | Blast radius |
| ---: | --- | --- | --- | --- |
| 1 | Performance | Share prepared digit templates across recognizers | M | Medium: Mat ownership |
| 2 | Correctness | Make the visual corpus a real, non-skipping gate | S/M | Low: test data only |
| 3 | Reliability | Time-bound the Vision OCR helper | S | Low: OCR callers |
| 4 | Reliability | Surface malformed-config fallback instead of only logging | M | Medium: startup/config API |
| 5 | Performance | Stop launching the OCR helper per call | M | Medium: helper protocol |
| 6 | Reliability | Stop discarding deployment-watch errors | S | Low: guard loop |
| 7 | Reliability | Report template files that fail to load | S | Low: startup diagnostics |
| 8 | Performance | Replace the 2 s three-call IPC poll with pushed state | M | Medium: Wails event wiring |
| 9 | New feature | Persist a per-raid evidence JSON beside the result frame | S | Low: artifact writer |
| 10 | New feature | Add config schema version + ordered migrations | M | Medium: config format |
| 11 | Performance | Append attack history instead of rewriting 500 entries | M | Medium: history file |
| 12 | Correctness | Remove sleep-dependent test assertions | S | Low: tests only |
| 13 | New feature | Notify on health transition, not only failed recovery | S/M | Low: notification policy |
| 14 | Correctness | Retire or harden the debug-only `TestZZVerifyPlacement` | S | Low: test only |
| 15 | Correctness | Add a frontend test runner for ConfigView/App wiring | M | Low: web dev deps |

---

## Reliability

### R1 (rank 4) — A malformed `config.json` silently becomes defaults

**What is wrong today.** `Load` returns a parse error, but `LoadOrDefault`
logs at warn level and returns `DefaultConfig()`; the GUI's `GetConfig` is
built on that function and the React init path only prints the resulting error
to the console. The user can therefore run a different strategy/army slot than
the file appears to contain, with no visible banner.

**Evidence.**
`internal/config/config.go:630-632`:
```go
if err := json.Unmarshal(data, &cfg); err != nil {
    return nil, fmt.Errorf("parse config: %w", err)
}
```
`internal/config/config.go:701-711`:
```go
cfg, err := Load(path)
if err != nil {
    ...
    log.Warn()....Msg("config unreadable; running on defaults and leaving the existing file untouched")
    return DefaultConfig()
}
```
The same file documents the live consequence at `internal/config/config.go:450-454`:
a non-parsing file dropped `strategy_slots`, so `resolveArmySlot` used the
strategy YAML's stale slot and armed the wrong recipe. The root package's
`app.go:773-775` exposes that defaulting result as `GetConfig`, and
`web/src/App.tsx:425-427` only does `console.error('Init failed:', err)`.

**Proposed change.** Keep `LoadOrDefault` for code that genuinely needs a
fallback, but add an error-returning variant (for example
`LoadOrDefaultWithError`) used by `App` and the launch path. Surface the error
as a persistent UI banner/health field, and write a one-time `config.invalid`
sidecar copy before the next save. Do not silently overwrite the bad file.

**Benefit.** A typo or partially written config becomes a visible, recoverable
state instead of an unexplained wrong army/strategy. The comment's incident is
exactly the failure mode this removes.

**Effort.** M — one new loader, call-site changes in the root package, and a UI
surface.

**Risk.** Configuration loading is on every startup and many tests. Adding a
new API is safer than changing `LoadOrDefault` semantics; the UI banner must
not block the app from starting in fallback mode.

### R2 (rank 3) — The OCR helper has no timeout or cancellation

**What is wrong today.** `OCRTextRegions` runs the compiled Swift helper with
`exec.Command(...).Output()` and no context. A hung Vision helper, stuck
compilation, or a bad image leaves the calling goroutine blocked indefinitely;
`OCRHelperBinary` also compiles with `CombinedOutput()` without a deadline.
The callers include the builder-menu check and the personal-break detector, so
a hang stalls normal bot progress.

**Evidence.** `internal/vision/ocr.go:53-68`:
```go
out, err := exec.Command(bin, imagePath).Output()
if err != nil { ... }
```
and `internal/vision/ocr.go:121`:
```go
out, err := exec.Command("swiftc", "-O", src, "-o", bin).CombinedOutput()
```
Call sites read this turn: `internal/bot/builder_manager.go:1060` and
`internal/bot/personal_break.go:65`.

**Proposed change.** Use `exec.CommandContext` with a bounded timeout
(configurable, with a conservative default), kill the child on deadline, and
return a typed timeout error. Add the same bound to the compile step. Callers
should log once and fall back rather than retry immediately.

**Benefit.** A broken OCR installation degrades to the existing coordinate
fallback instead of hanging a bot goroutine.

**Effort.** S — context/timeout plumbing in one package and a fake-helper test.

**Risk.** A too-short timeout turns slow first-time compilation into a false
failure. Compile and recognize need separate budgets; tests should cover the
timeout path with a fake executable.

### R3 (rank 6) — Deployment watch discards capture and classification errors

**What is wrong today.** The deployment guard's polling goroutine treats a
capture failure as `continue` and explicitly throws away classification error:
`state, _ := e.classify(frame)`. A camera/ADB failure during a battle therefore
produces no rate-limited warning, no failure counter, and no distinction from
"screen is still the same"; the guard just keeps polling.

**Evidence.** `internal/attack/deployment_guard.go:60-83`:
```go
frame, err := e.client.CaptureToMat()
if err != nil {
    continue
}
...
state, _ := e.classify(frame)
guard.observe(state)
```
The goroutine does stop its ticker correctly (`defer ticker.Stop()` at
`:62`), so this is an observability/correctness gap, not a leak.

**Proposed change.** Count capture/classify failures, log a rate-limited
warning with the last error, and expose the counters through the existing
health/stats snapshot. Do not end a battle merely because classification
failed; only surface the degraded evidence.

**Benefit.** The next "battle-end never fired" report has a cause instead of
requiring log archaeology; also distinguishes a dead device from a quiet
screen.

**Effort.** S — counters plus rate-limited logging in the existing guard.

**Risk.** Logging every poll would flood; rate-limit by count or interval. Any
automatic abort would risk ending a valid battle, so keep that out of the
first change.

### R4 (rank 7) — Unreadable template PNGs disappear without a report

**What is wrong today.** `LoadTemplates` silently returns `nil` for any PNG
whose `IMRead` result is empty. The two main callers ignore `LoadTemplates`'
return value entirely, then only log the final count. A corrupt `digit_7.png`
therefore makes a digit unrecognizable with no explicit warning, and the
classifier/loot reader fall back to weaker heuristics.

**Evidence.** `internal/game/templates.go:194-197`:
```go
mat := gocv.IMRead(path, gocv.IMReadColor)
if mat.Empty() {
    return nil
}
```
`internal/bot/bot.go:514-516` calls `templates.LoadTemplates()` without
checking its error. `internal/attack/attack.go:2059-2061` likewise calls
`tStore.LoadTemplates()` without checking it.

**Proposed change.** Return or accumulate a load report (loaded, skipped with
path/reason), and have both startup paths log skipped files. Consider failing
startup only when a required class of templates (for example all ten digits) is
absent, while retaining the empty-store fallback.

**Benefit.** Asset corruption becomes visible immediately and can be tied to
specific read failures; no more "templates loaded: 0" with no explanation.

**Effort.** S — return/aggregate skip information and log it at startup.

**Risk.** Template directories legitimately contain optional states. Logging
skips is safe; hard failure must be scoped to required names.

---

## Performance and efficiency

### P1 (rank 1) — Every battle and attack re-clones and canonicalizes ten digit templates

**What is wrong today.** `NewLootRecognizer` allocates ten template Mats and
ten canonical Mats, thresholds each template, finds its tight box, clones it and
resizes it. That construction happens per battle-wait recognizer and per result
settle; the attack orchestrator also constructs a separate `TroopCounter` that
loads and canonicalizes the same digit files from disk again.

**Evidence.** `internal/game/loot.go:69-118` constructs and calls
`prepareDigitTemplates`; the loop copies/thresholds/clones every digit.
`internal/attack/attack.go:2062` builds a recognizer for the battle wait.
`internal/bot/bot.go:2122` builds one for the search/attack path, and
`internal/bot/bot.go:2428` builds another for the result-settle loop.
`internal/attack/orchestrator.go:580` builds `NewTroopCounter`, whose
`loadDigitTemplates` independently reads `templates/digits` and calls
`vision.CanonicalGlyph` at `internal/attack/troop_counter.go:312-364`.

**Proposed change.** Introduce one immutable prepared digit set owned by the
bot (or a `TemplateStore`-owned cache), shared by `LootRecognizer`,
`TroopCounter`, and the battle wait. Invalidate it only after a template
reload. Keep per-recognizer result-layout caching separate, since that file may
legitimately be edited between battles.

**Benefit.** Removes repeated disk reads, threshold/clone/resize work and
duplicate native allocations from the hottest recurring path: every attack,
battle-wait start and result settle. It also gives one implementation of digit
preparation instead of two subtly different ones.

**Effort.** M — shared prepared set, ownership/lifetime rules, and race tests.

**Risk.** OpenCV Mats are reference-counted and manually closed. A shared cache
must have a clear owner and must never be closed by a short-lived recognizer;
tests should run the full attack/result paths under `-race` and close the
cache exactly once.

### P2 (rank 5) — Builder and personal-break OCR pay a process launch and disk write per call

**What is wrong today.** Each OCR check writes a full PNG to `os.TempDir`,
launches the Swift helper binary, parses stdout, then deletes the file. The
builder menu and personal-break detector call this repeatedly. Even though the
helper is compiled once, process startup, image encode and decode, and temp-file
I/O are paid per check.

**Evidence.** `internal/bot/builder_manager.go:1053-1060` writes
`builder_menu_<nanos>.png` and calls `vision.OCRTextRegions`; the helper itself
executes a new process at `internal/vision/ocr.go:58`. The personal-break path
does the same at `internal/bot/personal_break.go:59-65`.

**Proposed change.** Give the helper a long-lived stdin/stdout protocol (one
process, length-prefixed image or path requests with responses), or expose a
batch API that OCRs several frames in one launch. Keep the one-shot path as the
compatibility fallback. Reuse a single temp file per caller if the protocol
stays file-based.

**Benefit.** Removes process-spawn and repeated PNG traffic from menu checking
and break detection; reduces latency and disk churn without changing the
recognition algorithm.

**Effort.** M — helper protocol plus restart/fallback handling on both sides.

**Risk.** A persistent child can die, leak, or interleave requests. The first
step should be a serialized request/response with an explicit restart on EOF
and the existing fallback when the helper is unavailable.

### P3 (rank 11) — Each attack serializes and rewrites the entire 500-entry history

**What is wrong today.** `recordAttackReport` prepends the new report, caps the
slice at 500, then `json.MarshalIndent`s the whole slice and queues a write of
`attack_history.json` after every attack. The same function also writes
`last_attack_report.json`. Cost grows with history size even though only one
record changed.

**Evidence.** `internal/bot/bot.go:2000-2018`:
```go
history = append([]AttackReport{rep}, history...)
if len(history) > 500 { history = history[:500] }
...
histBytes, err := json.MarshalIndent(history, "", "  ")
if err == nil {
    _ = AsyncWriteFile(paths.ResolveConfig("attack_history.json"), histBytes, 0644)
}
```
The report schema is `internal/bot/bot.go:3991-4008`.

**Proposed change.** Store attacks in an append-only JSONL file with a small
in-memory ring for the UI, and compact periodically (or on startup) to bound
the file. Keep the existing JSON reader as a migration path for current
installs.

**Benefit.** Per-attack work becomes one small append instead of a full
500-record marshal and file replacement; also reduces the chance of losing the
whole history to a partial rewrite.

**Effort.** M — append writer, compaction, and migration for existing readers.

**Risk.** Readers (`GetAttackHistory`, history tests, tooling) currently expect
one JSON array. A migration and dual-read period are required; malformed tail
lines should be ignored/recovered rather than discarding the file.

### P4 (rank 8) — The UI polls three IPC calls every two seconds while events already exist

**What is wrong today.** The React app runs `GetStats`, `GetAttackHistory`, and
`GetLogs` every 2 s while visible, and separately receives pushed
`updater_status` events. Each call crosses the Wails boundary, returns fresh
objects, and then relies on client-side deep equality to avoid re-rendering.
The backend already has an event emitter and a status push loop.

**Evidence.** `web/src/App.tsx:450-473`:
```ts
const [s, h, l] = await Promise.all([GetStats(), GetAttackHistory(), GetLogs()]);
...
const interval = setInterval(fetchData, 2000);
```
`web/src/App.tsx:461-466` documents that the IPC boundary returns new objects
every poll, so reference checks cannot work. `app.go:224-247` already runs a
`forwardUpdaterStatus` loop that emits `updater_status` events; `app.go:86-90`
provides the emit helper.

**Proposed change.** Push a compact stats snapshot and an attack-history
revision (or the new report) from `OnStatsUpdate`; push log lines incrementally
or on demand. Keep a slow fallback poll for reconnects/visibility recovery, not
a three-call poll every 2 s.

**Benefit.** Fewer IPC round-trips and JSON allocations, less React work, and
less duplicate state synchronization. The existing payload-equality code can
remain as a safety net.

**Effort.** M — backend event publishing plus revision/resync handling in React.

**Risk.** Event ordering and missed events during window reload need a
revision/sequence number and a full resync on mount. Log pushes can be chatty;
cap batch size and coalesce.

---

## Correctness and test quality

### C1 (rank 2) — The visual corpus is gitignored, so `verify.sh` can pass on skips

**What is wrong today.** The corpus that backs classifier, HUD, troop-count and
result tests lives under `internal/game/testdata/corpus/*.png` and is matched
by the repository's blanket `**/*.png` ignore. Tests call `t.Skip` when a frame
is absent, and `verify.sh` has no skip budget. A clean clone can therefore run
a green gate while the most important vision assertions never executed.

**Evidence.** `.gitignore:113` contains `**/*.png`;
`git check-ignore -v internal/game/testdata/corpus/battle_mid_720p.png` reports
that exact rule. `internal/game/loot_test.go:400-403` skips when the frame is
missing:
```go
img := gocv.IMRead("testdata/corpus/battle_mid_720p.png", ...)
if img.Empty() {
    t.Skip("no battle_mid_720p.png")
}
```
There are 49 `t.Skip` call sites under `internal/` at audit time.
`tools/verify.sh:79-80` runs plain `go test ./...` and `go test -race ./...`
with no requirement that the corpus be present.

**Proposed change.** Commit a small, reviewed fixture set (or deterministic
derivatives) with provenance and expected outputs, and make the gate fail when
a required fixture is missing. If live captures cannot be committed, keep them
opt-in but add a separate `verify-corpus.sh` that fails on absence and run it
in the release checklist.

**Benefit.** The gate would actually cover the geometry/OCR code it claims to
cover, instead of being silently weaker on a fresh checkout. It also makes
future regressions reproducible.

**Effort.** S/M — curate/redact fixtures; wiring them into the gate is small.

**Risk.** Test images add repository weight and may contain account names or
other identifiers. Review/crop/redact them first; do not commit raw live
captures without user approval.

### C2 (rank 12) — Some tests still depend on elapsed wall time

**What is wrong today.** The async-writer test asserts "nothing landed after
Close" by sleeping 50 ms, and the CPU sampler tests busy-loop/sleep then assert
a range. These are not the worst offenders, but they make the suite slower and
sensitive to scheduler load. The live builder tests also sleep between swipes,
though they are behind the `livemenu` build tag and are not part of
`verify.sh`.

**Evidence.** `internal/bot/asyncwriter_test.go:146-155`:
```go
time.Sleep(50 * time.Millisecond)
mu.Lock()
if len(written) != countAfterClose { ... }
```
`internal/bot/cputime_test.go:14-21` burns a 50 ms loop; `:38` sleeps 20 ms
before reading `Usage()`. `internal/bot/builder_scroll_live_test.go:1` is
`//go:build livemenu` and `:57` sleeps 300 ms after a swipe.

**Proposed change.** Expose a completion/close signal from `AsyncWriter` (or a
test hook) and assert that the writer's worker has exited, rather than waiting
for a quiet period. For CPU sampling, inject a clock/sample function and test
the arithmetic with fixed samples; keep one tagged integration test for the
real platform call.

**Benefit.** Deterministic assertions and faster tests; removes "passed on this
machine" behavior. The tagged live scroll test can stay as a manual check, but
its sleeps should not be presented as gate coverage.

**Effort.** S — test seams/clock injection; no production behavior change.

**Risk.** Test-only changes; the async writer's real ordering guarantees still
need an integration test after the hook is added.

### C3 (rank 14) — `TestZZVerifyPlacement` logs numbers but never asserts them

**What is wrong today.** The test named as a verification gate loads one or two
frames, logs every slot count, and has no expected values or failure condition.
If the first frame is absent it calls `t.Skip`, and it is named `Test...` so the
full verification run includes it. A green run therefore says nothing about
placement quality.

**Evidence.** `internal/attack/zz_verify_test.go:14-61` builds frames, calls
`tc.DetectCounts`, then only `t.Logf`s the counts at `:56-59`. Missing frames
skip at `:43-44`.

**Proposed change.** Either move it under `cmd/` as a probe (matching tools such
as `cmd/countprobe`), gate it behind a build tag, or give it recorded fixtures
and expected counts with a real assertion. If it is kept as a test, a missing
required fixture must fail rather than skip.

**Benefit.** The verification suite stops counting a diagnostic print as
coverage; placement regressions can be detected instead of inspected manually.

**Effort.** S — move/tag the probe or add a fixture-backed assertion.

**Risk.** Low; if expected counts are not yet trustworthy, the probe route is
better than inventing thresholds.

### C4 (rank 15) — The React UI has no test runner; wiring is guarded only by source string checks

**What is wrong today.** `web/package.json` has `dev`, `build`, and `preview`
scripts but no test script. The only frontend regression guards are Go tests
that read `ConfigView.tsx`/`App.tsx` as text (for example the F.2 wiring guard),
which catch a deleted identifier but not a broken controlled input, a label
mismatch, or a save that sends stale state. Configuration init errors in
`App.tsx` are only logged to the console.

**Evidence.** `web/package.json:6-9`:
```json
"scripts": {
  "dev": "vite",
  "build": "tsc && vite build",
  "preview": "vite preview"
}
```
`web/src/App.tsx:425-427` catches init failure with `console.error` only.

**Proposed change.** Add Vitest + React Testing Library, with a first test that
renders `ConfigView` and covers: value display, invalid-target save disabled
state, save callback payload, and load of persisted targets. Add a second test
that `App` surfaces a `GetConfig`/init failure in the UI instead of only the
console.

**Benefit.** Catches UI regressions that `tsc` and source scans cannot see,
including the exact class of silent config failure described in R1.

**Effort.** M — add Vitest/RTL and the first component/loading tests.

**Risk.** New dev dependencies and a small test harness; keep it out of the Go
gate unless the project explicitly wants one combined command.

---

## New features

### F1 (rank 9) — Persist a per-raid evidence JSON next to the result screenshot

**What is missing today.** `saveResultFrame` writes only
`last_battle_result.png`; the parsed result and the settle diagnostics (attempt
count, fallback, destruction percentage, frame path) are not written together.
`AttackReport` has the final numbers and is saved separately, so a bad read
requires correlating timestamps and reading logs.

**Evidence.** `internal/bot/result_settle.go:166-172` writes the PNG and logs
the path. The report fields live at `internal/bot/bot.go:3991-4008`, and
`recordAttackReport` writes `last_attack_report.json` at
`internal/bot/bot.go:2000-2003`. The result loop tracks attempts and fallback at
`internal/bot/bot.go:2420-2497`.

**Proposed change.** On settle completion, write
`last_battle_result.json` with the accepted `BattleResult`, whether it came
from strict settle or fallback, attempts, destruction percentage, timestamp,
and the frame filename/hash. Keep it best-effort like the PNG.

**Benefit.** A post-mortem has the pixels and the numbers in one place; future
F.2/result-read changes can be compared against real evidence without a device.

**Effort.** S — reuse the settle result and write one JSON artifact.

**Risk.** Low; it is a new artifact. Do not include account identifiers, and
bound retention like diagnostics.

### F2 (rank 10) — Add an explicit config schema version and ordered migrations

**What is missing today.** `BotConfig` has no schema version. Migrations are
ad hoc predicates: `IsLegacyPetOrder` recognizes one old default, and the
launch/app paths call it independently. Adding a second migration means adding
another scattered special case, and there is no record of which transforms have
already run.

**Evidence.** `internal/config/config.go:473-488` starts `DefaultConfig` with
device fields and no version field. `internal/config/config.go:426-439` defines
`IsLegacyPetOrder`; `app_launch.go:147-154` and `app.go:1097-1099` both apply
that migration.

**Proposed change.** Add `schema_version` (default 1) and a small ordered
migration chain in `config.Load`, moving the pet-order migration into it. A
migration should be idempotent and only write when a change is made. Expose the
version in diagnostics/logs.

**Benefit.** Future config changes become testable, ordered and auditable;
users upgrading across several releases do not depend on every call site
remembering each historical special case.

**Effort.** M — schema field, migration chain, backups and version fixtures.

**Risk.** A bug in migration can silently rewrite user settings. Add fixtures
for each prior version, keep a backup of the pre-migration file, and fail
closed if a future version is newer than the binary.

### F3 (rank 13) — Notify on a health transition, not only after the recovery ladder fails

**What is missing today.** `SystemHealth.IsHealthy()` exists, and the stuck
watchdog logs/recovers at ten consecutive capture failures. The only
out-of-machine notification in that path is sent after transport, adb-server
recovery and a BlueStacks relaunch have all failed. A farm that is down for
minutes before that point is silent.

**Evidence.** `internal/game/types.go:213-215` defines
`IsHealthy() { return h.ADBConnected && h.ConsecutiveFails < 3 }`.
`internal/bot/bot.go:949-956` starts recovery at `ConsecutiveFails >= 10`.
`internal/bot/notify.go:45-50` is the notifier helper, and the only
`notifyEvent("halt", ...)` call read in this path is
`internal/bot/bot.go:1192-1194`, after recovery has failed.

**Proposed change.** Evaluate `Health().IsHealthy()` on the existing stats
cadence and send one notification on the healthy→unhealthy transition and one
on recovery, with a cooldown and an explicit config opt-in. Reuse the existing
notifier rather than adding a transport.

**Benefit.** An operator learns about a dead ADB/emulator early, before the
slow recovery ladder finishes; the recovery notification remains the final
escalation.

**Effort.** S/M — health-state counter, cooldown and one notifier call site.

**Risk.** Notification storms on flaky networks. Make it opt-in, debounce state
transitions, and suppress while the scheduler is intentionally paused.

---

## Blocked on user input

These are not actionable from repository code alone. They are listed
separately so they are not mistaken for backlog items an agent can close.

### F.1 — labelled outside-full/core frames and collector templates

Missing artifact: a hand-labelled set of recorded frames for bases whose
resource storage is outside the full/core layout, plus collector templates for
those layouts, with the expected classification for each frame. The repository
does not contain those labels/templates; without them a detector change would
be guesswork and could not be validated against ground truth.

### T.1 — an `adb.Client` capture seam

Missing artifact: an injectable capture/frame source for the automation path.
`Executor`/`TapExecutor` currently hold a concrete `*adb.Client` (with tap
hooks but no capture source), so a replay test cannot feed recorded frames
through the real attack pipeline without an emulator. The user needs to decide
and provide the seam/interface (or approve an API change) before T.1 work can
be implemented and tested offline.

### F.2 — live-raid proof of early end

Missing artifact: a recorded live raid that reaches the configured loot target,
shows the End Battle button after the target is crossed, and demonstrates the
bot ending the raid early. The code and fake-clock tests prove the decision
path, but no live-raid recording or account session exists in the repository,
so the live behavior remains unverified. This is evidence the user must capture;
it is not a code change.

---

## What this audit did not cover

- No live ADB/BlueStacks session, no battle was fought, no account was touched.
- No GUI window or browser rendering was exercised; frontend findings come from
  source and `npx tsc` only.
- No coverage run and no benchmark was executed this turn, so no coverage or
  timing numbers are stated.
- Only files cited above were read for a proposal; files not read were not
  audited deeply, even if they exist in the tree.
- The worktree already contained F.2 and other uncommitted changes at audit
  start; their authorship/history was not re-litigated here.

## Verification of this document's turn

Run after the document was written; these are the actual results, not a
restatement from an earlier session.

Focused F.2 command (OpenCV rpath omitted here for readability):

```
go test -race -count=1 -v -run 'TestReadBattleLootHUD|TestLootTargetReached|TestWaitForBattleEnd|TestUI_LootTarget' ./internal/game ./internal/attack .
```

Actual output (summary):

```
--- PASS: TestReadBattleLootHUD_CorpusFrame (0.04s)
--- PASS: TestReadBattleLootHUD_ResizedTo1024x768 (0.04s)
ok  github.com/Ducky705/ClashGO/internal/game 1.516s
--- PASS: TestLootTargetReached_Semantics (0.00s)
--- PASS: TestLootTargetReached_ThresholdMetOnEntry (0.00s)
--- PASS: TestWaitForBattleEnd_LootTargetFakeClock (1.07s)
    --- PASS: .../below_target,_button_hidden:_keeps_waiting (0.17s)
    --- PASS: .../target_reached,_button_hidden:_keeps_waiting (0.16s)
    --- PASS: .../target_reached,_button_visible:_ends_once (0.29s)
    --- PASS: .../HUD_runs_backwards_after_baseline:_gain_clamps_to_zero (0.15s)
    --- PASS: .../feature_disabled:_never_consults_the_HUD_or_ends (0.15s)
    --- PASS: .../HUD_unreadable:_no_end,_no_panic (0.15s)
--- PASS: TestWaitForBattleEnd_NoTicks (0.01s)
ok  github.com/Ducky705/ClashGO/internal/attack 2.514s
--- PASS: TestUI_LootTargetSettingsRoundTripAndValidation (0.01s)
--- PASS: TestUI_LootTargetControlsAreWired (0.00s)
ok  github.com/Ducky705/ClashGO 1.270s
```

Repository gate: `./tools/verify.sh` exited 0 and ended with
`VERIFY PASSED (full)`; no `^--- FAIL` or `^FAIL` line appeared.

Final rerun of the same focused filter (without `-v`) after the last document
edit: `FOCUSED_EXIT=0`; all three package summaries were `ok`. The same turn's
final `./tools/verify.sh` gave `VERIFY_EXIT=0` and `VERIFY PASSED (full)`.

Documentation changes only: `docs/AUDIT.md` rewritten and one pointer entry
added to `PROGRESS.md`. No production, test or frontend code was edited for
this audit.
