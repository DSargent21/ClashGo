# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added
- **Bot boot orchestration + ADB recovery module** — a self-contained
  BlueStacks bring-up path that survives the common failure modes on
  macOS Sequoia.
  - `internal/adb/bootprobe.go` — repeated `CaptureScreen` luma probe
    used to verify BlueStacks is past the boot animation (not just
    responsive to `adb shell`). Boots are considered complete only
    when OpenCV can produce a non-trivial frame on the working adb port.
  - `internal/bot/bootorchestrator.go` + `bootprofile.go` +
    `boot_report.go` — orchestrates the bring-up sequence using the
    new context-aware adb shell/capture helpers. Profile selection
    is gated on prior launch results so re-runs don't retry the same
    known-failing step (e.g. the pgrep-BlueStacks-shell false-positive
    that BlueStacksAI used to trigger).
  - `internal/bot/recovery.go` — fallback recovery ladder: `softResetAndroid`
    (cheap, non-destructive `adb shell stop && start`) before the
    heavier `ResetAdbServer` (`adb kill-server && start-server`).
    Picked by the orchestrator based on how stuck the boot is.
  - `internal/adb/emulator_mac_test.go` — table-driven tests for the
    single-launch `EnsureBlueStacksMac` path (no retry loop so a
    wedged GUI binary never thrashes AppKit).
- **`internal/adb.Client` context-aware shell/capture wrappers** —
  `ShellWithContext`, `CaptureScreenWithContext`, `ExecWithContext`
  plus the `ShellRunner` interface. The context-aware variants force
  the transport closed on `<-ctx.Done()` so a 2s probe timeout can't
  leave the transport's internal mutex held for the full 30s
  transport.Exec budget (this used to hang every subsequent probe
  signal in tight succession).
- **`AutoDetectDevice` verifies BlueStacks** — previously trusted
  `adb devices` verbatim. Now opens a fresh transport and checks
  `getprop` so a stale `localhost:5555 device` row from a previous
  run (force-killed socket) is skipped instead of accepted. Also
  does a best-effort `adb connect localhost:5555` so the local
  adb-server has a fresh entry when the server itself was just
  restarted.
