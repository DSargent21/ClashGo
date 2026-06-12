# ClashGO

A professional, high-performance Go port of the Clash of Clans AutoIt bot, optimized for Apple Silicon Macs using Android Debug Bridge (ADB).

---

## 🛡️ Anti-Ban & Safety
This project is designed with safety as a priority. All diagnostic and debug data that could identify your account or base layout is excluded from source control.
- **No Identifying Data**: Screenshots of player bases and diagnostic JSONs are strictly git-ignored.
- **Local Execution**: All vision processing happens on your machine.
- **Human-like Interaction**: Randomized delays and confirmation-backed state transitions.

---

## 🚀 Key Features

- **Persistent ADB Transport**: Low latency multiplexed TCP connection to ADB server (screencaps < 120ms).
- **Vision Engine**: Real-time GoCV/OpenCV 4.x vision pipelines for state classification and red-zone segmentation.
- **Dynamic State Machine**: Confirmation-backed state graph with Dijkstra-based shortest-path traversal.
- **Army Training Queue**: Automated troop trainer with status recognition and resource extraction.
- **Strategy Attack Executor**: CSV and YAML deployment parser with smart pacing and hero abilities.
- **GUI Remote Dashboard**: Wails v2 + React dashboard for real-time status and configuration.

---

## 📂 Project Structure

- `cmd/`: Application entry points.
- `internal/`: Core logic (ADB, Bot, Game State, Vision).
- `pkg/`: Publicly reusable packages (Strategy parser).
- `assets/`: UI templates and attack strategies.
- `docs/`: Design documents and architecture overview.
- `tools/`: Development and calibration utilities.
- `web/`: React frontend for the dashboard.

---

## 🛠️ Installation & Setup

1. **Go 1.25+**: [Install Go](https://go.dev/doc/install).
2. **OpenCV 4.x**: 
   ```bash
   brew install opencv pkg-config
   ```
3. **Verify Configuration**:
   ```bash
   pkg-config --libs --cflags opencv4
   ```

---

## 🏃 Running the Bot

### CLI Mode (Headless)
```bash
go build -tags cli -o clashgo .
./clashgo
```

### GUI Mode (Desktop)
```bash
wails dev
```

---

## 📄 License
Distributed under the GNU General Public License v3.0. See `License.txt` for more information.
