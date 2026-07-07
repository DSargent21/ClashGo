# ClashGO ⚔️

This is **ClashGO**, a super lightweight Clash of Clans bot written in Go.

I basically vibe-coded this in like 3 days because I wanted a bot that actually worked on my Mac (Apple Silicon). It's built from scratch using Go and OpenCV.

### ⚠️ WARNING: It's Buggy
Look, I made this fast. It's rough around the edges, probably has bugs, and might crash. Use it at your own risk. 

### 🚀 Why it's cool
- **Mac Native**: Works great on Apple Silicon.
- **Fast**: Uses a persistent ADB connection, so screen captures and taps are instant.
- **Lightweight**: Just a single binary.
- **Customizable strategies**: Drop a YAML strategy + matching `formula.json` (per-unit deploy coords) into `assets/strategies/` and the bot deploys each unit where you want. See `assets/strategies/auto_edrag_rush.yaml` + `assets/strategies/auto_edrag_rush_formula.json` for an end-to-end example (Balloon + EDrag + Rage + Ice).
- **Replayable attacks**: `make attack-record` records a deploy you perform on the emulator; `make attack-replay` re-fires that JSON on the device with classification + extras. Useful for sharing working attacks without re-engineering.

### 🛠️ How to use
1. **Emulator**: Set your emulator (like BlueStacks) to **860x732** resolution and **160 DPI**.
2. **Config**: Point `config.json` to your ADB device.
3. **Run**: 
   - CLI: `go build -tags cli -o clashgo . && ./clashgo`
   - GUI: `wails dev`

### 🤝 HELP WANTED (Porting to Windows)
Right now, this is heavily tested on macOS. I'd love some help **porting/testing this for Windows**. If you're a dev and want to help me make this not-just-a-Mac-thing, open a PR or hit me up!

Also, if you find bugs (you will), just open an issue.

### 📄 License
MIT. Do whatever you want with it.
