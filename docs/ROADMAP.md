# ClashGO Roadmap 🗺️

Vision: **High-performance, unattended Clash of Clans automation — village economy, farming, and progression — on desktop.**

ClashGO provides a native vision engine that interprets game state and acts through persistent ADB in real time (built for Apple Silicon, OpenCV, zero-Rosetta translation, and self-healing recovery).

---

## 🚀 Shipped Milestones

### Phase 1 — Reliable Farming Core (`v0.1` – `v0.9`) ✅
- **Persistent ADB Engine**: Sub-millisecond direct socket transport (`exec:/system/bin/screencap`), bypassing slow process-spawn loops.
- **Computer Vision & HUD Calibration**: Resolution-independent calibration, multi-scale template matching, pooled Mats, and OCR result verification.
- **Battle Execution & Orchestration**: YAML strategy compositions, geometric deploy formulas, 4-sided rapid deployment, centered spells, and hero orchestration.
- **Self-Healing Watchdogs**: Multi-stage splash auto-dismissal ("TAP!" prompt, loading logo, event news dialogs) and emulator crash recovery ladders.
- **Interactive Tooling**: Browser-based calibration suite (`make roi-picker`, `make pick-coords`, `make pick-army-slots`).
- **1-Click Auto-Updater**: In-app GitHub release detection, SHA-256 verification, and atomic in-place bundle updates.

### Phase 2 — Village Economy & Progression (`v1.0.0`) ✅
- **Automated Village Upgrades**: Build Overview Menu 2.0 automation for buildings, heroes, defenses, resources, and traps.
- **Granular Upgrade Policies**: `heroes_first`, `cheapest_first`, `most_expensive_first`, builder reservation limits, and per-resource spend caps.
- **Laboratory Research**: Automated category and priority sorting (`troops_first`, `spells_first`, `siege_first`, `lab_allow_de`).
- **Hero Pets Manager**: Automated priority queue progression across all hero pets.
- **Wall Upgrade Engine**: Resource-targeted wall upgrading (`gold`, `elixir`, or `both`) with kinetic settle handling.
- **Distribution & Footprint Optimization**: 65% DMG size reduction via `-dead_strip_dylibs`, Mach-O symbol stripping, and LZMA compression.

---

## 🎯 Active Priorities (Phase 3 — Unattended 24/7)

### Shipped — unattended loop closure
- [x] **Training Queue** (`internal/bot/training.go`): an empty attack panel now queues the saved recipe through controls the bot already proves on the frame (EDIT ARMY, recipe card, sheet action). It runs while the village idles instead of waiting for a human.
- [x] **Halt model** (`internal/bot/runtime_state.go`): scheduler break, full storages, and empty army are one published verdict (`BotStats.halted_reason`), the watchdog no longer treats a deliberate pause as a hang, and the scheduler stops input and capture entirely during a break.
- [x] **Durable pacing** (`session_state.json`): hourly/daily attack counters survive restarts, so a keepalive respawn cannot reset the daily cap.
- [x] **Safe pacing defaults**: new installs start with an 18 h active window, 55–95 min sessions, 13–27 min game-stopped breaks, 8 attacks/h and 50/day.
- [x] **Personal Break handling**: the dialog is OCR'd on unclassified frames and converted into a scheduler break instead of a restart loop.
- [x] **LaunchAgent** (`make daemon-install`): LaunchAgent + `caffeinate` wrapper so the farm survives reboot and sleep, with desktop notifications after repeated crashes.

### P1 — Farming Intelligence & Base Filtering
- [ ] **Dead-Base / Collector Detection**: Distinguish between full outside collectors and active core storages to maximize loot efficiency.
- [ ] **Early Surrender Rules**: Automatic surrender if battle achieves target resource threshold before timer expiration.
- [ ] **Hero Recovery Wait**: Idle village wait if designated heroes are regenerating health (the bot currently attacks without them).
- ~~Town Hall Level Filtering~~ — dropped: the search screen exposes no Town Hall reading to filter on, so the config and CLI knobs that claimed it were removed rather than left inert.

### P2 — Magic items
- [ ] **Book / hammer spending** on the longest running timer. Not automated yet: it needs calibrated magic-item UI coordinates and a spend ceiling before it can run unattended.

---

## 🌐 Future Horizons (Phase 4 — Platform & Integration)

### Multi-Instance & Remote Control
- [ ] **Multi-Instance Supervisor**: Manage multiple concurrent emulator instances and accounts from a single dashboard.
- [ ] **Remote Notifications**: Discord and Telegram webhooks for attack milestones, session summaries, and resource milestones.
- [ ] **Windows Port**: Cross-platform support for Windows-based Android emulators.
- [ ] **Community Strategy Sharing**: Import and export community-contributed YAML attack strategies and drop formulas.
