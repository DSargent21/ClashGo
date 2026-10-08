# ClashGO CLI Guide ⚔️

`bot_cli` is the headless, ultra-lightweight CLI version of ClashGO.

It runs the full bot engine without WebKit, web assets, or GUI overhead. Ideal for servers, headless machines, background tmux/screen sessions, or users prioritizing maximum performance and minimal resource usage.

---

## ⚡ Quick Start

### 1. Build the Binary
```bash
make build-cli
```
Binary output: `build/bin/bot_cli`

### 2. Verify ADB Device Connection
```bash
./build/bin/bot_cli -devices
```

### 3. Check Available Strategies
```bash
./build/bin/bot_cli -strategies
```

### 4. Run Farming with Maximum Performance
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
| `-min-trophies <N>` | Minimum trophy target | `0` |
| `-max-trophies <N>` | Maximum trophy target | `3000` |

### Village Management
| Flag | Description | Default |
|---|---|---|
| `-train` | Enable automatic army training | `true` |
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
