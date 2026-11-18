# ClashGO CLI Guide ⚔️

`bot_cli` is the headless, ultra-lightweight CLI version of ClashGO.

It runs the full bot engine without WebKit, web assets, or GUI overhead. Ideal for servers, headless machines, background tmux/screen sessions, or users prioritizing maximum performance and minimal resource usage.

---

## ⚡ Quick Start

### 1. Install the Prebuilt Binary
```bash
curl -fsSL https://raw.githubusercontent.com/DSargent21/ClashGo/main/tools/install.sh | bash -s -- --cli
```

Installs the self-contained release (bundled OpenCV dylibs + `assets/`) and writes a launcher to `~/.local/bin/bot_cli`, so it runs from any directory without a source checkout or Homebrew. Override the locations with `--bin-dir <dir>` / `--data-dir <dir>`, install a specific tag with `--version v1.1.1`, and see `--help` for the rest. Updates keep your edited `assets/` tree unless `--force` is passed, and a broken archive rolls back to the previous binary.

Downloads are verified against the release `SHA256SUMS` (fetched from the same GitHub release over HTTPS). That catches corrupted or mirror-swapped files; it is not a signature over the release.

### 2. Build from Source (alternative)
```bash
make build-cli
```
Binary output: `build/bin/bot_cli`

### 3. Verify ADB Device Connection
```bash
./build/bin/bot_cli -devices
```

### 4. Check Available Strategies
```bash
./build/bin/bot_cli -strategies
```

### 5. Run Farming with Maximum Performance
```bash
./build/bin/bot_cli -perf -gold 600000 -elixir 600000
```

---

## 🚀 Performance Mode (`-perf`)

For unattended farming, always pass `-perf`:
- **Lean attack pipeline**: Takes 1 troop-bar frame before first drop instead of 3.
- **No artifact dumps**: Skips generating post-battle evidence PNGs to disk.
- **Zero guest animation**: Zeros Android window and transition animation scales via ADB.
- **Vision cache**: Reuses classification verdicts on unchanged frames (`skip_unchanged_classify`).
- **Coalesced captures**: Shares screen captures between concurrent observers.
- **No debug screenshot I/O**: Disables saving large PNG screenshots to disk.

Combined with running BlueStacks at **860×732 / 160 DPI**, this yields the lowest possible CPU and memory footprint.

---

## 🔔 Webhook Notifications

An unattended farm is invisible unless it reports out. Add a `notify` block to `config.json` and every finished attack, storage halt and scheduler pause/resume is posted to Discord, Telegram or any receiver that accepts a JSON `text` field.

```json
"notify": {
  "enabled": true,
  "format": "discord",
  "webhook_url": "https://discord.com/api/webhooks/..."
}
```

Telegram instead:

```json
"notify": {
  "enabled": true,
  "format": "telegram",
  "telegram_token": "123456:ABC...",
  "telegram_chat_id": "987654"
}
```

- `format: generic` posts `{"text","kind","at"}` to `webhook_url` (Slack, ntfy, a self-hosted receiver).
- Delivery is fire-and-forget: a 16-message queue on one worker goroutine, a 10 s request timeout, and one retry on 5xx/429. A webhook that is down never stalls the bot.
- Attack messages carry stars, loot, bonus, deploy health and the session attack count. A result panel the bot could not read is reported as *unknown* — never as a zero-loot raid.
- `telegram_token` is a secret: keep `config.json` out of version control. The bot never logs it, and transport errors are scrubbed before they reach the log.
- Off by default. Nothing is sent until `notify.enabled` is true.

---

## 🎯 Town Hall Loot Floors

`search.use_suggested_floors` (on by default) sizes the three search minimums from the account's Town Hall instead of one static pair: 60k at TH5, 300k at TH9, 900k at TH14 and up. It only acts once the Town Hall is known, so an install that never imports an export keeps its configured numbers, and `search.auto_loot` still overrides both once it has enough measured bases.

Know the Town Hall either way:

```bash
# One-off: copy the export in game (Profile → Settings → More Settings → Data Export)
./build/bin/bot_cli -import-village ~/Desktop/village.json
```

or set `"town_hall_level": 12` under `search`. The import is also where the account's facts (builders, Laboratory, pets, Builder Base) are read from, and it never touches the device.

Floors above what a base can offer (5M gold/elixir, 50k dark elixir) are capped and logged once — a floor nobody can meet otherwise turns into an endless search that looks exactly like a broken one.

---

## 🛠 Builder Base Battles

`builder_base` is off by default. Once enabled, one Builder Base raid runs between home-village raids: drop one troop, let the raid pay out, surrender. Builder Base loot is paid on destruction against a base nobody is defending, so this is the cheapest income the account has.

```json
"builder_base": {
  "enabled": true,
  "mode": "elixir",
  "every_n_home_attacks": 3,
  "max_per_day": 20,
  "surrender_after_seconds": 20
}
```

Only `elixir` mode exists. `gold` (a full Builder Base attack) needs deployment templates that do not exist yet, so it is refused at the start of the sequence rather than attempted blind.

