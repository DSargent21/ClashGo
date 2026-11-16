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

- [x] **T.5 Config validation + enforced UI binding coverage**
  - Why: bad form values used to be silently kept, clamped, or written where
    the bot would never read them; and a newly added binding could ship with
    no test proving it does anything.
  - Touch: `internal/config/validate.go` (new), `app_config_validation_test.go`
    (new), `app_ui_contract_test.go` (new), `app.go` (validation in every save
    binding + army-slot/strategy guards), `app_test_tools.go` (device seam),
    `app_test.go` (real strategy fixture).
  - Fixed: out-of-range army slot was clamped to 1 while reporting success;
    per-strategy overrides accepted names that are not on disk; empty/bad
    schedule hours, zero/oversized thresholds, empty enums and unknown pets
    were ignored or persisted; negative loot floors and tiny stall timers were
    accepted by SaveConfig; SaveFullConfig wrote arbitrary configs.
  - Acceptance: invalid, empty, out-of-range and contradictory values are
    rejected with a named error while config.json bytes and live `b.cfg`,
    scheduler and builder-manager copies stay unchanged; valid values still
    save; every binding exposed in `App.d.ts` or forwarded by the browser stub
    is mapped to a test that calls it and observes engine/config state, so a
    no-op test or a call-and-ignore-error test is rejected; the Settings device
    buttons run through a device seam with the persisted device/filter config
    and write nothing; updater buttons are proven to refuse without GitHub.
  - Verified: `go test -run 'TestValidation_|TestUI_|TestEngine_' .` PASS;
    coverage guard proven to fail by temporarily adding an uncovered binding
    to `App.d.ts` (failed with the expected message) then restored, and by
    mutating `TestEngine_SearchAndAttackControls` first to a no-op and then to
    a call-and-no-error body (both failed, both restored); fresh invalid cases
    (launch slot 5/-1, wizard save with a duplicated pet, single-digit hour)
    rejected with byte-identical config and live state;
    `npx tsc --noEmit` PASS; `node --check web/public/wails-stub.js` PASS;
    `./tools/verify.sh` -> VERIFY PASSED (full).

- [x] **T.6 UI audit: control ownership and dead-end forms**
  - Why: the same plan settings appeared on Config and Army, wall configuration
    was split across two tabs, and two controls could not be saved at all.
  - Fixed: Army is the only home for active strategy, recipe slot and auto-end;
    walls (switch + resource + reserved builder) live together under Builders
    and disappear from Storage; Storage gained the resume threshold that used
    to be silently hardcoded to 80 on save; session/break jitter are preserved
    on save instead of being reset to 15/5; stall timer 0 now matches the form's
    "0 disables" hint and the engine contract; Settings derives ADB health from
    the same helper as Dashboard (no more "Optimal" before the first capture);
    the setup wizard moved out of Settings → Testing; the one-item Fleet nav
    group was merged into Overview.
  - Acceptance: every plan-level control is rendered exactly once; a Save from
    any Config tab preserves unrelated stored values; stall 0 reaches the live
    engine config; both surfaces label ADB state from one helper; `tsc` and the
    root package tests pass.
  - Verified: see `PROGRESS.md`.

- [x] **T.7 Honest gating + Analytics window labels**
  - Why: Config's Schedule/Builders/Lab/Pets forms were wrapped in a
    pointer-events wall while their master switch was off, so values could not
    be prepared or saved; Analytics mixed session and saved-history numbers
    under labels that did not say which was which.
  - Fixed: every Config field is editable and persisted regardless of its
    master switch (verified at the Go save/reload boundary). The setup wizard's
    upgrade block is the one honestly-disabled form: native disabled controls
    plus a visible "turn it on to edit these" reason, no pointer-events wall.
    Rotation controls are editable while rotation is off; wall resource and
    loot thresholds are always editable; Analytics labels every surface with
    "This session" or "Saved history".
  - Acceptance: no Config form uses `is-disabled`/`pointer-events`/master-switch
    disabled attributes; values set with the feature off survive save + fresh
    app load; every Analytics KPI/chart/table names its window; existing
    aggregations unchanged; all views load against the Wails stub with no
    console errors or React warnings.
  - Verified: see `PROGRESS.md`.

- [x] **T.8 UI control inventory, duplicate sweep, and size matrix**
  - Why: the audit needed a per-view control inventory, a duplicate/label
    adjudication for every repeated action, and proof that the layout holds at
    the minimum, short and tall window sizes.
  - Fixed: Dashboard report rows no longer add a second keyboard focus stop for
    the same open-report action (chevron is the sole AT target; row click stays
    as a pointer shortcut); Schedule max/hour and max/day inputs gained
    accessible names. Added `docs/UI_CONTROL_INVENTORY.md` with all controls,
    locations, actions and deliberate shortcut decisions.
  - Acceptance: every view/tab/overlay inventory recorded; no unexplained
    duplicate or inconsistent label; every control reachable at 1024x768,
    1024x600 and 1024x1440; tsc, guards and full verify green; clean console.
  - Verified: see `PROGRESS.md`.

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
