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
- **Self-healing boot sequence**: the bot auto-dismisses the post-boot
  splash chain ("ТАР!" tap-to-continue → castle logo → news splash)
  instead of force-restarting in a loop, so unattended runs survive game
  relaunches.
- **Unattended-run resilience**: if the emulator dies mid-session the bot
  escalates through transport reconnect → adb-server reset → BlueStacks
  relaunch instead of spinning; a bad frame or missing asset can't crash
  the process (panic guards + nil-template fallback); and
  `tools/run_bot_keepalive.sh` respawns the bot ~10s after any exit so it
  keeps farming unattended.
- **Text-based observability**: `./tools/observe.sh` captures the emulator
  screen, classifies it with the bot's real vision layer, and OCRs the
  on-screen text — a terminal-only "eye view" for debugging without a
  GUI. See [`docs/OBSERVABILITY.md`](docs/OBSERVABILITY.md).
- **Replayable attacks**: `make attack-record` records a deploy you perform on the emulator; `make attack-replay` re-fires that JSON on the device with classification + extras. Useful for sharing working attacks without re-engineering.
  - In-app updater: New releases ship through GitHub Releases. Once a version is published, ClashGO's UI shows a banner; clicking it downloads + verifies (SHA256), then opens Finder so you can drag-replace. No new servers, no manual checks. Skip / later are honored.

### 🛠️ How to use
1. **Emulator**: Set your emulator (like BlueStacks) to **860x732** resolution and **160 DPI**.
2. **Config**: Point `config.json` to your ADB device.
3. **Run**:
   - CLI: `make build-cli && ./build/bin/bot_cli`
   - GUI: `make build-gui` and run `build/bin/ClashGO.app`

### 💾 Resource usage (estimates)

All numbers below are **principled estimates from code analysis** (frame
sizing, mat pool, template cache, capture-loop cadence), not live
measurements. They assume an **Apple Silicon Mac**, **BlueStacks at
860×732 / 160 DPI**, and the bot running at the configured capture rate.
The recipe in [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md#how-to-verify-on-your-machine)
validates them on your machine.

**Frame math (860×732, RGB):**
- Full capture frame: `860 × 732 × 3 ≈ 1.80 MB`
- Half-size frame (Live View JPEG encode): `430 × 366 × 3 ≈ 0.47 MB`

| Scenario | ClashGO (Go) RSS | ClashGO CPU¹ | + BlueStacks RSS² | Combined RSS (est.) |
|----------|----------------:|-------------:|------------------:|--------------------:|
| **Idle / UI only** (1 FPS capture) | ~60–90 MB | ~1–3% (1 core) | ~800 MB–1.2 GB | ~0.9–1.3 GB |
| **Active battle @ 15 FPS** | ~90–140 MB | ~15–25% (1 core) | ~1.0–1.5 GB | ~1.1–1.7 GB |

¹ CPU is single-core; the bot is largely single-threaded per capture frame
(classify + template match + tap). Higher FPS = proportionally more CPU.
At 15 FPS the per-frame vision work (~7–15 ms) consumes ~15–25% of one core.
The bot also reports **`cpu_time_sec`** — absolute CPU time since process
start — which is the device-independent metric: it means the same on an M1 or
an M3 Max, so you can compare efficiency across machines without normalizing
by core count. The "% CPU" shown in the UI is derived by multiplying the
per-core fraction (`cpu_cores`) by the host's logical core count; treat that
number as host-relative only.
² BlueStacks footprint is driven by the emulator + Android guest, not the
bot. It scales with emulator window size / DPI and the guest's own memory
pressure, not with ClashGO's FPS.

**Where the bot's RAM goes:**
- Capture + working Mats (mat pool): ~2–4 MB retained (serial capture keeps
  only 1–2 mats per size alive).
- `ScaledTemplateCache` (6 classifier rules × several scales): ~1–3 MB.
- Live View base64 frame buffer (`lastFrame`): a few hundred KB.
- Wails/WebKit GUI harness: the bulk of the idle ~60–90 MB is the embedded
  browser, not the Go bot logic.

ClashGO itself is tiny; the dominant memory cost when running is almost
always **BlueStacks**, not the bot.

### 🚢 Releasing a new version

Updates are powered by GitHub Releases — no extra infrastructure.

1. Bump `productVersion` in `wails.json` (e.g. `0.3.1`).
2. Commit + push, then tag it:

   ```sh
   git tag v0.3.1 && git push origin v0.3.1
   ```

   Pushing a `v*` tag runs the **Release** workflow
   (`.github/workflows/release.yml`), which builds the macOS zip, the
   DMG, and `latest.json` on a fresh runner and publishes them to a
   GitHub Release automatically. No manual upload, no secrets — the
   workflow uses GitHub's built-in `GITHUB_TOKEN`, and the app itself
   checks the public GitHub API unauthenticated.
3. Existing users get an auto-popping update window within 6h (or on
   next launch) offering one-click *Update & Restart*.

Manual fallback (no CI):

1. `make release VERSION=0.3.1` — produces the zip, the DMG,
   and `latest.json`.
2. Publish a GitHub release tagged `v0.3.1`, and attach:
   - `ClashGO-v0.3.1-macOS.zip`
   - `latest.json`

### 🤝 HELP WANTED (Porting to Windows)
Right now, this is heavily tested on macOS. I'd love some help **porting/testing this for Windows**. If you're a dev and want to help me make this not-just-a-Mac-thing, open a PR or hit me up!

Also, if you find bugs (you will), just open an issue.

### 📄 License
MIT. Do whatever you want with it.