Every step proves its screen before the next tap, and a failure names the step (`the versus screen's Battle button was not located`), sends the `halt` webhook when notifications are on, and takes the boat back to the home village instead of restarting the game.

Three anchors cannot be proven from code — the boat to the Builder Base in the home village, the first troop card, and the drop point — so check them on the emulator before trusting a run. All three are authored reference pixels (the 860x732 geometry) and the bot scales them to the live display, so they never depend on the emulator's resolution:

1. In the home village run `make see ARGS="look"`. The report prints the classifier's state (`MainVillage`), any action button it found, and a coordinate-ruled render; convert a pixel back to reference by dividing by the `k` shown in the header. Set `boat_x`/`boat_y`.
2. Walk to the Builder Base by hand and repeat: the state must read `BuilderBase`. In battle the state must read `Battle`; the troop card and drop point go in `troop_slot_x`/`troop_slot_y` and `deploy_x`/`deploy_y`.
3. `make see ARGS="rec -label builder_base"` records frames while you walk the flow, so a later failure can be reviewed with `make see ARGS="timeline"` and `make see ARGS="show last"`.
4. First live run: `"every_n_home_attacks": 1` and `"max_per_day": 1`. Raise both once the log shows one raid completing end to end.

The schedule counts home attacks since the last Builder Base battle, plus a per-local-day cap held in memory (a restart starts the day over, like the scheduler's own counters).

---

## 📋 Command-Line Options

### Information & Utility
| Flag | Description |
|---|---|
| `-devices` | List connected ADB devices and exit |
| `-strategies` | List available strategy YAML files and exit |
| `-v`, `-version` | Print ClashGO version and exit |
| `-h`, `--help` | Show command-line help |

### Core Automation
| Flag | Description | Default |
|---|---|---|
| `-c`, `-config <file>` | Path to custom config JSON file | `config.json` |
| `-s`, `-strategy <file>`| Strategy YAML file | `valk_spam.yaml` |
| `-n`, `-max-attacks <N>`| Stop cleanly after N attacks (0 = unlimited) | `0` |
| `-once` | Run exactly 1 attack, then exit cleanly | `false` |
| `-deploy-only` | Deploy immediately on current screen (no search) | `false` |

### Loot & Search Filters
| Flag | Description | Default |
|---|---|---|
| `-gold <amount>` | Minimum gold loot to attack | `750000` |
| `-elixir <amount>` | Minimum elixir loot to attack | `750000` |
| `-de <amount>` | Minimum dark elixir loot to attack | `2000` |
| `-attack-if-de` | Attack if Dark Elixir threshold is met regardless of gold/elixir | `false` |
| `-max-skips <N>` | Maximum search skips before aborting attack and returning home | `60` |

### Village Management
| Flag | Description | Default |
|---|---|---|
| `-train` | Enable automatic army training (queues the saved recipe when the camps are empty) | `true` |
| `-full-army` | Require full army before attacking | `true` |
| `-army <1-4>` | Train specific saved army slot (1–4) | Strategy preset |
| `-upgrade-walls` | Enable automatic wall upgrading | `false` |

### Performance & Connection
| Flag | Description | Default |
|---|---|---|
| `-perf` | Enable maximum performance mode | `false` |
| `-no-screenshots` | Disable debug screenshots on disk | `false` |
| `-no-restart` | Do not restart Clash of Clans on boot | `false` |
| `-device <serial>` | ADB device serial / ID | `localhost:5555` |
| `-adb-host <host>` | ADB server host | `127.0.0.1` |
| `-adb-port <port>` | ADB server port | `5037` |
| `-q`, `-quiet` | Suppress 10-second heartbeat progress ticker | `false` |
| `-debug` | Enable verbose debug logging | `false` |

---

## 🛠️ Common Workflows

### Unattended Farming (Max Performance)
```bash
./build/bin/bot_cli -perf -gold 700000 -elixir 700000 -strategy auto_edrag_rush.yaml
```

### Single Test Attack
Test strategy deployment on the live emulator and exit cleanly after one battle:
```bash
./build/bin/bot_cli -once -perf -strategy valk_spam.yaml
```

### Instant Deploy
Already in battle or on the attack screen with troops loaded? Deploy immediately without searching:
```bash
./build/bin/bot_cli -deploy-only -strategy auto_edrag_rush.yaml
```

### Running with a Custom Profile
```bash
./build/bin/bot_cli -config /path/to/alt_account_config.json -perf
```

---

## 🛑 Clean Shutdown & Session Summary

- **Ctrl+C once**: Graceful shutdown. Bot completes current cycle or battle, saves stats, and exits.
- **Ctrl+C twice**: Immediate emergency exit.

On exit, the CLI prints a clean session summary:
```text
========================================
        ClashGO Session Summary
========================================
  Uptime:             24m10s
  Attacks Completed:  3
  Searches Skipped:   14
  Total Loot Gained:  Gold: 2,450,000 | Elixir: 2,100,000 | DE: 12,400
  Stars:              [0*] 0 | [1*] 1 | [2*] 2 | [3*] 0
  Vision Optim:       48 reused / 52 ran (48.0% cached)
========================================
```
