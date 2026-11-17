# Progress

Single source of truth for "where did the last session stop". A fresh session
reads this first, then `BUILD_PLAN.md`. Never rely on chat history.

## Now

- Objective: reliable unattended farming + deeper offline tests.
- Last task: **T.10 Responsive UI polish + Wails parity** (see the
  2026-11-17 session log entry).
- Next task: **F.1 Dead-base / collector detection** (`BUILD_PLAN.md`).
- Last green commit: see `git log -1`.
- Verify gate: `./tools/verify.sh` (full) / `./tools/verify.sh --fast`.

## Blockers

- None open. The boot-splash variant gap is fixed (rules in `classifier.go`, own
  live-frame test); only a fresh live re-offer of that screen is unproven — see
  the 2026-11-17 entry.

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

### 2026-11-16 — T.3: UI control contract + live browser sweep
- Changed: `app_ui_bindings_test.go` (new), `app.go` (pet queue save),
  `internal/config/config.go` (reserve-field comment), `web/public/wails-stub.js`,
  `web/src/components/ConfigView.tsx`, `BUILD_PLAN.md`.
- Fixed:
  - Config > Lab & pets: removing every pet saved "successfully" and the old
    queue came back on the next read (`len(petPriorities) > 0` guard). The
    queue is now replaced unconditionally, empty included.
  - Config > Storage & walls: "Min gold reserve" / "Min elixir reserve" were
    written to config.json but nothing in `internal/bot` read them — the wall
    loop is blind-tap and has no absolute-loot read to compare against. The
    inputs are gone rather than pretending; the persisted fields stay for
    compatibility (see config comment).
  - Browser IPC stub: missing `SaveUpgradeSetup`/`SaveLaunchSettings`/account
    CRUD made the setup wizard's Start throw, and static `GetConfig` /
    `GetArmyPlans` / `GetLootAutotune` replies made saved toggles look like
    they reverted. Stub is now stateful and mirrors the Go semantics for slot,
    auto-end and auto-loot; account CRUD mutates in place so delete works.
- Added tests: `TestUI_PetQueueCanBeCleared`, `TestUI_ConfigFormSaveOrder`
  (the four-binding Save path), `TestUI_DeviceTestTools` (unknown action +
  stop-the-bot guard, no device), `TestUI_LaunchWizardBindings`,
  `TestUI_BindingsExistForUITests` (every UI import exists on `*App` and in
  `App.d.ts`), `TestUI_ReserveFieldsAreNotRendered`.
- Live browser sweep (vite dev + stub, `127.0.0.1:5199`): setup wizard all
  three steps and every switch/chip; Config all five tabs, every switch,
  category chips, halt tiles, slot buttons, auto-end, auto-loot, pet
  remove/add, Save -> saved; Settings all appearance switches, default page,
  export diagnostics, three device-test buttons, reset-stats two-click arm,
  reset-preferences reload; Army slot + "Use this army"; Accounts add/edit/
  delete/reset-usage/rotation/switch; Analytics series toggles; Dashboard log
  chips, filter, auto-scroll, sidebar collapse, start button. No console
  errors after the stub fix.
- Not verified here: real Wails IPC (browser runs the stub), and a live raid
  (spends army; `make smoke-live*` stays opt-in). Binding contracts for those
  paths are covered offline.
- Verified: `go test -run TestUI_ .` PASS; `npx tsc --noEmit` PASS;
  `./tools/verify.sh` -> VERIFY PASSED (full).
- Next: F.1 dead-base detection.

### 2026-11-16 — T.4: UI control -> engine effect proof
- Changed: `app_ui_engine_test.go` (new), `internal/bot/live_config.go` (new),
  `app.go` (boot/start/stop seams; reserve args removed from
  `SetAutoUpgradeConfig`), `internal/bot/bot.go` (nil guards in `Stats` and
  `Cancel`), `app_test.go`, `app_ui_bindings_test.go`,
  `web/public/wails-stub.js`, `web/src/App.tsx`,
  `web/src/components/ConfigView.tsx`, `web/wailsjs/go/main/App.{d.ts,js}`,
  `BUILD_PLAN.md`.
- Fixed:
  - Wall-reserve numbers were still threaded UI -> binding -> config despite
    nothing reading them. Removed from the UI, the binding signature and the
    stub; the persisted fields stay so old `config.json` files load and keep
    their values (asserted).
  - `Bot.Stats` dereferenced `client`/`cpuSampler`, so `GetAccounts` (which
    reads stats) panicked for a bot without a device. Both are nil-guarded;
    `Cancel` tolerates a nil runtime context the same way.
