# Build Plan

Objective: keep improving ClashGO in small, independently verifiable
increments — reliability, unattended farming, bot intelligence, test coverage —
without needing one long agent session.

Workflow and stop conditions live in `AGENTS.md`. Current state lives in
`PROGRESS.md`. Architectural constraints live in `DECISIONS.md`.

## Rules for this file

- One task = one agent session (roughly one focused commit).
- Every task has acceptance criteria a test can prove offline.
- Tick `[x]` only after `./tools/verify.sh` passed and the session was logged in
  `PROGRESS.md`.
- New work goes at the bottom of the active phase, not scattered.
- If a task turns out bigger than a session, split it and keep the first half.

---

## Phase 0 — Harness (done)

- [x] **H.1 Persistent state + verification gate**
  - Files: `AGENTS.md`, `BUILD_PLAN.md`, `PROGRESS.md`, `DECISIONS.md`,
    `tools/verify.sh`, `Makefile`, `.agents/skills/*`
  - Acceptance: `make verify` runs vet + tests + race + build; a fresh session
    can find the next task without chat history.
  - Verified: `make verify` on 2026-11-16 (see `PROGRESS.md`).

- [x] **H.2 Offline testing story documented**
  - Files: `.agents/skills/testing/SKILL.md`
  - Acceptance: agent can name how each subsystem is tested without a device.

---

## Phase 1 — Reliable farming (active)

- [ ] **F.1 Dead-base / collector detection**
  - Why: full outside collectors are the cheapest loot; the bot currently
    judges loot from the HUD counters only.
  - Touch: `internal/game/loot.go` (+ zone helpers), attack search decision in
    `internal/bot`.
  - Acceptance: classifier over recorded frames returns an outside-full/core
    verdict per base; unknown frames return `Unknown`; existing loot tests
    unchanged; one table-driven fixture test per verdict.
  - Verify: `go test ./internal/game -run 'Loot|Zone'`

- [ ] **F.2 Early surrender on loot target**
  - Why: end a raid once the target resource threshold is already banked.
  - Touch: `internal/attack` battle-end logic, `internal/config` (threshold +
    enable flag, default off), stats/history reason.
  - Acceptance: fake battle clock + loot readings end the attack exactly once
    at the threshold; disabled config never triggers; reason recorded in
    history; test covers threshold already met on entry.
  - Verify: `go test ./internal/attack -run 'BattleEnd|Surrender'`

- [ ] **F.3 Hero recovery wait**
  - Why: attacking without designated heroes wastes army and loot.
  - Touch: `internal/bot` idle/halt gate, `internal/config` hero wait policy,
    `BotStats.halted_reason`.
  - Acceptance: with wait enabled and a hero regenerating, no attack starts and
    the halt reason names the hero wait; with heroes ready the loop proceeds;
    watchdog does not treat the wait as a hang; fake clock covers the boundary.
  - Verify: `go test ./internal/bot -run 'Hero|Halt|Idle'`

- [ ] **F.4 Classifier regression fixtures from bug reports**
  - Why: recovered frames are the only way to stop regressions the emulator
    cannot replay.
  - Touch: `internal/game/testdata`, classifier tests, `cmd/capture_template`
    only if a frame needs cropping.
  - Acceptance: every fixture has an expected state; unknown fixtures are
    labelled; suite fails on a deliberately changed classifier threshold.
  - Verify: `go test ./internal/game -run Classifier`

---

## Phase 2 — Test depth (active, parallel-safe)

- [ ] **T.1 Attack pipeline fake-device test**
  - Why: deploy/spell/hero ordering is only covered in units; the orchestrated
    path needs one offline end-to-end check.
  - Touch: `internal/attack/testharness_test.go`, orchestrator test.
  - Acceptance: fake capture + taps drive a full planned attack; assertion on
    ordered tap list; no sleeps; passes under `-race`.
  - Verify: `go test -race ./internal/attack -run Orchestrator`

