# ClashGO ⚔️

This is **ClashGO**, a super lightweight Clash of Clans bot written in Go.

I basically vibe-coded this in like 3 days because I wanted a bot that actually worked on my Mac (Apple Silicon). It's built from scratch using Go and OpenCV.

### ⚠️ WARNING: It's Buggy
Look, I made this fast. It's rough around the edges, probably has bugs, and might crash. Use it at your own risk.

### 🚀 Why it's cool
- **Mac Native**: Works great on Apple Silicon.
- **Fast**: Uses a persistent ADB connection, so screen captures and taps are instant.
- **Lightweight**: Just a single binary, with a recent perf audit cutting battle-state CPU ~10–20% and transient RSS ~10–20 MB. See [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md) for the per-change breakdown and how to verify on your machine.
- **Customizable strategies**: Drop a YAML strategy + matching `formula.json` (per-unit deploy coords) into `assets/strategies/` and the bot deploys each unit where you want. See `assets/strategies/auto_edrag_rush.yaml` + `assets/strategies/auto_edrag_rush_formula.json` for an end-to-end example (Balloon + EDrag + Rage + Ice).
- **Per-corner formula workflow**: `target_edge: "Random"` (the default in `auto_edrag_rush.yaml`) picks a random corner per attack. `cmd/design_attack -live -corner BR|BL|TR|TL` authors the per-corner deploy coords; per-corner overrides in `formula.corner_overrides[<CORNER>]` are used as-authored (the mirror is only a fallback). See [`docs/formula-authoring.md`](docs/formula-authoring.md) for the full walkthrough.
- **Replayable attacks**: `make attack-record` records a deploy you perform on the emulator; `make attack-replay` re-fires that JSON on the device with classification + extras. Useful for sharing working attacks without re-engineering.
- **In-app updater**: New releases ship through GitHub Releases. Once a version is published, ClashGO's UI shows a banner; clicking it downloads + verifies (SHA256), then opens Finder so you can drag-replace. No new servers, no manual checks. Skip / later are honored.

### 🛠️ How to use
1. **Emulator**: Set your emulator (like BlueStacks) to **860x732** resolution and **160 DPI**.
2. **Config**: Point `config.json` to your ADB device.
3. **Run**:
   - CLI: `make build-cli && ./build/bin/bot_cli`
   - GUI: `make build-gui` and run `build/bin/ClashGO.app`

### 🚢 Releasing a new version

Updates are powered by GitHub Releases — no extra infrastructure.

1. Bump `productVersion` in `wails.json` (e.g. `0.2.0-beta`).
2. `make release VERSION=0.2.0-beta` — produces the zip, the DMG,
   and `latest.json`.
3. Publish a GitHub release tagged `v0.2.0-beta`, and attach:
   - `ClashGO-v0.2.0-beta-macOS.zip`
   - `latest.json`
4. Existing users get a banner within 6h (or on next launch).

### 🤝 HELP WANTED (Porting to Windows)
Right now, this is heavily tested on macOS. I'd love some help **porting/testing this for Windows**. If you're a dev and want to help me make this not-just-a-Mac-thing, open a PR or hit me up!

Also, if you find bugs (you will), just open an issue.

### 📄 License
MIT. Do whatever you want with it.
