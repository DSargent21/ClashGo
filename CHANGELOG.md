# Changelog

All notable changes to this project will be documented in this file.

## [0.3.1] - 2026-08-10

### Added
- **Auto-pop update dialog** — when a new release is published, ClashGO
  now opens the update window by itself (no pill click needed): one-click
  *Update & Restart*, SHA256-verified download, in-place bundle swap, and
  a non-dismissible restart splash. "Later" silences it for the session;
  "Skip version" silences it permanently (`web/src/components/UpdateBanner.tsx`).
- **One-command release publishing** — pushing a `v*` tag now runs
  `.github/workflows/release.yml`, which builds the macOS zip, DMG, and
  `latest.json` on a fresh runner and publishes them to a GitHub Release
  automatically. Uses only the built-in `GITHUB_TOKEN` — no personal
  access token ships in the app, the repo, or the artifacts.

### Fixed
- **Window close no longer freezes the app** — the async log writer now
  flushes blocked callers immediately instead of deadlocking on shutdown
  (`internal/bot/asyncwriter.go`).
- **Packaged-app Config crash** — assets are injected into the .app
  bundle for BOTH packaging paths (DMG and zip), the Config page no
  longer crashes on a nil strategies slice, and the sidebar cleanup was
  removed (`internal/paths`, `Makefile`, `web/src`).
- **`latest.json` min_supported default** — beta releases now default to
  the previous minor instead of the current version, so beta users still
  get the update banner (`Makefile`).
- **App icon + bundle signature** — ClashGO logo lands on the app icon
  and the DMG bundle is re-signed (ad-hoc) after asset injection
  (`tools/build_dmg.sh`).

## [0.3.0-beta] - 2026-08-10

### Added
- **Autonomous-run resilience hardening** (all live-verified on BlueStacks Air):
  - **Emulator-death recovery ladder** — when screen capture dies mid-session
    the bot now escalates transport reconnect → adb-server reset →
    BlueStacks relaunch → boot re-orchestration instead of force-stopping
    the game against a dead transport and spinning forever
    (`recoverEmulator` in `internal/bot/bot.go`).
  - **Panic containment** — recover guards around the capture frame loop and
    the attack-sequence goroutine, so one bad frame or missing asset can no
    longer kill the whole process.
  - **Nil-template fallback** — the template store degrades to an empty
    (non-nil) store when assets can't be resolved, so loot/battle OCR keeps
    running instead of panicking mid-attack (`internal/game/templates.go`).
  - **Process keepalive** — `tools/run_bot_keepalive.sh` respawns the CLI
    bot ~10s after ANY exit (panic, OOM, kill) and pins
    `CLASHGO_ASSETS_DIR` so templates/strategies resolve even under launchd
    (whose default cwd is `/`).
- **Web UI overhaul** — polished dashboard console (severity chips,
  text filter, copy, match highlighting, pause-on-hover auto-scroll),
  config view with live range validation + `role=switch` toggles + keyboard
  accessible combobox, two-step armed reset confirm, ADB status pill with
  semantic colors, analytics donut, dark-mode toggle, and an a11y pass
  (`role=log`, focus-visible rings, `prefers-reduced-motion`).
- **Human-gesture input layer** (autonomous-hardening iteration 1):
  - `adb.SwipeBezier` — low-level `sendevent` quadratic-bezier swipe with
    ease-in-out velocity (accelerate → coast → decelerate) and a random
    10-20% perpendicular arc so every drag reads as a real finger; wired
    into the navigator's village camera pans; degrades to the linear path
    on devices that can't inject raw input. Unit-tested
    (`internal/adb/bezier_test.go`).
  - `TapHuman` now emits ~1-in-5 taps as a 60-130ms press-and-release
    instead of an instant down/up pair, and hesitates with a Gaussian
    reaction delay (base 250ms / σ 70ms — mass in the 180-320ms human
    band, tail ~460ms) before committing. Hot deploy loops (`TapFast` /
    `TapAsync`) are untouched and stay fast.
  - `Navigator.IdlePan` — randomized bezier camera pan + micro-pause +
    mirrored return for idle base-wandering; throttled (~18s) in the
    bot's idle-in-village path and deliberately NOT `recordActivity`, so
    the stuck-watchdog still fires on genuinely stuck idles. Unit-tested
    (`internal/game/navigator_test.go`).
  - `cmd/swipe_probe` — standalone live-gesture verification harness
    (`launcher` / `game` / `idlepan` modes) for Phase-1 style isolated
    emulator action checks.
