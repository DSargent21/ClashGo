# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

### Added
- **`target_edge: "Rotate"` YAML mode** - cycles through the 4 corners (TopLeft -> TopRight -> BottomRight -> BottomLeft) across attacks via a persistent on-disk counter (`rotation_state.json` in the config dir). Survives process restarts so a long-running bot distributes attacks evenly across all 4 sides instead of re-starting at TopLeft on every launch. Backward compatible - "Random" and any direct corner string still work as before.
- **Per-corner hero drop pinning** - `pCfg.HeroTargets[corner]` in `precision_config.json` is now honored by `HeroManager.resolveHeroTarget` in addition to the formula path. Combined with "Rotate", every attack in the cycle drops heroes at the user's per-corner pinned point without requiring a per-unit `formula.json`.

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
