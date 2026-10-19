# ClashGO ⚔️

[![Release](https://img.shields.io/github/v/release/DSargent21/ClashGo?color=blue&label=release)](https://github.com/DSargent21/ClashGo/releases)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go)](https://golang.org)
[![Platform](https://img.shields.io/badge/Platform-macOS%20(Apple%20Silicon)-black?logo=apple)](https://github.com/DSargent21/ClashGo)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

High-performance, lightweight Clash of Clans automation engine written in Go and OpenCV. Built natively for macOS (Apple Silicon) with sub-millisecond persistent ADB transport, resilient computer vision, self-healing recovery, a sleek desktop GUI, and a headless CLI.

---

## ⚡ Key Highlights

- **Native Apple Silicon Architecture**: Compiled directly for ARM64 macOS — zero Rosetta translation, minimal memory footprint, and low thermals.
- **Persistent ADB Engine**: Bypasses slow `adb shell` execution loops using a raw persistent socket connection (`exec:/system/bin/screencap`), reducing capture latency to ~30–50ms.
- **Resilient Computer Vision**: Multi-scale template matching and OCR powered by OpenCV and Apple Vision, with built-in asset pooling to eliminate memory thrash.
- **Self-Healing State Watchdogs**: Auto-dismisses post-boot splash chains ("TAP!" continue $\to$ castle logo $\to$ news dialog) and recovers gracefully from disconnected emulators, network stalls, and UI interruptions.
- **Customizable Strategy Formulas**: Author drop orders, deploy angles, and timings via YAML strategies (`assets/strategies/`) paired with geometric formula JSONs (`pkg/formula/`).
- **Interactive Visual Calibration**: Launch browser-based interactive tools (`make roi-picker`, `make pick-coords`) to drag boxes, crop templates, and calibrate HUD coordinates live on emulator frames.
- **1-Click In-App Updater**: Automated GitHub Releases integration. Detects new versions, verifies SHA-256 signatures, applies detached hot-swaps, and relaunches with zero manual re-installation.
- **Headless CLI (`bot_cli`) & Desktop GUI (Wails)**: Run the full bot engine headlessly inside `tmux` for 24/7 background farming (~25–40 MB RSS), or launch the modern React/Wails UI for visual analytics and live control.

---

## 🚀 Quickstart

### Prerequisites
1. **macOS** (Apple Silicon M1/M2/M3/M4 recommended).
2. **Android Emulator** (e.g., BlueStacks) configured to:
   - Resolution: **860 × 732**
   - DPI: **160 DPI**
   - ADB enabled on `127.0.0.1:5555` (default).

### 1. Download & Install (Desktop GUI)
Download the latest `ClashGO.dmg` from [GitHub Releases](https://github.com/DSargent21/ClashGo/releases/latest), drag `ClashGO.app` into `/Applications`, and launch.

### 2. Headless CLI (`bot_cli`)
For maximum efficiency and 24/7 background servers:

```bash
# Build the standalone binary
make build-cli

# Print version and git commit
./build/bin/bot_cli -v

# Run with performance optimizations and custom loot filters
./build/bin/bot_cli -perf -gold 600000 -elixir 600000 -min-de 3000
```

See [`docs/CLI.md`](docs/CLI.md) for full flag documentation (`-perf`, `-once`, `-deploy-only`, `-devices`, `-strategies`).

---

## 🛠️ Tooling & Calibration Suite

ClashGO includes a suite of developer and calibration utilities in `cmd/`:

| Command / Tool | Description |
|----------------|-------------|
| `make roi-picker` | Drag-and-drop web ROI selector (`http://127.0.0.1:8795`) for buttons, slots, and templates |
| `make pick-coords` | Visual deploy point picker (`http://127.0.0.1:8791`) for calibrating base boundary lines |
| `make pick-army-slots` | Calibrate clickable boxes for army recipe slots on live frames |
| `make attack-verify` | Renders attack plans (deploy taps, spells, red zone) onto emulator frame without tapping |
| `make attack-record` | Macro recorder: capture clicks on emulator to JSON |
| `make attack-replay` | Macro replayer: fire recorded attack JSON at cadence |
| `make see` | Terminal ASCII screen inspector and vision debug eye |
| `./tools/observe.sh` | Terminal capture + classifier + Apple Vision OCR dump |

---

## 💾 Resource Usage & Efficiency

All metrics reflect Apple Silicon runs with BlueStacks at 860×732 / 160 DPI:

| Scenario | ClashGO RSS | ClashGO CPU¹ | + BlueStacks RSS² | Combined Footprint |
|----------|------------:|-------------:|------------------:|-------------------:|
| **CLI Headless (Idle / UI only)** | **~25–40 MB** | **<1% (1 core)** | ~800 MB–1.2 GB | **~0.8–1.2 GB** |
| **CLI Headless (Active battle, `-perf`)** | **~45–70 MB** | **~8–15% (1 core)** | ~1.0–1.5 GB | **~1.0–1.6 GB** |
| **GUI (Idle / Dashboard)** | ~60–85 MB | ~1–2% (1 core) | ~800 MB–1.2 GB | ~0.9–1.3 GB |
| **GUI (Active battle)** | ~80–120 MB | ~12–20% (1 core) | ~1.0–1.5 GB | ~1.1–1.6 GB |

¹ Single-core CPU percentage. During battle, vision classification runs at throttled intervals to minimize CPU usage while guaranteeing fast result detection.
² BlueStacks memory footprint is governed by the Android guest OS and emulator settings.

See [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md) for profiling details and optimization benchmarks.

---

## 📚 Documentation Index

- **[Architecture & Design](docs/DESIGN.md)**: State machine, vision pipelines, and subsystem layout.
- **[CLI Reference](docs/CLI.md)**: Headless CLI flags, execution modes, and environment variables.
- **[Formula Authoring Guide](docs/formula-authoring.md)**: Designing custom deployment patterns and corner overrides.
- **[Performance & Optimization](docs/PERFORMANCE.md)**: Memory pooling, capture caching, and profiling guide.
- **[Observability & Debugging](docs/OBSERVABILITY.md)**: ASCII visualization, tap traces, and OCR tools.
- **[Resolution & Scaling](docs/RESOLUTION.md)**: Mathematical models for resolution independence and DPI scaling.
- **[ROI Picking Guide](docs/PICKING.md)**: Calibrating game assets and buttons with interactive visual pickers.
- **[Roadmap](docs/ROADMAP.md)**: Product milestones, tactical backlog, and architectural vision.

---

## 🚢 Release Workflow

Releases are built automatically via GitHub Actions:

```bash
# 1. Update productVersion in wails.json
# 2. Tag and push
git tag v1.0.1
git push origin v1.0.1
```

GitHub Actions compiles the universal Go binaries, packages the self-contained DMG (`ULMO` LZMA compression) and zip bundle, emits `latest.json`, and publishes the release assets automatically.

---

## 🤝 Contributing

Contributions, issues, and PRs are welcome!
- Testing or porting to **Windows** (see roadmap).
- Authoring new troop and spell strategies in `assets/strategies/`.
- Reporting bugs or edge cases with logs from `logs/`.

---

## 📄 License

Distributed under the **MIT License**. See [LICENSE](LICENSE) for details.