- **`EnsureBlueStacksMac` is single-launch + smart pre-check** —
  scans candidate ports `5555..5565` (TCP-only, with 2s/timeouts),
  honors an existing healthy BlueStacks session (skips kill+launch
  to avoid resetting the user's game state), then does one
  `open -a BlueStacks.app` attempt with VM-process + adb port
  waits. Removed the kill-and-retry path: every retry on this
  failure mode reproduces the same AppKit exit-125ms signature.
- **`cmd/test_wall_upgrade`** — standalone Phase-logging harness
  that drives the same `RunWallUpgradeLoop` the GUI uses. Drives
  the recent refactor end-to-end without requiring the Wails app.
- **`cmd/adb_probe` / `cmd/boot_debug`** — diagnostic tools that
  print the bring-up probe results and confirm the new code paths
  in isolation (no need to launch the full bot).
- **`cmd/capture_template`** — UI screenshot capture utility used
  during the template ROI calibration runs.
- **`tools/picker.py` + `tools/pick_upgrade_exit.sh` +
  `tools/test_wall_upgrade.sh`** — the calibration + replay
  tooling backing the asset-driven wall-upgrade flow. Documents
  live in `tools/CALIBRATION_README.md`.
- **`ResetAdbServer` / `SoftResetAndroid`** on
  `internal/adb.Client` — exposed for the recovery ladder. Both
  documented with SAFETY notes (`adb kill-server` is global to
  the host Mac).
- **Wall-upgrade single-tap-each-X dismiss flow** — when the gold
  (or elixir) button's popup survives a single X tap, the bot now
  does ONE additional tap on the alt X (if configured) instead of
  offset-jittering the same one. Spec-mandated by the user:
  "click X, then click X again in the confirm menu".
- **`aborted` flag is per-wall-iteration** — moved from
  function-scope into the `for upgradeCount` body so a defensive
  transport-abort on wall N no longer leaks `aborted=true` into
  wall N+1.
- **`defensiveDualTapAndLogClose` helper** — centralises
  tap-both-X + sleep + uniform `close_failed` payload emission +
  best-effort `runDismiss`. Replaces two duplicated defensive
  blocks in the asset-driven flow.
- **`asset_driven_modal_close_failed` unified payload** — all
  three close-failed emitters (capture-fail defensive, verify-fail
  defensive, post-tap-still-up) carry
  `name + reason + primary_still_up + alt_still_up`. JSONL
  consumers can key on a single shape.
- **`asset_driven_modal_verified` payload carries `primary_px +
  alt_px + all_up`** — symmetric with `asset_driven_modal_checked`
  so post-tap threshold tuning works against both events.

### Changed
- **`auto_edrag_rush.yaml` switched to `target_edge: "Random"`** — (now the default for `auto_edrag_rush.yaml`) — picks a random corner per attack (TopLeft / TopRight / BottomLeft / BottomRight) for an unpredictable per-attack landing side, ideal for unattended runs. Backward compatible: `"Rotate"` (deterministic cycle), specific corner names, and legacy side names (`top` / `right` / `bottom` / `left`) all still work.
- **Per-corner formula override workflow** — `Formula.CornerOverrides` (`map[string]map[string]UnitEntry`, omitempty) holds optional per-corner partial overrides authored via `cmd/design_attack -corner BL/TR/TL`. The orchestrator (`internal/attack/orchestrator.go::DeployDynamicV2`) merges a per-corner override with `Units` per-unit and uses the result INSTEAD of mirroring when present, so the user can pin coords that match the base's actual red-line position on each side. This fixes the previous "mirror lands too close to the red line on BL/TR/TL" symptom. `assets/strategies/auto_edrag_rush_formula.json` now has `units` (BR default) + 3 `corner_overrides` (BL/TR/TL) populated.
- **`docs/formula-authoring.md`** — canonical reference for the per-corner formula workflow. Sections: TL;DR with 4 corner commands, `target_edge` modes, `cmd/design_attack` flag reference, per-corner authoring walkthrough, partial override semantics, schema reference, `MirrorForCorner` reflection rules table.
- **`Formula.MirrorForCorner(targetEdge)`** in `pkg/formula/formula.go` — reflects every `P` / `P1` / `P2` / `Lines[i].P1` / `Lines[i].P2` across the screen axes (`BottomRight: identity`, `BottomLeft: mirrorX`, `TopRight: mirrorY`, `TopLeft: mirror both`). Accepts both full canonical names (`BottomLeft`) and abbreviated forms (`BL` / `TR` etc.) plus a substring fallback for freeform values like `"left"`.
- **`Formula.LoadFile(path)`** — loads a formula from a direct path. Used by `cmd/design_attack -verify` to read a previously-saved formula for visual inspection.
- **`cmd/design_attack` is now the one-stop tool for authoring + verifying per-attack formulas**:
  - `-live` — captures the screen from adb via `adb exec-out screencap -p` (no pre-saved `-screen` PNG needed)
  - `-verify <formula.json>` — loads an existing formula, shows a 2x2 grid of the formula mirrored to all 4 corners via `MirrorForCorner`. The grid PNG is saved to `verify_grid.png`. Press any key to exit
  - `-corner <BR|BL|TR|TL>` — which corner is being authored. `BR` (default) saves to `formula.units`; the others save to `formula.corner_overrides[<CORNER>]` (preserves previously-saved corners)
  - Required-flag check now happens BEFORE the live capture so a missing `-strategy` / `-out` fails fast without an adb screencap

### Changed
- **`auto_edrag_rush.yaml` switched to `target_edge: "Random"`** — combined with the per-corner override path, this is the new canonical "all 4 sides" workflow. Replaces both the previous `Rotate` cycle and the 5 redundant single-side strategies.
- **Removed 5 redundant strategy files** — `top_edrag_rush.yaml`, `bottom_edrag_rush.yaml`, `left_edrag_rush.yaml`, `right_edrag_rush.yaml`, `balloon_edrag_rush.yaml`. All were single-side variants of `auto_edrag_rush.yaml` (different `target_edge` value, otherwise identical unit composition). `auto_edrag_rush.yaml` + `target_edge: "Random"` + per-corner overrides covers all use cases. `valk_spam.yaml` is kept (different attack system — FourSides Valkyrie ring).
- **Stray PNG cleanup** — removed untracked debug artifacts (`verify_grid.png`, `last_failure.png`, `last_battle_result.png`, `diag_*.png`). These were runtime output, never intended for the repo.

### Fixed
- **`MirrorForCorner` failed for abbreviated corner names** — the previous `strings.Contains(corner, "left")` / `"top"` substring matching worked for the orchestrator's full names (`BottomLeft` / `TopRight`) but produced a no-op for the abbreviated forms (`BL` / `TR`) used by `cmd/design_attack -verify`'s 2x2 grid labels. All 4 quadrants rendered the same unmirrored geometry. Replaced with a switch on the normalized corner name covering both full + abbreviated forms (plus a freeform substring fallback).
- **Mirror pointer-aliasing in `runVerify`** — `MirrorForCorner` mutates `*Point` in place, so 4 sequential mirrors on shared pointers all collapsed to the TL geometry. Fixed by deep-copying `P` / `P1` / `P2` into fresh `*Point` allocations and the `Lines` slice (LinePoint is a value type) per iteration.
- **2x2 grid scaling** in `runVerify` — formula's authored coords are in 860×732 frame, quadrants are at `w/2 × h/2`. Added `ApplyScreenScale(mirrored.Screen.W, mirrored.Screen.H, halfW, halfH)` after the mirror so the deploy lines land on the quadrants (a BL mirror of `(60, 110)` was previously off the right edge of a 430×366 quadrant).
- **Required-flag check happened after the live capture** in `cmd/design_attack` — missing `-strategy` wasted an adb screencap before showing the usage. Reordered so the check fires before the capture.

### Security
- **`.gitignore` covers `rotation_state.json`** — runtime state of the `Rotate` cycle (per-process index + last-written `last_index`). Not code, not committed.

### Fixed
- **Theme defaults to light** instead of honoring OS
  `prefers-color-scheme`. macOS defaults to dark and Wails also
  defaults the window to `NSAppearanceNameDarkAqua`; layering
  `bg-zinc-950` over the dark window BG produces an
  indistinguishable-black surface (perceived as a "black screen")
  rather than a real Crash. The Settings view lets users opt back
  into dark.
- **`safeEventsOn` defensive wrapper** in `web/src/App.tsx` —
  `EventsOn` no longer throws synchronously when `window.runtime`
  is undefined (the Wails runtime bridge isn't present during
  `npm run dev`), so the React tree no longer unmounts on the
  `TypeError: Cannot read properties of undefined (reading
  'EventsOnMultiple')` failure path.
- **`UpdateBanner` Rules-of-Hooks violation** — the SHA256-flash
  `useEffect` was placed AFTER three early returns (`!status` /
  `status.state === 'restarting'` / `!visible`), so its hook
  index varied 7 → 8 across renders and React threw
  `"Rendered more hooks than during the previous render"`,
  unmounting the tree. Hoisted the effect above all early
  returns; body now guards against missing `status`.
- **Sidebar brand swap** to `clashgo-logo.png`.

- **In-app update system** — ClashGO now talks to GitHub Releases. On
  every startup (and every 6 hours thereafter) a lightweight Go
  service checks for a new release, compares versions with a tiny
  pre-release-aware semver, and surfaces a banner in the dashboard.
- **`internal/updater`** package — HTTP poller with ETag caching
  (respects the 60/hour GitHub rate limit), SHA256-verified asset
  downloads into `~/Library/Application Support/ClashGO/updates/`,
  Finder-open for the install step, plus a bundled
  `install_update.sh` helper for in-place .app replacement.
- **`latest.json` release manifest** — every release now ships a
  manifest alongside the binary with the SHA256, asset URL, and
  optional `min_supported` gate. The Makefile auto-emits it via the
  new `cmd/release_manifest` helper.
- **UpdateBanner component** — pill in the dashboard header opens a
  modal with release notes and four buttons: Update (download),
  Open in Finder (install), Skip version (persisted to
  `skip_version.txt`), and Later.
- **`make release VERSION=...`** now produces:
  - `ClashGO-v{VERSION}-macOS.zip` (binary)
  - `latest.json` (manifest)
  - The DMG is unchanged for now; the helper script is bundled inside
    `Contents/Resources/install_update.sh`.

### Changed
- **Build metadata via ldflags** — the version string is now injected
  at compile time into `main.version` so `bot_cli --version` and the
  Wails GUI show the same value. The Makefile picks up `VERSION` from
  `wails.json` automatically.

### Security
- Downloads are verified against the SHA256 in `latest.json` before
  the apply step. Mismatches abort cleanly and the corruption file is
  removed.
- macOS quarantine + ACLs are stripped on the bundled helper before
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