- **Logging** — `logs/autonomous_loop.log` records each hardening-loop
  iteration's audit findings, live-verification results, and regression
  status.
- **Post-boot splash auto-handling — the bot can now boot unattended.**
  After every relaunch, Clash of Clans shows a short splash chain (the
  "ТАР!" tap-to-continue / collect screen → the CoC castle logo, which
  sits static 1-3 min while the session connects → an optional news /
  announcement splash with a Continue button) before the village loads.
  Previously the classifier misread the collect splash's orange artwork
  as `StateBattle` (1/9 pixels), the stuck-watchdog force-stopped the
  game, and every relaunch re-showed the splash — an endless
  force-stop/relaunch loop with zero attacks. Now:
  - 3 new states — `StateTapToContinue`, `StateNewsSplash`, `StateLogo`
    (`internal/game/types.go`) with classifier rules
    (`internal/game/classifier.go`) verified at 0 distance on live
    captures and 0 false-positives on the village.
  - Auto-dismiss in `processFrame` (`internal/bot/bot.go`) — taps the
    "ТАР!" prompt text (ref 450,195) and the news Continue button (ref
    403,535), each through a single in-flight goroutine guarded by the
    atomic `splashDismissInFlight` flag. Deliberately does NOT
    `recordActivity` so a genuinely-stuck splash variant still trips the
    stuck-watchdog.
  - Boot-grace in `checkStuck` — splash states get a 5-minute window
    (the logo has no progress indicator) instead of the generic 35s
    timeout that caused mid-boot force-restarts.
  - Mirrored dismissal cases in `internal/bot/wall_upgrade.go`.
  - Live-verified: restart loop eliminated (1 startup restart vs 15+),
    splash auto-dismissals, and full attacks completing with correct
    result parsing.
- **Text-based observability tooling — the bot's "eye view" for a
  terminal-only session.**
  - `cmd/screendump` — captures the live screen (or reads a saved PNG),
    runs the bot's real classifier with per-rule pixel evidence, renders
    a color-mapped ASCII layout, probes key button pixels, and
    optionally OCRs on-screen text. `-watch` loops every 3s.
  - `tools/observe.sh` — one-shot bundle: capture + classify + color map
    + OCR, artifacts saved to `obs/<timestamp>/` with an `obs/latest`
    symlink for convenience.
  - `tools/ocr.swift` — on-device Apple Vision OCR (no tesseract / no
    network); prints `x,y,w,h | text` per recognized region in
    screenshot pixel space.
  - `cmd/classify_probe` / `cmd/result_probe` / `cmd/swipe_probe` —
    standalone diagnostics for classifier state, battle-result OCR
    parsing, and human-gesture verification respectively.
  - See `docs/OBSERVABILITY.md` for the full workflow.

### Changed
- **Massive dead-code cleanup** — removed ~16.5k lines of unreachable code
  and pruned the repo tree:
  - **Deleted the NATS event-bus subsystem** (`internal/bus`,
    `internal/agent`, `internal/geo`, `internal/world`,
    `game/enricher.go` + `game/explorer.go`, `docker-compose.yml`) — zero
    production references; NATS deps dropped from `go.mod`/`go.sum`.
  - **Removed ~35 verified-dead functions** across `attack`, `bot`, `game`,
    `vision`, `logger`, `adb`, `training`, `updater`, `strategy` — incl. the
    NDJSON log mirror, `ParseCSV`, `ClassifyStateFast`, `SpellLine`,
    `isUnitSelected`, `hasColorSignature`, `WaitForSlotEmpty`, the legacy
    `SlotManager` helpers, the validator `Planner` engine
    (`internal/attack/plan.go` + its test), `game.Calibrator`, and the
    `adb.WithDeviceID` option.
  - **Deleted 48 unreferenced `cmd/` diagnostic tools** — kept the five wired
    into Makefile/scripts (`attack_record`, `capture_template`,
    `design_attack`, `release_manifest`, `test_wall_upgrade`).
  - **Dropped dead struct fields** — `app.echo`, `transport.seq`,
    `bot.lastFrameTime`, `bootorchestrator.mu`, `recovery.onApply`,
    `updater.manifest`, `troop_counter.calibrated/scaleX/scaleY`.
  - **Consolidated duplicated logic** — `app.go` stats merge extracted into a
    `mergeStats` helper; `Transport.ExecRaw` merged into `Exec`; the
    `version.go` build-date + Makefile ldflag removed.
  - **Frontend** — fixed 4 unused-variable TS errors (`App.tsx`, `main.tsx`,
    `Sidebar.tsx`).
  - **Docs + tree** — README / DESIGN / PERFORMANCE / CHEST refreshed for the
    new layout; orphaned `assets/grab/` screenshots and stale `.gitignore`
    entries removed.

