# ClashGo: Vanguard CoC Bot

A professional, high-performance Go port of the Clash of Clans AutoIt bot, optimized for Apple Silicon Macs using Android Debug Bridge (ADB).

---

## Key Features

- **Persistent ADB Transport Layer**: Low latency multiplexed TCP connection to ADB server (screencaps in under 120ms).
- **Vision Engine**: Real-time GoCV/OpenCV 4.x vision pipelines for pixel-based state classification, template matching, and red-zone (obstacle border) segmentation.
- **Dynamic State Machine**: Confirmation-backed state graph with Dijkstra-based shortest-path traversal to navigate between game states automatically.
- **Army Training Queue**: Automated troop trainer with delay controls, army status recognition, and resource count extraction.
- **Strategy Attack Executor**: CSV-driven troop deployment parser with drop pacing, hero auto-abilities, and smart spacing.
- **Web-enabled GUI Remote Dashboard**: Built with Wails v2 and Echo framework for interactive status reporting and dashboard-based remote configurations.

---

## Repository Structure

```
.
├── app.go                      # Wails remote bridge & local Echo HTTP server
├── cli.go                      # CLI command launcher (run with -tags cli)
├── main.go                     # Wails GUI bootstrapper
├── internal/
│   ├── adb/                    # TCP ADB client transport (multiplexed socket layer)
│   ├── attack/                 # Attack algorithms, deploy queues, and obstacle border checks
│   ├── bot/                    # Bot orchestrator: ticks state evaluation loop @ 5Hz
│   ├── config/                 # Typed configurations load/defaults
│   ├── game/                   # Classifier, calibration, Dijkstra navigator, and template store
│   └── training/               # Army monitoring and training sequences
├── pkg/
│   └── strategy/               # Attack strategy CSV parser
├── scripts/                    # Python and Go utility tools
├── assets/
│   ├── strategies/             # BARCH CSV deployment files
│   └── templates/              # Core templates required for vision matching
└── web/                        # React/TS Frontend code for the GUI Dashboard
```

---

## Installation & Prerequisites

1. **Go Toolchain**: Make sure Go 1.22+ or newer is installed on your Mac.
2. **OpenCV Dependencies**:
   Install OpenCV via Homebrew:
   ```bash
   brew install opencv pkg-config
   ```
3. **GoCV Binding**:
   Verify OpenCV is discovered by `pkg-config`:
   ```bash
   pkg-config --libs --cflags opencv4
   ```

---

## Building and Running

### CLI Mode (Recommended for Server / Headless)
Build and run with the `cli` build tag:
```bash
go build -tags cli -o coc-cli cli.go app.go
./coc-cli
```

### GUI Mode (Wails desktop interface)
Run locally in development mode:
```bash
wails dev
```
To bundle for production:
```bash
wails build
```

---

## Configuration

Settings are parsed from `config.json` in the working directory. A default config file is generated automatically if one does not exist.

Key configuration nodes:
- `Device`: ADB targets and scale resolution mappings.
- `Search`: Hard thresholds for Gold, Elixir, and Dark Elixir search filters.
- `Training`: Pacing, training order schedules, and wait loops.
- `Debug`: Local screenshot caching and visualization tools.