- [ ] **T.2 Pacing persistence round-trip**
  - Why: restarts must not reset hourly/daily counters.
  - Touch: `internal/bot` session state, `internal/paths` (temp dir in test).
  - Acceptance: save → reload keeps counters and timestamps; corrupt file falls
    back to safe defaults with a logged warning.
  - Verify: `go test ./internal/bot -run 'Session|Pacing'`

- [x] **T.3 UI control contract + live browser sweep**
  - Why: a control whose binding is renamed, whose value is dropped on save,
    or whose config field nothing consumes is a dead button in the packaged
    app, and `tsc` cannot see any of it.
  - Touch: `app_ui_bindings_test.go`, `web/public/wails-stub.js`,
    `web/src/components/ConfigView.tsx`, `app.go` (pet queue).
  - Fixed: removing every pet in Config > Lab & pets now persists an empty
    queue (the `len>0` guard resurrected the old one); the two wall-reserve
    number inputs were removed because the blind-tap wall loop cannot read
    absolute loot and never honoured them; the browser IPC stub was missing
    `SaveUpgradeSetup`/`SaveLaunchSettings`/account CRUD and answered every
    save from static data, so Start in the wizard threw and toggles looked
    like they reverted.
  - Acceptance: every UI-imported binding exists on `*App` and in the
    generated `App.d.ts`; config saves survive the full four-binding save
    order; pet queue round-trips empty; every switch/segmented control in the
    browser dev build flips and re-reads its stored value.
  - Verified: `go test -run TestUI_ .` PASS; `npx tsc --noEmit` PASS;
    `./tools/verify.sh` -> see `PROGRESS.md`; browser sweep on
    `http://127.0.0.1:5199` (wizard, Config 5 tabs, Settings, Army, Accounts,
    Analytics, Dashboard) with no console errors.

- [x] **T.4 UI control -> engine effect proof**
  - Why: T.3 proved the bindings exist and the stub round-trips, not that a
    click changes what the bot does. The packaged app talks to Go over Wails
    IPC, so every control needs a consequence asserted where the loop reads it.
  - Touch: `app_ui_engine_test.go` (new), `internal/bot/live_config.go` (new),
    `app.go` (boot/start/stop seams, reserve args gone), `internal/bot/bot.go`
    (nil-client/nil-cancel guards for headless stats), `web/` (reserve state
    and 20-arg binding removed).
  - Fixed: the two wall-reserve arguments were still passed by the UI and
    written by the binding although nothing read them; the whole plumbing is
    gone now and `config.json` values survive untouched. `Bot.Stats` panicked
    for a bot without a device, which also broke `GetAccounts` in any
    device-less context.
  - Acceptance: save/search, storage-halt predicate, schedule gate, builder /
    lab / pet queue, army slot + auto-end, auto-loot apply/reset, account
    CRUD/switch/reset and start/stop each assert a real consequence (live
    `b.cfg`, scheduler gate decision, builder-manager filters, config.json the
    engine reloads); a static guard fails if any binding writes a
    `cfg.Section.Field` that no engine package reads.
  - Verified: `go test -run 'TestUI_|TestEngine_' .` PASS;
    `npx tsc --noEmit` PASS; `./tools/verify.sh` -> VERIFY PASSED (full).

- [ ] **T.5 Config validation contract**
  - Why: bad YAML must fail loudly, not silently disable farming.
  - Touch: `internal/config`.
  - Acceptance: table of invalid values (negative caps, unknown sort modes,
    inverted schedules) rejected with named errors; defaults pass.
  - Verify: `go test ./internal/config`

---

## Phase 3 — Backlog (plan before starting)

Items not yet decomposed; promote one into Phase 1/2 with criteria before work:

- Magic item spending (needs calibrated UI + spend ceiling).
- Multi-instance supervisor.
- Remote notifications for milestones/session summaries.
- Windows port.
- Community strategy import/export.

Re-plan when the active phases are empty. See `docs/ROADMAP.md` for product
context.
