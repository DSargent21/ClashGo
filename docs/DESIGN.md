# ClashGO Architecture & System Design 📐

High-performance, lightweight Clash of Clans automation engine written in Go and OpenCV for Apple Silicon macOS.

---

## 🎯 Architectural Principles

1. **Native Apple Silicon Execution**: Built directly for ARM64 macOS without Rosetta emulation, achieving low thermals, sub-second startup, and minimal memory footprint (~25–40 MB RSS headless).
2. **Persistent ADB Engine**: Connects directly to the local ADB server over a raw persistent TCP socket (`exec:/system/bin/screencap`), reducing capture latency to ~30–50ms and eliminating `app_process` JVM spawn churn.
3. **Resilient Vision & Calibration**: Combines multi-scale template matching with resolution-independent HUD affine mapping (`internal/game/calibration.go`), pooled result Mats, and settled multi-frame OCR verification.
4. **Self-Healing State Watchdogs**: Multi-layered recovery ladder covering emulator disconnects, post-boot splash chains ("TAP!" prompt $\to$ loading logo $\to$ event news), modal popups, and battle result timeouts.
5. **Dual Headless & Desktop Interfaces**: Complete separation between headless CLI daemon (`bot_cli` via `//go:build cli`) and the desktop GUI harness (Wails v2 + React).

---

## 🏗️ Subsystem Architecture

### 1. Attack Lifecycle Flow

```
Main Village (5Hz capture loop)
  └── Army readiness check
        └── Navigate to Battle
              ├── Tap Find Match (search button)
              ├── Read Available Loot & Dark Elixir filters
              │     ├── Loot < threshold → Tap Next (bounded by MaxSearchSkips)
              │     └── Loot ≥ threshold → Commit Attack
              ├── Full zoom-out injection (ZoomInject gesture / pinch-drag)
              ├── Load YAML strategy + formula coordinates
              ├── Rapid deployment (DeployDynamicV2 / 4-sided bursts)
              ├── Mid-battle spell deployment & hero ability activation
              ├── 4x speed-up auto-toggle (after 2 minutes)
              ├── Wait for battle end or 50% damage auto-end target
              ├── Settled result panel OCR (Gold, Elixir, DE, Stars, Bonus)
              ├── Record attack history JSON
              ├── Return Home
              └── Village Progression (Build Overview 2.0 & Wall Upgrades)
```

### 2. Persistent ADB Transport Layer

```
Client (High-level API: CaptureToMat, Tap, Swipe, Shell)
  └── Transport (Direct TCP connection to ADB server 127.0.0.1:5037)
        ├── connect() → TCP dial → CNXN handshake
        ├── setTransport() → host:transport:<device>
        ├── exec(service) → length-prefixed packet protocol
        ├── persistent shell pipe (exec:app_process / exec:screencap)
        └── auto-reconnect & recovery ladder on transport loss
```

### 3. Game State Machine

```
Capture Loop (200ms ticker)
  └── Capture Frame → Classify (pixel invariants) → 2-Frame Confirm
        └── State Change Dispatcher:
              ├── StateMainVillage → Attack search or economy loop
              ├── StateBattle → Battle observation & stall watchdogs
              ├── StateBattleResult → Settled loot OCR & Return Home
              ├── Boot Splashes (TapToContinue / Logo / NewsSplash) → Auto-dismiss
              └── Interrupt States (Obstacles, Modals, Gem popups) → Defensive dismiss
```

### 4. Village Progression & Economy Engine

The Build Overview 2.0 engine (`internal/bot/builder_manager.go`) automates base progression between attacks:
- **Prioritization Filters**: `heroes_first`, `cheapest_first`, `most_expensive_first`.
- **Builder Allocation Policies**: `reserve_builders` policy ensures an unassigned builder remains available for continuous wall dumps.
- **Resource Spend Limits**: `max_gold_per_upgrade`, `max_elixir_per_upgrade`, `max_de_per_upgrade`.
- **Laboratory Research**: Automated category taps (`troops_first`, `spells_first`, `siege_first`) respecting Dark Elixir permissions (`lab_allow_de`).
- **Hero Pets**: Priority queue ordering across active hero pets.
- **Wall Upgrades**: Single-resource targeted upgrades (`gold`, `elixir`, `both`) with kinetic inertia dampening and camera repositioning.

---

## 📂 Project Structure

```
ClashGO/
├── Makefile                     # Build targets (build-cli, build-gui, release, package)
├── main.go                      # Wails GUI application entry point
├── cli.go / cli_test.go         # Headless CLI entry point (//go:build cli)
├── app.go / app_test.go         # Wails frontend IPC bindings & stats aggregator
├── version.go                   # LDFLAGS-injected version and commit metadata
├── wails.json                   # Wails configuration schema
│
├── internal/
│   ├── adb/                     # Persistent TCP transport, screencap, gesture injection
│   ├── attack/                  # Battle orchestration, 4-sided deployer, hero manager, spells
│   ├── bot/                     # Main bot capture loop, builder manager, wall upgrades, scheduler
│   ├── config/                  # Typed JSON configuration models & default values
│   ├── game/                    # State classifier, resolution calibration, loot OCR, navigation
│   ├── paths/                   # Cross-platform asset and configuration path resolution (~/.clashgo)
│   ├── logger/                  # Structured zerolog logging subsystem
│   ├── updater/                 # GitHub Releases auto-detection, SHA256 verification, hot-swap
│   └── vision/                  # OpenCV template matching, pixel search, pooled result Mats
│
├── pkg/
│   ├── strategy/                # YAML strategy parser & composition validator
│   └── formula/                 # Geometric deployment formula calculations & reflections
│
├── assets/
│   ├── strategies/              # Built-in YAML attack strategies (e.g. auto_edrag_rush, valk_spam)
│   ├── templates/               # Image templates for buttons, icons, digits, and heroes
│   └── *.json                   # Calibrated HUD coordinates (rois, buttons, slots)
│
├── cmd/                         # Developer tools, visual pickers, and diagnostics
│   ├── roi_picker/              # Browser-based ROI calibration utility (make roi-picker)
│   ├── pick_coords/             # Visual deploy point picker (make pick-coords)
│   ├── pick_army_slots/         # Army recipe slot calibrator (make pick-army-slots)
│   ├── see/                     # Terminal ASCII screen inspector (make see)
│   └── screendump/              # Screen capture, state classifier, and OCR inspector
│
├── web/                         # Modern React/TypeScript Wails GUI frontend
└── tools/                       # Dylib bundler, DMG packager, and calibration scripts
```

---

## ⚡ Performance Benchmarks

Measured on Apple Silicon (M-series) running BlueStacks at 860×732 / 160 DPI:

| Runtime Mode | RSS Footprint | Single-Core CPU |
|---|---|---|
| **Headless CLI (`bot_cli -perf`) — Idle** | **~25–40 MB** | **<1%** |
| **Headless CLI (`bot_cli -perf`) — Active Battle** | **~45–70 MB** | **~8–15%** |
| **Desktop GUI (Wails) — Idle** | ~60–85 MB | ~1–2% |
| **Desktop GUI (Wails) — Active Battle** | ~80–120 MB | ~12–20% |

See [`PERFORMANCE.md`](PERFORMANCE.md) for profiling details, capture cache invariants, and Mat pooling architecture.