### Fixed
- **BlueStacks Air cold boot burned 90s then failed** — `waitForVMProcess`
  waited for `qemu-system`/`hd-adb` process names that never appear on
  BlueStacks Air for Apple Silicon (the VM runs in-process), so every cold
  boot hit the full wait and logged a spurious "VM did not start" failure.
  An open adb port (5555+) now counts as a VM-up signal — the same signal
  the ADB-connect wait already uses (`internal/adb/emulator_mac.go`).
- **Nil-pointer panic on missing templates** — `NewLootRecognizer` panicked
  on a nil template store when assets didn't resolve (e.g. running from a
  launchd cwd of `/`), killing the bot at attack entry. The store now
  falls back to an empty store and the attack goroutine is panic-guarded.
- **Attack History lagged the loot totals** — the history entry was only
  appended after Return Home + wall upgrades (which can take minutes), so
  the dashboard showed fresh gold/elixir totals long before the new attack
  row appeared — and the entry was lost entirely if Return Home failed.
  `executeAttackSequence` now records the report and fires the UI refresh
  at battle-result parse time (`internal/bot/bot.go`).
- **League Bonus gold misread by a factor of 10** — the bonus column's
  right padding clipped the trailing digit (bonus gold "+256 000" read as
  25600): the digit lost its right edge and its template-match score fell
  below the 0.5 floor. Narrow columns now get relaxed padding
  (`internal/game/loot.go`) and the bonus ROI's right edge was widened
  (`assets/battle_loot_rois.json`). Regression-tested via
  `TestLootVictory/screen_victory` on the tracked fixture
  (`internal/game/testdata/screen_victory.png`); the former
  `screen_victory_live.png` fixture was removed for privacy (it was a live
  capture containing real player names and was gitignored, so it silently
  skipped on fresh clones anyway).
- **Endless force-stop/relaunch loop on the post-boot collect splash** —
  the "ТАР!" tap-to-continue screen was misclassified as `StateBattle`, so
  the stuck-watchdog force-stopped the game and every relaunch re-showed
  the splash. Root cause, the mapped boot-splash chain, and the fix are
  documented in the Added entry above and in `docs/OBSERVABILITY.md`.
  Verified live: restart count dropped from 15+ to 1, and the bot
  completed multiple attacks with correct result parsing.

## [0.2.0-beta] - 2026-08-05

### Added
- **Event-agnostic chest detection** — `StateChestReward` classifier rule
  (`internal/game/classifier.go`) now fires ONLY on the `hammer`
  ("TAP TO OPEN") template match, so dark bottom pixels on a normal
  village no longer false-trigger dismissal. Survives CoC event art swaps.
- **Template-driven Continue button** — `chestContinueTap`
  (`internal/game/chestdismiss.go`) now auto-detects a `btn_continue`
  template (`assets/templates/btn_continue.png`) and taps the matched
  point, falling back to `assets/continue_button.json` only if no template
  is loaded. Removes the need for a hand-measured Continue rect. See
  `docs/CHEST.md`.