- Added: `LiveConfig()` (b.cfg, resolved army slot, scheduler copy,
  builder-manager copy) and `NewHeadlessBot` (real Bot + real scheduler +
  real builder manager, no ADB). `TestEngine_*` drives the genuine `*App`
  bindings against a temp config dir and asserts: search floors / strategy /
  stall reach `b.cfg`; storage halt fields change `VillageResources.IsFull`;
  saved schedule changes `Scheduler.CanAttack` (window + hourly cap);
  builder/lab/pet filters and queue order/clear reach the builder manager;
  army slot + auto-end reach live config and the plan list; auto-loot apply
  installs the shown suggestion and reset keeps it; account create/update/
  rename-on-disk/switch/reset-usage; StartBot hands the form thresholds to the
  started bot, duplicate start refused, StopBot tears down. Static guard
  `TestEngine_EveryUIWrittenFieldHasEngineReader` fails when a binding writes
  a field no engine package reads (the reserve class of defect).
- Not verified: real Wails IPC window (macOS Wails is WKWebView-only; no
  display automation), live ADB/game session and Settings device buttons that
  restart ADB/BlueStacks (device-mutating), and a real raid (spends army).
  Start/stop proves the App lifecycle with the device-bound boot seamed out.
- Verified: `go test -run 'TestUI_|TestEngine_' .` PASS; `npx tsc --noEmit`
  PASS; `node --check web/public/wails-stub.js`; `./tools/verify.sh` ->
  VERIFY PASSED (full).
- Next: T.5 config validation contract.

### 2026-11-16 — T.5: config validation + enforced binding coverage
- Changed: `internal/config/validate.go` (new), `app_config_validation_test.go`
  (new), `app_ui_contract_test.go` (new), `app.go` (validation in SaveConfig,
  SaveFullConfig, SetStorageConfig, SetScheduleConfig, SetAutoUpgradeConfig,
  SaveLaunchSettings, SaveUpgradeSetup, SetStrategyArmySlot/AutoEnd, StartBot;
  strategy-reference helpers), `app_test_tools.go` (device seam
  `runDeviceTestMenu`), `app_test.go` (real strategy fixture instead of the
  fake "barcher").
- Fixed (all found by the new validation tests, none worked around):
  - `SetStrategyArmySlot` silently clamped slot 0/5+ to 1 and returned success.
    Now rejects anything outside 1-4 and writes nothing.
  - `SetStrategyArmySlot`/`SetStrategyAutoEnd` accepted any name, creating
    overrides for strategies the bot never loads. Now require a real
    `strategies/*.yaml`.
  - `SetScheduleConfig` treated empty hours and zero durations/caps as "keep
    the old value" while reporting success; invalid `HH:MM` was never parsed.
    Now parses both clocks and rejects empty, malformed, zero, out-of-range and
    per-day-below-per-hour combinations.
  - `SetStorageConfig` ignored `fullThresh <= 0` and accepted any halt rule or
    a resume point at/above the full threshold. Now rejects all of them.
  - `SetAutoUpgradeConfig` accepted empty/unknown strategies, unknown or
    duplicate pets, negative caps and >5 reserved builders. Now rejects.
  - `SaveConfig` accepted negative/oversized floors, stall outside 10-600 and
    a strategy path that does not exist.
  - `SaveFullConfig` wrote any struct; now validates search/attack/storage/
    schedule/upgrade sections first.
  - `StartBot` refused to boot with invalid floors instead of starting a
    session that cannot search.
