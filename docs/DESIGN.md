# MyBot.run → Go Port Plan

## Goal
Port MyBot.run (Clash of Clans AutoIt bot) to Go for Apple Silicon Macs using BlueStacks Air via ADB.

## Constraints & Preferences
- Must run on Apple Silicon Macs
- Uses BlueStacks Air via ADB (already connected and working)
- Professional, super fast, efficient code approach
- GUI: design is secondary, can be worked on later
- Strategy format: YAML + per-unit formula coordinates
- Follow Go best practices throughout
- Go 1.22 installed and confirmed working

## Status

### Done
- [x] Project initialized: `coc-bot/` Go module (go 1.22, gocv, zerolog)
- [x] `internal/vision/vision.go` — Template matching (multi-scale, cached), PixelSearch, mat pool
- [x] `pkg/strategy/parser.go` + `yaml_parser.go` — YAML strategy parser
- [x] **Persistent ADB Transport** (`internal/adb/transport.go` + `internal/adb/client.go` + `internal/adb/types.go`)
  - Direct TCP connection to ADB server (port 5037) — no process spawning per command
  - Single persistent connection multiplexing all commands
  - Auto-reconnect on transport loss with automatic retry
  - Health tracking: avg capture ms, consecutive fails, total captures/errors
  - ~30%+ faster than process-spawn approach
- [x] **Game State Machine** (`internal/game/`)
  - `types.go` — GameState enum, TransitionAction, Clickable, Rectangle, Config structs
  - `context.go` — GameContext with RWMutex, state management, screen capture buffering
  - `state_graph.go` — Dijkstra shortest path
  - `classifier.go` — Pixel-based state detection (21 states incl. the
    post-boot splash chain: TapToContinue / Logo / NewsSplash)
  - `calibration.go` — Resolution-independent scaling
  - `navigator.go` — State-to-state navigation + interrupt handling
  - `recognizer.go` — ScreenHash, blur detection, contour-based element detection
  - `templates.go` — Template store with multi-scale matching
- [x] **Training System** (`internal/training/train.go`)
  - Army status reading (full army bar detection, slot counting)
  - Troop training queue executor with configurable delays
  - Resource reading (gold, elixir, dark elixir)
  - WaitForFullArmy with polling
- [x] **Attack System** (`internal/attack/attack.go`)
  - YAML strategy loading and parsing
  - Deploy order builder (troops grouped by slot)
  - Troop selection, drop execution, spell casting
  - Queen/Warden/CC activation
  - Red area detection via HSV color segmentation
  - Battle result reading (star detection)
  - End battle and return home sequences
- [x] **Bot Orchestrator** (`internal/bot/bot.go`)
  - Captures screen at 5Hz
  - Classifies state with 2-frame confirmation
  - Automatically trains army when not full
  - Automatically searches for match and attacks when army ready
  - Stats tracking (attack count, uptime, health)
  - Graceful shutdown with signal handling
- [x] **Config System** (`internal/config/config.go`)
  - Typed JSON config structs (Device, Training, Attack, Search, Debug)
  - Default values for all settings
  - LoadOrDefault pattern
- [x] **Assets**
  - `assets/strategies/default.csv` — BARCH strategy (280 troop capacity)
  - `assets/templates/` — Template matching storage
  - `assets/screenshots/` — Debug capture directory

### In Progress
- [ ] Search system (base filtering, weak base detection)

### Shipped since this plan was written
- [x] Wails GUI — React dashboard (0.2.0)
- [x] Attack stats + history — JSON-backed (`attack_history.json`), no SQLite

### Next Priority
1. **Search system** — Trophy/TH filtering, weak base detection, next-base button
2. **SQLite stats** — Record attack results, stars, loot, trophies, duration
3. **Wails GUI** — Status dashboard, strategy selector, manual controls

## Architecture

### Attack Flow
```
Main Village (5Hz capture loop)
  ├── Army full? → No → Navigate to Army Camp → Train queue → Return
  └── Army full? → Yes → Navigate to Battle
        → Find Match (tap search button)
        → Wait for battle state
        → Load CSV strategy
        → Analyze red area (HSV segmentation)
        → Deploy all troops by slot groups
        → End battle
        → Return home
        → Record stats
```

### ADB Layer (persistent transport)
```
Client (public API: CaptureToMat, Tap, Swipe, Shell)
  └── Transport (persistent TCP to ADB server)
        ├── connect() → TCP dial → CNXN handshake
        ├── setTransport() → host:transport:<device>
        ├── exec(service) → length-prefixed packet → OKAY/FAIL
        └── reconnect() on failure
```

### Game State Layer
```
Capture Loop (200ms ticker)
  └── Capture → Classify (pixel rules) → Confirm (2 frames) → Update State
        └── State change event → Check actions:
              ├── MainVillage + !ArmyFull → Training flow
              ├── MainVillage + ArmyFull → Attack flow
              ├── Splash states (TapToContinue / NewsSplash) → Auto-dismiss tap
              └── Interrupt states → Dismiss dialogs
```

### Resource usage (estimates)

Principled estimates from code analysis (frame sizing + mat pool + template
cache + capture cadence). Assume Apple Silicon Mac + BlueStacks at
860×732 / 160 DPI.

- **Frame**: full capture `860×732×3 ≈ 1.80 MB`; half-size Live View frame
  `430×366×3 ≈ 0.47 MB`.
