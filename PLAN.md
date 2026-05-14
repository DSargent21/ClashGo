# MyBot.run → Go Port Plan

## Goal
Port MyBot.run (Clash of Clans AutoIt bot) to Go for Apple Silicon Macs using BlueStacks Air via ADB.

## Constraints & Preferences
- Must run on Apple Silicon Macs
- Uses BlueStacks Air via ADB (already connected and working)
- Professional, super fast, efficient code approach
- GUI: design is secondary, can be worked on later
- Keep existing CSV attack strategy format
- Follow Go best practices throughout
- Go 1.22 installed and confirmed working

## Status

### Done
- [x] Project initialized: `coc-bot/` Go module (go 1.22, gocv, zerolog, go-sqlite3)
- [x] `internal/geo/geo.go` — Point, Rect, Diamond geometry types
- [x] `internal/vision/vision.go` — Template matching, multi-scale matching, PixelSearch, MultiPixelSearch, FindRedArea, IsInsideDiamond
- [x] `pkg/strategy/parser.go` — CSV attack strategy parser
- [x] **Persistent ADB Transport** (`internal/adb/transport.go` + `internal/adb/client.go` + `internal/adb/types.go`)
  - Direct TCP connection to ADB server (port 5037) — no process spawning per command
  - Single persistent connection multiplexing all commands
  - Auto-reconnect on transport loss with automatic retry
  - Health tracking: avg capture ms, consecutive fails, total captures/errors
  - ~30%+ faster than process-spawn approach
- [x] **Game State Machine** (`internal/game/`)
  - `types.go` — GameState enum, TransitionAction, Clickable, Rectangle, Config structs
  - `context.go` — GameContext with RWMutex, state management, screen capture buffering
  - `state_graph.go` — Dijkstra shortest path + JSON persistence
  - `classifier.go` — Pixel-based state detection (13+ states)
  - `calibration.go` — Resolution-independent scaling
  - `navigator.go` — State-to-state navigation + interrupt handling
  - `explorer.go` — Auto-mapping of game states
  - `recognizer.go` — ScreenHash, blur detection, contour-based element detection
  - `templates.go` — Template store with multi-scale matching
- [x] **Training System** (`internal/training/train.go`)
  - Army status reading (full army bar detection, slot counting)
  - Troop training queue executor with configurable delays
  - Resource reading (gold, elixir, dark elixir)
  - WaitForFullArmy with polling
- [x] **Attack System** (`internal/attack/attack.go`)
  - CSV strategy loading and parsing
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
- [ ] SQLite stats (migrate AttackStats schema)
- [ ] Wails GUI

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
        ├── execRaw(service) → binary response
        └── reconnect() on failure
```

### Game State Layer
```
Capture Loop (200ms ticker)
  └── Capture → Classify (pixel rules) → Confirm (2 frames) → Update State
        └── State change event → Check actions:
              ├── MainVillage + !ArmyFull → Training flow
              ├── MainVillage + ArmyFull → Attack flow
              └── Interrupt states → Dismiss dialogs
```

## Technology Stack
- **ADB**: Persistent TCP transport to ADB server (no process spawning)
  - `exec:exec-out screencap` for raw RGBA captures (~120-150ms)
  - `shell:input tap/swipe/text/keyevent` for interaction
  - Auto-reconnect on transport loss with retry
- **Vision**: gocv (OpenCV 4.x) for template matching + red line detection
- **OCR**: Tesseract 5 via CGO (for resource reading, army count OCR) — planned
- **Config**: Typed JSON structs (custom unmarshal, no external dep)
- **GUI**: Wails v2 (planned)
- **Concurrency**: Goroutines + channels + atomic
- **State Machine**: Explicit GameState enum with 2-frame confirmation
- **Database**: mattn/go-sqlite3 (planned)
- **Logging**: rs/zerolog (structured, low overhead)

## Project Structure
```
coc-bot/
├── go.mod / go.sum
├── main.go                          # CLI entry, signal handling, bot startup
├── internal/
│   ├── adb/
│   │   ├── client.go               # Public ADB API (CaptureToMat, Tap, Swipe...)
│   │   ├── transport.go            # Persistent TCP transport to ADB server
│   │   ├── types.go                # Logger interface, Option, Health
│   │   └── transport_test.go       # Tests + benchmarks (skipped, real device)
│   ├── bot/bot.go                  # Bot orchestrator: capture loop + action dispatch
│   ├── game/                        # Game state machine
│   │   ├── types.go               # GameState, TransitionAction, Clickable, Config
│   │   ├── context.go             # GameContext (shared state hub, RWMutex)
│   │   ├── state_graph.go         # Dijkstra graph, JSON persistence
│   │   ├── classifier.go          # Pixel-based state detection (13+ states)
│   │   ├── calibration.go         # Resolution scaling
│   │   ├── navigator.go           # State navigation + interrupt handling
│   │   ├── explorer.go            # Auto state mapping
│   │   ├── recognizer.go          # ScreenHash, blur, contours
│   │   ├── templates.go           # Template matching store
│   │   └── doc.go                 # Package documentation
│   ├── training/train.go          # Army camp reading, training queue, resource OCR
│   ├── attack/attack.go            # Strategy execution, red area, drop algorithms
│   ├── search/                     # Search system (TODO)
│   ├── config/config.go           # Typed JSON config, all settings
│   └── stats/                      # SQLite stats (TODO)
├── pkg/
│   └── strategy/parser.go          # CSV attack strategy parser
└── assets/
    ├── strategies/default.csv      # BARCH attack strategy
    ├── templates/                  # Template images for matching
    └── screenshots/                # Debug capture output
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