- **Device-independent CPU metric** — `internal/bot/cputime.go` reads
  process CPU time via `getrusage(RUSAGE_SELF)` (user + system) and exposes
  `cpu_time_sec` (absolute, kernel-accurate, comparable across machines)
  plus `cpu_cores` (fraction of one core over the last sample window).
  Wired into `game.SystemHealth` + `bot.BotStats`, surfaced in the GUI
  Settings view. A non-unix fallback returns zero so the build stays
  portable. See
  [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md#cpu-metric-absolute-time-not-a-percentage).
- **Bot boot orchestration + ADB recovery module** — a self-contained
  BlueStacks bring-up path that survives the common failure modes on
  macOS Sequoia:
  - `internal/adb/bootprobe.go` — repeated `CaptureScreen` luma probe that
    verifies BlueStacks is past the boot animation (not just responsive to
    `adb shell`).
  - `internal/bot/bootorchestrator.go` + `bootprofile.go` +
    `boot_report.go` — orchestrates the bring-up sequence; profile
    selection is gated on prior launch results so re-runs don't retry the
    same known-failing step.
  - `internal/bot/recovery.go` — fallback recovery ladder:
    `softResetAndroid` (cheap, non-destructive `adb shell stop && start`)
    before the heavier `ResetAdbServer` (`adb kill-server && start-server`).
  - `internal/adb/emulator_mac_test.go` — table-driven tests for the
    single-launch `EnsureBlueStacksMac` path.
- **Context-aware ADB shell/capture wrappers** — `ShellWithContext`,
  `CaptureScreenWithContext`, `ExecWithContext` plus the `ShellRunner`
  interface. The context-aware variants force the transport closed on
  `<-ctx.Done()` so a 2s probe timeout can't hold the transport mutex for
  the full 30s budget.
- **`AutoDetectDevice` verifies BlueStacks** — no longer trusts
  `adb devices` verbatim. Opens a fresh transport and checks `getprop` so a
  stale `localhost:5555 device` row is skipped, plus a best-effort
  `adb connect localhost:5555` refresh.
- **`EnsureBlueStacksMac` is single-launch + smart pre-check** — scans
  candidate ports `5555..5565` (TCP-only, 2s timeouts), honors an existing
  healthy BlueStacks session (no kill+launch), then one
  `open -a BlueStacks.app` attempt. Removed the kill-and-retry path.
- **`ResetAdbServer` / `SoftResetAndroid`** on `internal/adb.Client` —
  exposed for the recovery ladder. Both documented with SAFETY notes
  (`adb kill-server` is global to the host Mac).
- **Wall-upgrade single-tap-each-X dismiss flow** — when a popup survives a
  single X tap, the bot does ONE additional tap on the alt X (if configured)
  instead of offset-jittering the same one. The `aborted` flag is now
  per-wall-iteration, and `defensiveDualTapAndLogClose` centralises the
  tap-both-X + uniform `close_failed` payload. `asset_driven_modal_*`
  events now carry `name + reason + primary_still_up + alt_still_up` and
  `primary_px + alt_px + all_up`.
- **In-app update system** — ClashGO now talks to GitHub Releases. On
  startup (and every 6h) a lightweight service checks for a new release,
  compares versions with a pre-release-aware semver, and surfaces a banner
  in the dashboard. `internal/updater` does ETag-cached polling,
  SHA256-verified downloads into
  `~/Library/Application Support/ClashGO/updates/`, Finder-open install
  plus a bundled `install_update.sh` in-place .app replacement helper.
  Every release ships a `latest.json` manifest alongside the zip, emitted
  by the new `cmd/release_manifest` helper. `make release VERSION=...`
  now produces `ClashGO-v{VERSION}-macOS.zip` + `latest.json`.
- **New diagnostic tooling** — `cmd/test_wall_upgrade` (Phase-logging
  harness), `cmd/adb_probe` / `cmd/boot_debug` (bring-up probes),
  `cmd/capture_template` (template ROI capture), plus `tools/picker.py`
  + calibration scripts documented in `tools/CALIBRATION_README.md`.
- **`docs/CHEST.md`** — capture + tuning workflow for chest/continue
  templates and the detection rule.
- **Classifier detection tests** — synthetic-frame guards that the chest
  rule fires on a chest screen and does NOT false-positive on MainVillage.
- **Resource-usage documentation** — README, `docs/PERFORMANCE.md`, and
  `docs/DESIGN.md` carry RAM/CPU estimates for ClashGO alone and combined
  with BlueStacks at 860×732 / 160 DPI (incl. the 15 FPS battle case).

### Changed
- **`auto_edrag_rush.yaml` switched to `target_edge: "Random"`** — picks a
  random corner per attack for an unpredictable per-attack landing side,
  ideal for unattended runs. Backward compatible: `"Rotate"` (deterministic
  cycle), specific corner names, and legacy side names still work.
- **Per-corner formula override workflow** — `Formula.CornerOverrides`
  (`map[string]map[string]UnitEntry`, omitempty) holds optional per-corner
  overrides authored via `cmd/design_attack -corner BL/TR/TL`. The
  orchestrator (`DeployDynamicV2`) merges a per-corner override with `Units`
  per-unit and uses the result INSTEAD of mirroring when present — fixing
  the "mirror lands too close to the red line on BL/TR/TL" symptom. See
  `docs/formula-authoring.md`.
- **`Formula.MirrorForCorner(targetEdge)`** — reflects every `P` / `P1` /
  `P2` / line point across the screen axes (`BottomRight: identity`,
  `BottomLeft: mirrorX`, `TopRight: mirrorY`, `TopLeft: mirror both`).
  Accepts full + abbreviated corner names plus a substring fallback.
- **`Formula.LoadFile(path)`** — loads a formula from a direct path, used
  by `cmd/design_attack -verify`.
- **`cmd/design_attack` is the one-stop authoring/verify tool** —
  `-live` captures from adb directly (no pre-saved PNG needed),
  `-verify <formula.json>` renders a 2×2 mirror grid saved to
  `verify_grid.png`, `-corner <BR|BL|TR|TL>` picks the authored corner.
  Required-flag check now fails fast before the capture.
- **Removed 5 redundant strategy files** — `top_edrag_rush.yaml`,
  `bottom_edrag_rush.yaml`, `left_edrag_rush.yaml`,
  `right_edrag_rush.yaml`, `balloon_edrag_rush.yaml` were single-side
  variants of `auto_edrag_rush.yaml`. `valk_spam.yaml` is kept (different
  attack system — FourSides Valkyrie ring).
- **Build metadata via ldflags** — the version string is injected at
  compile time into `main.version` so `bot_cli --version` and the Wails GUI
  show the same value. The Makefile picks up `VERSION` from `wails.json`.
- **Stray PNG cleanup** — removed untracked runtime debug artifacts
  (`verify_grid.png`, `last_failure.png`, `last_battle_result.png`,
  `diag_*.png`).

### Fixed
- **Chest Continue tap timing** — `chestContinueTap` now waits for the
  button to render and retries the tap (up to `chestContinueMaxTaps`,
  default 3) with a settle between attempts, because the overlay can lag
  the chest-open animation by ~1s and a single early tap classifies as a
  transitional `Unknown`. On final failure it dumps
  `debug_chest_continue_fail.png` for ROI recapture.
- **MatPool key collision (silent corruption)** — `getPoolKey` previously
  cast dimensions to a single `rune`, collapsing distinct sizes (e.g. rows
  100 vs 256) into one key and risking panics / mis-sized Mats. Keys are
  now encoded numerically (`%d_%d_%d`).
- **Frame-encode Mat leak** — the Live View encode path now closes the JPEG
  buffer on every branch and always returns the pooled half-frame Mat, so a
  failed encode cannot leak it.
- **`lastNav` global race** — moved the ArmyCamp navigation cooldown
  tracker from a package-level `var` to a `Bot` field, removing
  cross-instance state sharing / data races.
- **`Health().LastCapture` lied** — now reports the real last successful
  capture time instead of `time.Now()` on every call.
- **Attack-history wipe on read error** — the in-memory `historyCache`
  (seeded from disk at `NewBot`) is now the source of truth, so a transient
  `attack_history.json` read failure can no longer silently drop all prior
  attacks.
- **Duplicated ROI switch** — extracted `Bot.buttonROI()` so the
  wait-for-button and find-and-click paths share one definition.
- **`MirrorForCorner` failed for abbreviated corner names** — replaced
  `strings.Contains` substring matching with a switch on the normalized
  corner name covering full + abbreviated forms.
- **Mirror pointer-aliasing in `runVerify`** — `MirrorForCorner` mutates
  `*Point` in place, so 4 sequential mirrors on shared pointers collapsed
  to TL geometry. Fixed by deep-copying points and lines per iteration.
- **2×2 grid scaling in `runVerify`** — quadrants are at `w/2 × h/2`;
  `ApplyScreenScale` is applied after the mirror so deploy lines land on
  the quadrants.
- **Required-flag check happened after the live capture** — missing
  `-strategy` wasted an adb screencap before showing usage. Reordered.
- **Theme defaults to light** instead of honoring OS
  `prefers-color-scheme` — macOS dark default + `bg-zinc-950` produced an
  indistinguishable-black surface (perceived "black screen"). The Settings
  view lets users opt back into dark.
- **`safeEventsOn` defensive wrapper** — `EventsOn` no longer throws
  synchronously when `window.runtime` is undefined (the Wails runtime
  bridge isn't present during `npm run dev`), so the React tree no longer
  unmounts on the failure path.
- **`UpdateBanner` Rules-of-Hooks violation** — the SHA256-flash effect was
  placed after three early returns, so its hook index varied across renders
  and React threw "Rendered more hooks than during the previous render".
  Hoisted above all early returns.
- **Sidebar brand swap** to `clashgo-logo.png`.

### Performance
- **Audit-driven perf pass** — zero behavior changes, multiple-order-of-
  magnitude CPU/RAM reductions across the bot + UI. Full rationale +
  methodology in [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md):
  - Removed dead `b.OnFrame` EventsEmit (`live_feed`) — up to 10 FPS in
    battle state, every emit pushing a 50–150 KB base64 JPEG through
    WailsIPC to zero React subscribers. **~7–15% battle-state CPU freed,
    ~5–15 MB transient RSS eliminated.**
  - Removed `bot_log` EventsEmit in `WailsLogWriter.Write` — every zerolog
    line generated a wasted WailsIPC round-trip. **~1–3% CPU freed.**
  - Tab-gated screenshot poll — the 1 Hz interval now only mounts on the
    Live View tab. **~0.1–0.3% continuous CPU saved.**
  - `vision.MatchMultiScaleROICached` keyed by rule name — fixes the
    empty-name bypass that re-allocated 3 scaled-template Mats per call
    (180 alloc/free per second eliminated). **~1–2% CPU freed.**
  - `world.MinWriteInterval` 250 ms → 1 s — 4× fewer tmp+rename flushes.
    **~0.5–0.8% CPU freed.**
  - `GetAttackHistory` in-memory cache — no more disk re-read on every
    0.5 Hz React poll. **~0.1–0.2% CPU freed.**
  - Detached heavy teardown in `StopBot` — UI Stop now returns ~10–50 ms
    instead of 1–3 s. **~95% Stop-IPC latency cut.**
- **Aggregate battle-state estimate** — **~10–20% CPU freed + ~10–20 MB RSS
  + ~40–60% GC-cycle reduction** during battle state. Idle (bot on, no
  battle) saves ~3–5% CPU. **Verify on your machine** with the recipe at
  the bottom of [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md).

### Security
- **`.gitignore` covers `rotation_state.json`** — runtime state of the
  `Rotate` cycle. Not code, not committed.
- **Downloads are verified against the SHA256 in `latest.json`** before the
  apply step. Mismatches abort cleanly and the corruption file is removed.
- **macOS quarantine + ACLs are stripped** on the bundled helper before
  relaunch, avoiding Gatekeeper's "damaged" error.

---

## [0.1.0-beta] - 2026-06-12

### Added
- **GUI Dashboard**: Modern Wails v2 + React interface for remote bot management.
- **Vision Engine**: High-performance GoCV/OpenCV integration for real-time base analysis.
- **Dynamic Navigator**: Dijkstra-based state traversal for reliable UI navigation.
- **Anonymization Layer**: Core engine now strictly excludes identifying base screenshots from diagnostic logs.

### Changed
- **Safety First**: Updated `.gitignore` and build pipelines to ensure no account-leaking data is ever committed.
- **Project Structure**: Streamlined repository layout; consolidated scripts and expanded documentation.
- **Theme**: Set Light Mode as the default initialization state for new launches.

### Fixed
- **App Icon**: Corrected missing branding in the macOS application bundle.
- **Interface Stability**: Resolved regressions in mock devices and interface satisfaction for unit tests.

---
*Initial Beta Release - Use with caution. Report bugs via GitHub Issues.*