- **ClashGO (Go) RSS**: ~60–90 MB idle (1 FPS), ~90–140 MB in active battle
  at 15 FPS. The idle bulk is the Wails/WebKit GUI harness, not the bot
  logic (~2–4 MB mats + ~1–3 MB template cache + small Live View buffer).
- **ClashGO CPU**: single-core; ~1–3% idle, ~15–25% at 15 FPS (per-frame
  vision work ~7–15 ms scales ~linearly with FPS). The bot also reports
  `cpu_time_sec` (absolute CPU seconds since start, device-independent and
  kernel-accurate via `getrusage`) so efficiency is comparable across
  machines without normalizing by core count. See
  [`PERFORMANCE.md`](PERFORMANCE.md#cpu-metric-absolute-time-not-a-percentage).
- **+ BlueStacks**: ~800 MB–1.2 GB idle, ~1.0–1.5 GB in battle; driven by
  the emulator + Android guest, largely independent of ClashGO's FPS.
- **Combined**: ~0.9–1.3 GB idle, ~1.1–1.7 GB at 15 FPS.

See [`PERFORMANCE.md`](PERFORMANCE.md#resource-usage-ram--cpu) for the full
table and a verification recipe.

## Technology Stack
- **ADB**: Persistent TCP transport to ADB server (no process spawning)
  - `exec:exec-out screencap` for raw RGBA captures (~120-150ms)
  - `shell:input tap/swipe/text/keyevent` for interaction
  - Auto-reconnect on transport loss with retry
- **Vision**: gocv (OpenCV 4.x) for template matching + red line detection
- **OCR**: none in the runtime — resource/loot reading is template + pixel
  based. A diagnostic-only Apple Vision OCR helper (`tools/ocr.swift`)
  exists for the text-based observability tooling; see
  [`OBSERVABILITY.md`](OBSERVABILITY.md).
- **Config**: Typed JSON structs (custom unmarshal, no external dep)
- **GUI**: Wails v2 + React (shipped)
- **Concurrency**: Goroutines + channels + atomic
- **State Machine**: Explicit GameState enum with 2-frame confirmation
- **Database**: none — stats + attack history are JSON files
- **Logging**: rs/zerolog (structured, low overhead)

## Project Structure
```
ClashGO/
├── go.mod / go.sum
├── main.go / cli.go / app.go        # Wails entry, CLI (build tag: cli), IPC + stats
├── internal/
│   ├── adb/                         # Persistent ADB transport, gestures, emulator bring-up
│   │   ├── client.go / transport.go / types.go / pinch.go / bootprobe.go / emulator_mac.go
│   │   └── *_test.go                # Tests (device-backed ones skip without a device)
│   ├── attack/                      # Strategy execution: planner, executor, red line, spells
│   │   ├── orchestrator.go          # Full-attack orchestration (DeployDynamicV2)
│   │   ├── deploy_planner.go / deploy_line.go / spell_deployer.go / executor.go
│   │   ├── redline.go / slot_manager.go / troop_counter.go / hero_manager.go / sweep.go
│   │   ├── verifier.go / rotation_state.go / live_count.go / ...
│   │   └── *_test.go
│   ├── bot/                         # Capture loop, boot orchestration, wall upgrades, CPU metric
│   │   ├── bot.go / bootorchestrator.go / bootprofile.go / boot_report.go / recovery.go
│   │   ├── wall_upgrade.go / asyncwriter.go / diagnostics.go / cputime.go
│   │   └── *_test.go
│   ├── game/                        # State machine: detection + navigation
│   │   ├── types.go / context.go / state_graph.go / classifier.go
│   │   ├── calibration.go           # Physical→reference (860×732) scaling
│   │   ├── navigator.go / chestdismiss.go / loot.go / recognizer.go / templates.go
│   │   └── *_test.go
│   ├── training/                    # Army-camp reading + training queue
│   ├── config/                      # Typed JSON config
│   ├── paths/                       # Asset-path resolution
│   ├── logger/                      # zerolog wiring
│   ├── updater/                     # GitHub Releases in-app updater
│   └── vision/                      # gocv template matching + mat pool
├── pkg/
│   ├── strategy/                    # YAML strategy parser
│   └── formula/                     # Per-unit deploy formula (design_attack output)
├── assets/
│   ├── strategies/                  # YAML strategies + formula JSON
│   ├── templates/                   # Template images for matching
│   └── *.json                       # Picked ROIs (wall upgrade, chest, battle loot)
├── cmd/                             # Helpers: attack_record, capture_template, design_attack,
│                                    #          release_manifest, test_wall_upgrade, screendump,
│                                    #          classify_probe, result_probe, swipe_probe
├── web/                             # Wails React GUI
└── tools/                           # picker.py + calibration scripts + observe.sh/ocr.swift
```

## Relevant AutoIt Source Files (reference)
- `MyBot.run.au3` — Main entry (~1665 lines)
- `COCBot/functions/Android/Android.au3` — ADB layer (~5046 lines)
- `COCBot/functions/Image Search/imglocAuxiliary.au3` — imgloc API
- `COCBot/functions/Image Search/QuickMIS.au3` — Main image search interface
- `COCBot/functions/Pixels/isInsideDiamond.au3` — Diamond containment
- `COCBot/functions/Pixels/_ColorCheck.au3` — Color matching
- `COCBot/functions/Attack/RedArea/_GetRedArea.au3` — Red line detection
- `COCBot/functions/Attack/Attack Algorithms/AttackFromCSV.au3` — CSV attacks
- `COCBot/functions/CreateArmy/TrainSystem.au3` — Training orchestrator
- `CSV/` — CSV attack strategy files (keep format)