- Added coverage contract: `TestUI_EveryFrontendBindingHasStateTest` parses
  `web/wailsjs/go/main/App.d.ts` plus the `window.go.main.App` table in the
  dev stub, and requires every binding to map to a test that calls it AND
  reads engine/config state (LiveConfig, GetConfig, diskConfig, ...). Proven
  three ways: appending `TotallyUncoveredBinding` to App.d.ts failed; mutating
  `TestEngine_SearchAndAttackControls` into a no-op body failed ("never calls
  SaveConfig"); mutating it into a body that only calls SaveConfig and checks
  for an error failed ("only calls the binding"). All mutations restored.
- Fresh invalid-input cases outside the original table: `ChooseArmy` slot 5
  and -1, `SaveUpgradeSetup` re-saving a hand-edited duplicate pet queue, and
  a single-digit hour. The last two exposed missing guarantees: the wizard did
  not validate the pet queue it persists (now `ValidatePetQueue` before write),
  and `time.Parse` accepted "7:00" (now HH:MM is required).
- Added device-seam test: `TestUI_DeviceTestMenuRoutesActions` runs open /
  filters / close through the real App with `runDeviceTestMenu` replaced,
  asserts the persisted host/port/display scale/upgrade strategy reach the
  menu runner, and that config.json is byte-identical after.
- Added updater guard test: all update buttons refuse visibly with no update /
  no updater, and status stays readable.
- Added getter contract: GetConfig/GetStats/GetLogs/GetAttackHistory/
  GetStrategies/GetLaunchOptions/GetLootAutotune/GetAppVersion/IsRunning and
  ResetStats reflect live engine state.
- Not verified: real Wails IPC window (macOS WKWebView-only, no display
  automation); live ADB menu taps (device seam proves routing/config only);
  GitHub update download/install (external network + app relaunch), refused
  paths covered; real raid (spends army).
- Verified: `go test -run 'TestValidation_|TestUI_|TestEngine_' .` PASS;
  root package + internal/config PASS; coverage guard failure demonstrated;
  `npx tsc --noEmit` PASS; `node --check web/public/wails-stub.js` PASS;
  `./tools/verify.sh` -> VERIFY PASSED (full).
- Next: T.2 pacing persistence round-trip (remaining unchecked plan task).

### 2026-11-16 — T.6: UI audit, control ownership, dead-end forms
- Changed: `web/src/{App.tsx,utils.ts}`, `web/src/components/{ConfigView,
  ArmyView,SettingsView,Sidebar,Dashboard}.tsx`, `web/src/styles/views.css`,
  `internal/config/validate.go`, `app_config_validation_test.go`,
  `docs/{UI,DESIGN,STRATEGIC_RUSH}.md`, `DECISIONS.md` (D-9), `BUILD_PLAN.md`.
- Fixed:
  - Plan-level controls had two homes. Config > Attack & loot carried a second
    recipe slot, an auto-end switch labelled "at 50%" while the plan carried
    its own percent, and a strategy select that duplicated Army's "Use this
    army". Config now owns engine-wide settings only; Army owns strategy,
    slot and auto-end.
  - Walls were split across Storage & walls (switch + resource) and Builders
    (reserved builder). All three now sit in one Builders block; the Storage
    tab is purely halt/hysteresis/auto-collect.
  - Config's Save hardcoded resume=80, session jitter=15 and break jitter=5,
    silently overwriting stored values; lowering the full threshold below 80
    made Save impossible to satisfy. The resume point is now a validated input
    and the jitters are read from config and preserved.
  - The stall input offered 0 ("disables") while `ValidateAttack` rejected it;
    1–9 also passed the form and failed on save. Validation now accepts 0 or
    10–600 and the form flags 1–9 before submit.
  - Settings reported ADB "Optimal" before any capture and "Healthy" for a
    0 ms placeholder, while Dashboard said "Awaiting". Both surfaces use one
    `adbHealthDisplay` helper; Settings hides latency until a capture exists.
  - Settings > Testing contained "Open setup wizard" (configuration, not a
    device test); moved to Behavior. The one-item Fleet nav group was merged
    into Overview.
- Verified: `npx tsc --noEmit` PASS; `node --check web/public/wails-stub.js`
  PASS; `./tools/verify.sh --fast` -> VERIFY PASSED (fast);
  `./tools/verify.sh` -> VERIFY PASSED (full).
- Not verified: visual click-through of the moved controls (no browser drive
  this session); real Wails IPC window.
- Next: F.1 dead-base detection.

### 2026-11-16 — T.7: honest gating + Analytics window labels
- Changed: `web/src/components/{ConfigView,LaunchSetup,AccountsView,Analytics,
  SettingsView}.tsx`, `web/src/styles/views.css`, `app_ui_bindings_test.go`
  (3 new guards + persistence test), `docs/UI.md`, `DECISIONS.md` (D-10, D-11),
  `BUILD_PLAN.md`.
- Fixed:
  - Config > Schedule/Builders/Lab/Pets used `.is-disabled` (opacity +
    `pointer-events: none`), so fields looked off and swallowed clicks while
    the master switch was off. All four wrappers are gone: fields are editable,
    and Save writes every value; the switch only gates engine use. Proved by
    `TestUI_ConfigSectionsPersistWhileDisabled` (disabled schedule, builders,
    lab strategy/DE flags, pets, wall resource survive save + fresh app load).
  - Search thresholds, wall resource and account-rotation controls had
    `disabled` tied to their master switch; now editable and persisted.
  - Setup wizard's upgrade block was the same pointer-events wall. It now keeps
    its native controls disabled with an explicit "Upgrade engine is off"
    reason and no pointer-events rule; lab order is editable while lab is off.
  - Settings > Setup wizard disabled while the bot runs without a reason; the
    row now says "Stop the bot to replay the guided setup".
  - Analytics mixed windows silently. KPI strip says "session ...", chart,
    outcomes, revenue and strategy table say "This session", activity says
    "Saved history · last 12 months". No aggregation changed.
  - Analytics chart legend refused to hide the last visible series with no
    feedback; every chip toggles now.
- Guards: `TestUI_ConfigSectionsEditableWhenOff` (no Config pointer-events/
  master-switch disabled inputs), `TestUI_AnalyticsStatesWindow` (every window
  label present; old ambiguous copy rejected),
  `TestUI_ConfigSectionsPersistWhileDisabled` (Go save/reload proof).
- Verified: `npx tsc --noEmit` PASS; `./tools/verify.sh --fast` -> VERIFY
  PASSED (fast); `./tools/verify.sh` -> VERIFY PASSED (full).
- Stub sweep (Vite `localhost:5199`, Wails IPC stub): loaded Dashboard, Army,
  Analytics, Accounts, Config (Search & loot, Builders, Lab & pets, Schedule,
  Storage) and Settings; 0 console errors and 0 React warnings. Measured in
  the live DOM: wizard upgrade block `pointer-events: auto`, 11/11 controls
  `disabled`, visible "Upgrade engine is off" note; Schedule off with 0
  disabled form controls; Lab + Pets off while lab order, DE switch and Add-pet
  stay enabled; rotation off while order/attacks/auto-start stay enabled;
  Builders has exactly 1 wall switch/resource, Storage has 0.
- Not verified: real Wails IPC window (stub only, as scoped).
- Next: F.1 dead-base detection.

### 2026-11-16 — H.3: humanization audit + pacing pass
- Changed: `internal/bot/{humanize,humanize_test,idle,bot,scheduler,personal_break}.go`,
  `internal/adb/client.go`, `internal/game/navigator.go`,
  `internal/attack/{executor,sweep,attack}.go`, `internal/config/config.go`,
  `docs/HUMANIZATION.md`, `BUILD_PLAN.md`.
- Audit: documented every timing surface in `docs/HUMANIZATION.md`, with the
  honest limit stated up front — no client-side change prevents a ban; these
  changes only remove behavioural timing signatures.
- Fixed:
  - Deploy cadence was ~24 taps/s: `TapTriple` 55±12 ms and batch gap 60±20 ms.
    Now 85±25 ms intra-batch, 110±35 ms between, 12% longer branch; hero
    abilities 260±70 ms; sweep 95±30 ms. Live trace measured 45-118 ms
    intra-batch and 91-149 ms between batches.
  - Inter-raid gap was exactly `min_seconds_between_attacks` (30 s) every raid.
    Now ±22% with a 30-150 s tail on ~1 raid in 10.
  - Idle pan was exactly every 18 s. Now 22-57 s, with ~1 in 8 waits at 2-5 min.
  - Session/break length was uniform. Now ~1 session in 6 is a short check-in
    and ~1 break in 10 is 1-3 h longer; jitter 0 still returns the configured
    value exactly (scheduler tests unchanged and passing).
  - Graph-navigation taps were `Client.Tap` (0 ms press, 2 px spread). Now
    `TapRandomized` (reaction pause + hold chance); transition waits ±20%.
    Tap jitter default 2 -> 5 px, with a `Load` floor of 4 px for older configs;
    explicit `jitter_taps:false` / `max_jitter_pixels:0` untouched.
  - `personal_break.go` warned "a Personal Break dialog may be on screen" on
    every unclassified frame every 12 s because the `schedule.enabled` check ran
    before the OCR. It now warns only after the phrase and an action button are
    both read from the frame (found on the live run).
- Verified: `go test ./internal/bot -run 'Humanized|IdlePan|Scheduler'` PASS;
  `./tools/verify.sh` -> VERIFY PASSED (full), twice (before and after the
  `personal_break` fix).
- Live: `make smoke-live` with `CLASHGO_TAP_TRACE=tmp/live_humanize_taps.ndjson`
  (`tmp/live_humanize.log`). One raid fired: 11 Balloons + 11 Electro Dragons,
  5 rage casts, both heroes; it reached 64% destruction when the emulator
  dropped its network connection (`connection lost during battle`). The result
  panel and Return Home were NOT exercised; the army was spent, so no second
  attempt. Deploy cadence above is measured from that trace, not inferred.
- Environment left in: MainVillage (recovered by hand after the loading-screen
  restart loop).
- Commit: this commit
- Next: F.1 dead-base detection; or the boot-splash classifier gap above.

### 2026-11-16 — T.8: UI control inventory, duplicate sweep, size matrix
- Changed: `web/src/components/{Dashboard,ConfigView}.tsx`,
  `app_ui_bindings_test.go` (2 new guards), `docs/UI_CONTROL_INVENTORY.md`
  (new), `docs/UI.md`, `DECISIONS.md` (D-12), `BUILD_PLAN.md`.
- Inventory: recorded every rendered action in Dashboard, Army, Analytics,
  Accounts, Config (Search & loot, Builders, Lab & pets, Schedule, Storage),
  Settings, account drawer, update modal and all three wizard steps, including
  conditional empty/error states and the crash fallback. Each row names its
  label, location and action; repeated row patterns are recorded once.
- Fixed:
  - Dashboard raid rows were a second keyboard target for the same open-report
    action (`tabIndex={0}` + Enter/Space) beside the chevron button. The row
    keeps pointer click as a broad hit area; the chevron is now the only
    keyboard/AT stop (`TestUI_DashboardReportHasOneKeyboardTarget`).
  - Schedule max attacks/hour and max attacks/day inputs had no accessible
    name. Both now carry explicit `aria-label`s
    (`TestUI_ScheduleInputsAreNamed`).
- Deliberately kept (all documented with reasons in the inventory): sidebar +
  active-account Start/Stop (global vs contextual, same binding); Settings
  Check now + modal Re-check; topbar update chip + Settings Review update;
  setup wizard preset vs Army/Config (wizard says choices stay editable);
  page-head/empty-state Add account; Refresh accounts + error Retry;
  drawer/modal X/Cancel/Later/scrim dismissals.
- No control was moved on taste. Walls remain only under Builders, per-plan
  strategy/slot/auto-end only on Army, rotation on Accounts, diagnostics and
  device tests on Settings.
- Size matrix: measured in the Wails stub at 1024x768, 1024x600 and 1024x1440.
  All main views and Config tabs: zero page/content horizontal overflow. A
  reachability pass scrolled every rendered button/input/select/textarea/tab/
  switch into view and checked it against its own scroll container: 0 failures
  (Dashboard 9–12, Army 6, Analytics 3, Accounts 15, Config 14/20/32/14/14,
  Settings 14; drawer 15; modal 7; wizard 11/18/10). Drawer, modal and wizard
  bodies scroll under fixed headers/footers at 600px height.
- Verified: `npx tsc --noEmit` PASS; focused Go guards PASS; fresh reload in
  the stub -> only Vite/dev-stub info logs, then all views/tabs exercised with
  console `[]` (0 errors, 0 React warnings). Schedule labels read
  "Max attacks per hour/day"; the report row has `tabIndex=-1` with one
  focusable chevron. `./tools/verify.sh` -> VERIFY PASSED (full).
- Not verified: real Wails IPC window (stub only, as scoped); 480px-wide layout
  is below the enforced 1024px minimum and was not restyled.
- Next: F.1 dead-base detection.

### 2026-11-17 — H.4: splash-first dismissal, live dialog misreads, tap floor
- Changed: `internal/game/{classifier,dismiss,classifier_splash_test,dismiss_test}.go`,
  `internal/bot/bot.go`, `internal/adb/{client,jitter_test}.go`,
  `internal/attack/{executor,attack}.go`, `PROGRESS.md`.
- Fixed:
  - `SplashStateOnFrame` runs before the ChestReward and Unknown branches, so a
    classified splash owns the frame and the Unknown self-heal only presses Back.
  - 1024x768 signatures for the reconnect dialog (`ConnectionLost`, prio 99) and
    the inactivity dialog (`Inactivity`, prio 99). Both used to satisfy the chest
    rule (3/4) and classify `ChestReward 405`, which ran the chest flow's hammer
    taps on a dialog. The inactivity title probe sits on a thick stroke interior
    because the bot's raw screencap renders that text edge darker (179 vs 240)
    than `adb exec-out screencap -p`.
  - Dismissal candidates for both dialogs; `DismissOverlay` taps only the probe
    that verifies on the frame about to be tapped (TRY AGAIN (293,455),
    RELOAD GAME (290,460)).
  - Tap floor: dual/triple settle 90±20 ms with a hard 65 ms floor (the Gaussian
    tail produced 34 ms), and the four-sides sweep, batch gap and redeploy slot
    taps go through `Client.SettleTap`. Pre-fix live trace had 8 gaps < 60 ms;
    post-fix deploy gestures stayed at 65-150 ms.
- Verified: `./tools/verify.sh --fast` -> PASS. Real-frame tests:
  `TestDismissLoadingChestFromTheLiveFrame`,
  `TestConnectionLostLiveFrameBeatsTheChestRule`,
  `TestInactivityLiveFrameBeatsTheChestRule`,
  `TestTapSettleStaysInsideHumanCadence`.
- Live: the inactivity dialog was cleared by the bot's own tap
  (`dismiss: tapping ... state=Inactivity x=290 y=460` -> village detected).
  Acceptance run from the launcher (`tmp/accept_run4.log`, configured cold start,
  one raid): match -> deploy -> 35% / 0 stars -> result parsed -> Return Home ->
  village. Zero manual taps, zero recovery restarts, zero personal-break
  warnings. Trace: 182 taps, deploy gestures 65-150 ms, 177/182 jittered, one
  50 ms pair between concurrent paths (field tap then army-bar tap).
- Not verified: the live game never re-offered the "Tap!" loading table
  (force-stop + relaunch x5, HOME/resume, airplane-mode toggle, TRY AGAIN
  reconnect, launch while idle; every boot went straight to the village).
  Clearance is proven on the recorded live frame
  (`TestDismissLoadingChestFromTheLiveFrame`: TapToContinue 610, tap (512,420)),
  not on a fresh live boot.
- Next: F.1 dead-base detection.

### 2026-11-17 — H.5: process-wide tap floor + cold-boot splash coverage
- Changed: `internal/adb/{client,jitter_test}.go`, `internal/bot/{bot,bot_test}.go`,
  `cmd/tapaudit/{main,main_test}.go`, `PROGRESS.md`.
- Fixed:
  - `HumanTapFloor` (65 ms) is enforced in `routeTap` via `paceTap`, the single
    funnel every tap path uses. Concurrent paths can no longer emit a pair
    closer than the floor; the paced remainder is jittered so collisions do not
    produce a run of exactly-65 ms gaps, and the dual/triple settle resamples
    the sub-65 ms tail instead of clamping it (same 90±20 ms shape).
  - `splashStateForFrame` gives the splash probes first refusal on Unknown,
    Loading and ChestReward frames, so a cold boot whose table reads as the
    generic loading rule still hands the frame to the splash handler.
  - `tapaudit -assert-human` fails a trace whose minimum consecutive gap is
    below 65 ms or whose 8 consecutive gaps have sd <= 5 ms (fixed interval).
- Verified: real-frame cold-boot routing test
  `TestSplashFirstCoversUnknownLoadingAndChest` (Unknown/Loading/ChestReward ->
  TapToContinue, village untouched) plus the existing
  `TestDismissLoadingChestFromTheLiveFrame`; `TestTapPacingFloorSerializesGoroutines`;
  `cmd/tapaudit` tests.
- Live: cold boot from the launcher, zero hand taps, one raid:
  match -> deploy -> 80% / 1 star -> result parsed -> Return Home -> village.
  Loot Gold 874,034 (incl. 320,000 bonus), Elixir 787,214, DE 10,648.
  `tapaudit -assert-human` on the trace: minimum consecutive gap 67 ms >= 65 ms,
  no fixed-interval run. Trace 66 taps, 63 jittered, one boot Back self-heal,
  zero recovery restarts, zero personal-break warnings
  (`tmp/coldboot.log`, `tmp/coldboot_taps.ndjson`).
- Not verified: the live game still never re-offered the "Tap!" loading table
  (cold boot goes straight to the village; same attempts as the H.4 entry).
  The dismissal path is covered by the real-frame tests above instead.
- Next: F.1 dead-base detection.

### 2026-11-17 — H.6: tap-floor review, cadence gate, drag-deploy finding
- Changed: `internal/adb/client.go` (paceTap simplified: one lock, zero-time
  short-circuit), `cmd/tapaudit/main.go` (`-assert-human` is now a clean gate),
  `PROGRESS.md`.
- Review: `splashStateForFrame` stays the seam — a pure function of
  (state, classifier, frame) with a real-frame test for all three inputs;
  nothing to cut. `SettleTap`/`nextTapSettle` stay because the floor only
  paces collisions, while the settle shapes the normal cadence.
- Fixed: `tapaudit -assert-human` no longer fails on the choreography flags that
  deliberate deploy bursts always raise. It exits 0 when the cadence contract
  holds (`human cadence OK: minimum consecutive gap 67 ms >= 65 ms, no
  fixed-interval run`) and 1 on a short gap or a fixed-interval run.
- Tried live, reverted: drag-deploy (card->line swipe, then field strokes) and
  field-only swipe streaming. On this BlueStacks/CoC build a held swipe places
  one unit at release and the card deselects, so both variants left 10 of 11
  on the card and forced 3 top-up rounds of 12 taps; the raid finished at 40%
  instead of the usual 80%. No drag code is left in the tree.
- Measured attack window from the last clean run (`tmp/coldboot.log`):
  Balloons 3.2 s, Electro Dragons 3.6 s (goal 7 s for troops), Siege 3.7 s,
  Spells 1.0 s, Heroes 5.6 s, total 17.6 s. Troop placement is at the 7 s goal;
  the overrun is siege retries and the hero settle, not tap count.
- Verified: `go test ./internal/attack ./internal/adb ./cmd/tapaudit` PASS;
  `tapaudit -trace tmp/coldboot_taps.ndjson -assert-human` -> exit 0.
- Not verified: full cold-boot end-to-end on this pass — the last live run could
  not start a battle (empty army after the drag test; "EDIT ARMY was not located
  on the empty attack panel") and was stopped. The previous cold-boot run
  (`tmp/coldboot.log`) is the last complete evidence: 80% / 1 star, Return Home,
  village, min tap gap 67 ms.
- Next: F.1 dead-base detection.

### 2026-11-17 — H.7: evdev multi-touch primitive, and why it stays off on this build
- Changed: new `internal/adb/multitouch.go` (`TouchPanel` discovery from
  `getevent -pl`, `BuildDragStroke`/`BuildHoldThenDrag`/`BuildWaypointDrag`,
  `Client.DragStroke` streaming one held contact over `/dev/input/event2`),
  `internal/adb/multitouch_test.go`, `internal/adb/client.go` (panel cache),
  `internal/attack/executor.go` (`DragDeployLine`, one-shot `dragState`),
  `internal/attack/hero_manager.go` (a refused pass calls `DisableDrag`),
  `internal/attack/orchestrator.go` (`ResetDrag` per battle), `PROGRESS.md`.
- Device proof: "BlueStacks Virtual Touch" at `/dev/input/event2` reports
  ABS_MT_SLOT/TRACKING_ID/POSITION_X/Y (0..32767 over 1024x768). An injected
  held stroke pans the village, so the panel accepts the stream end to end.
- Live finding: this CoC build deploys nothing from it. Field-only strokes
  (held 0/300 ms, paced with sleeps), field strokes with a pause, and a
  card-anchored stroke (press on the slot, hold, cross to the line, sweep) all
  left the card's count untouched; the reconcile's top-up taps did every
  placement. Four raids with the stroke on: 70%, 60%, 50% destruction vs 80%
  with taps. The emitter itself is sound, so this is the game, not the input.
- Decision: `evdevDragEnabled = false` keeps the primitive and its tests in the
  tree but lets the tap path own deployment; the guard still exists for a build
  where the gesture lands.
- Live re-verify with taps only (`tmp/evdev_run5.log`, `tmp/evdev_taps5.ndjson`):
  100% / 3 stars, Gold 549,395 + 320,000 bonus, Return Home processed. Deploy
  window 17.1 s (Balloons 2.3, EDrags 2.4, Siege 5.4, Spells 1.0, Heroes 5.9),
  beating the previous 17.6 s baseline despite three refused siege placements.
- Cadence: 56 taps, minimum consecutive gap 65 ms, no fixed-interval run,
  `tapaudit -trace tmp/evdev_taps5.ndjson -assert-human` exit 0.
- Still open (audit findings): siege placement refused on all four grounds
  (`siege_placements=0`); battle-result OCR can fail after ~28 animation reads
  and drop the loot row (run3 recorded 0 loot on a 1-star result).
- Next: F.1 dead-base detection.

### 2026-11-17 — H.8: siege one-tap fix, result settle, human-paced default
- Siege cause (live, three raids + screenshots): the Stone Slammer card carries
  NO count badge (a siege is one unit; the bar only prints xN for N>1), so the
  troop counter read slot 239 as 0. The armed-card check also reads the card as
  "still armed" after a successful drop, so every confirmation called a placed
  machine a refusal, fired up to four retries on the now-empty card (the game
  answered "Your Stone Slammer is empty!") and burned 5-7 s of the window.
  Confirmed with battle-start bar captures and the result panel's expended strip.
- Fix: siege is one tap on the troop line's midpoint — ground the game accepted
  seconds earlier — with no confirmation retries and no count-based skip. The
  refused-ground retry path is not entered from this path any more.
- Live proof (`tmp/evdev_run10.log`): siege phase 912 ms (was 5448-7078 ms),
  siege_placements=1, siege_retries=0, and the result panel's expended strip
  shows the x1 Stone Slammer. Raid 80% / 1 star, Gold 437,708 + 320,000 bonus,
  Elixir 689,603, DE 4,765. Deploy window 12.8 s (Balloons 2.2, EDrags 3.0,
  Siege 0.9, Spells 1.0, Heroes 5.5).
- Result parsing: 40 settle attempts; a stable read is banked at the bounded
  give-up with a warning and a partial bonus is never invented; a win that
  reads zero loot on a raid with destruction is treated as a parse failure, not
  a zero-loot row (`zeroLootOnDamagingRaid`). Live raids captured with stars,
  destruction and loot: 100%/3 stars 578,860+320,000; 70%/1 star
  476,112+320,000; 49%/0 stars 302,715; 80%/1 star 437,708+320,000.
- Cadence: shipped default `min_seconds_between_attacks` 30 s -> 300 s, ±22%
  jitter and the 1-in-10 long tail unchanged; an explicit config value still
  overrides it (test `TestAttackIntervalDefaultIsHumanPacedAndConfigurable`).
  Live test config set 300 explicitly and the cooldown waited it out.
- Verified: `./tools/verify.sh` PASS; `go test ./internal/attack ./internal/adb
  ./cmd/tapaudit` PASS; `tapaudit -trace tmp/evdev_taps10.ndjson -assert-human`
  exit 0 (56 taps, min gap 65 ms, no fixed-interval run).
- Open: the 22:18:39 raid still exists in history as 80% / 0 loot — that read
  predates the zero-loot guard; no way to recover its loot row. The result OCR
  needs 13-19 reads on this geometry (~10-15 s) before the bonus column holds
  still; it is bounded and logged, not silent.
- Next: F.1 dead-base detection.

### 2026-11-17 — T.9: UI polish, honest off states, Army loot rates
- Changed: `web/src/styles/{design,views}.css`, `web/index.html`,
  `web/src/App.tsx`,
  `web/src/components/{ArmyView,ConfigView,AccountsView,SettingsView,Dashboard}.tsx`,
  `web/src/armyApi.ts`, `web/public/wails-stub.js`, `app_army.go`,
  `app_army_test.go`, `docs/UI.md`, `BUILD_PLAN.md`, `PROGRESS.md`.
- Fixed: applied the launch wizard's design language across every view (darker
  surfaces, 14/9 radii, 24px page titles, hairline cards, one gold accent,
  mono numerals); added top-bar session status (Idle/Farming + active
  account); Config/Accounts/Settings dependent blocks now dim to 58% with an
  `off-note` while staying editable and saveable; Search's master switch moved
  into its card head; Army plan cards show loot/hour with a measured or
  first-launch basis and their own recipe slot / auto-end values (the active
  plan's slot/auto-end had leaked onto every card).
- Added: `GetArmyPlans` returns `loot_per_hour`, `measured`, `raids` from the
  same estimator as the launch screen; `TestArmyPlansCarryLootEstimates`
  proves the empty-profile fallback; Army list and wizard cannot disagree.
- Verified: `npx tsc --noEmit` PASS; `node --check web/public/wails-stub.js`
  PASS; `./tools/verify.sh --fast` PASS; `./tools/verify.sh` -> VERIFY PASSED
  (full); stub preview at 1280x800 checked idle and with the bot running
  (Farming pill, disabled Settings test tools with visible reason), console
  and network clean. Visual review of Dashboard, Army, Config (Search,
  Schedule), Accounts, Settings, Analytics and the launch wizard; not a
  click-through of every tab at every window size.
- Commit: this commit
- Next: F.1 dead-base detection.

### 2026-11-17 — T.10: responsive polish + Wails parity
- Changed: `web/src/App.tsx`, `web/src/components/{AccountsView,ConfigView,
  LaunchSetup}.tsx`, `web/src/components/ui/Switch.tsx`,
  `web/src/styles/{design,views}.css`, `docs/UI.md`, `BUILD_PLAN.md`.
- Fixed:
  - Frame: forced icon rail below 900px, 68px rail below 760px, sidebar toggle
    and breadcrumb trail hidden there; main-tab and Config sub-tab changes now
    reset `.content` scroll instead of opening a form mid-page.
  - Header actions: master switches use visible ON/OFF text and stay on the
    title row at narrow widths; Settings switch rows and the wizard's lab/pet
    columns stack cleanly; the wizard keeps a `Set up manually` exit at narrow
    widths instead of hiding its only way out; adaptive-loot copy now reflects
    the off state.
  - Density: Config subnav uses the full row at wide widths and a 2-column grid
    below 520px (no clipped `Schedule`); Dashboard keeps its 3-up resource strip
    at the Wails minimum 1024px; heatmap month labels hide below 760px where 52
    columns cannot hold them.
  - Detail: compact Accounts delete action (no stretched empty button), gold
    tone for Gold banked, remaining 8/10/12/16px surface radii normalized to
    the 14/9/6 tokens, light off-block adjusted to 66% (focus 90%) and disabled
    primaries rendered as inert neutrals.
- Verified: `npx tsc --noEmit` PASS; `node --check web/public/wails-stub.js`
  PASS; `./tools/verify.sh` -> `VERIFY PASSED (full)`; `make build-gui` ->
  built `build/bin/ClashGO.app`. Stub screenshots reviewed at 390x844 and
  1440x900 in light and dark, plus 1024x768 with the sidebar both collapsed
  and expanded: every primary view, empty state, Config off state, and all
  three wizard steps. Wails 1200x850 dark render matches the stub except
  native macOS window chrome, version string, and live ADB/config data; the
  macOS window appearance remains `NSAppearanceNameDarkAqua` as documented,
  and sub-1024 layouts remain browser-preview only.
- Commit: this commit
- Next: F.1 dead-base detection.
