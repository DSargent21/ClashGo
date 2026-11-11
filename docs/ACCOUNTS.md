# Multi-Account Switching

ClashGO runs one bot session at a time. Multiple accounts are supported as a
**fleet**: every village owns a profile and an emulator binding, and the
switcher hands the active session from one account to the next without ever
mixing their config, stats or history.

This document covers the model, the competitor landscape that shaped it, the
implemented feature set, and the deliberate limits.

---

## 1. Why device-bound profiles

Supercell ID offers two ways to play several villages on one machine:

1. **A separate game instance per village**, each signed in once and left
   signed in.
2. **The in-game fast switcher** (Settings → arrows next to Supercell ID),
   which works only after Supercell has already trusted the device.

Automating option 2 means driving the login form, storing Supercell
credentials and replaying email verification. That is a security liability, a
Terms-of-Service problem, and — because the login UI changes between game
versions — the least reliable part of any bot. ClashGO therefore automates
the *session hand-off*, never the login:

- An account is bound to one ADB serial (`localhost:5555`, `localhost:5556`, …).
- The operator signs each emulator instance into its village once.
- ClashGO switches by stopping the current bot, selecting that account's
  profile and booting against the bound serial.
- The `email` field is a **login hint**, not a credential. No password,
  token or cookie is stored anywhere.

This is the same topology used by the mature bots surveyed below.

---

## 2. What other bots do

Surveyed projects (2025–2026) fall into two families.

| Project | Topology | Notable multi-account behaviour |
|---|---|---|
| [m24842/CoC_Bot](https://github.com/m24842/CoC_Bot) | Emulator instance per account | `INSTANCE_IDS` in `configs.py`; instances named in the emulator's multi-instance manager; one bot per instance |
| [GameBotOps](https://www.gamebotops.com/en) | Sequential fleet, one instance running at a time | Accounts stored outside application files; waiting/running/flagged states in one panel; fleet order |
| [ryanchandra1/clash-of-clans-farm-bot](https://github.com/ryanchandra1/clash-of-clans-farm-bot) | Account list on a shared emulator | 15+ accounts, automatic switching after N farm cycles, per-account progress in `account_config.json` |
| [luizunc/SimplicityBot](https://github.com/luizunc/SimplicityBot) | Several accounts at once | Up to 5 accounts, per-account loot statistics |
| [HeyFang/coc-bot](https://github.com/HeyFang/coc-bot) | Device rotation | Rotates between multiple devices rather than emulator profiles |
| [Zen Macro](https://zenmacro.shop/) | Instance fleet | Multi-account reserved for the emulator edition; one live stat panel per instance |

Three patterns are consistent across all of them and are the reason for this
implementation's shape:

1. **Per-account state is mandatory.** Loot filters, army slot, strategy and
   history belong to a village, not to the machine.
2. **A switch must be safe.** Stop taps, flush metrics, then change identity.
   Swapping under a running attack produces mis-attributed loot and stray
   taps on the wrong village.
3. **The fleet needs an eligibility policy.** Cooldowns, daily caps and
   priority exist because a naive round robin hammers one village forever.

The differences are topology (parallel instances vs. sequential hand-off) and
whether credentials are stored. ClashGO chooses sequential hand-off with
device-bound profiles: it fits the existing single-engine architecture, needs
no credential store, and keeps one authoritative source of truth for "which
village is this session farming".

---

## 3. Implemented feature set

### Accounts

- Create, edit and archive accounts from **Accounts** in the sidebar.
- Stable slug ID (immutable after creation), unique player tag validation.
- Town hall, player tag, login hint, free-form notes.
- Enable/disable an account without deleting it; disabled accounts are
  excluded from rotation but can still be switched to manually.
- Delete moves the whole profile to `accounts/.trash/<id>-<timestamp>` so
  history is recoverable.
- Active account cannot be deleted; switch away first.

### Profiles

Each account owns:

```
<configDir>/accounts/<id>/
  config.json          # loot filters, strategy, upgrade policy, device
  stats.json           # lifetime session totals
  attack_history.json  # per-village attack report
  boot_profile.json    # learned boot timings
  output/…             # debug artifacts
```

`paths.SetProfile` redirects `paths.ResolveConfig` for the whole process, so
every existing subsystem (bot config, stats writer, history cache, debug
output) became account-aware without a parallel code path.

### Safe switching

`SwitchAccount` is serialized and performs:

1. Cancel a running bot or in-flight boot.
2. Wait for teardown (bounded at 30 s; refuses the switch on timeout).
3. Flush stats to the **old** profile.
4. Select the new profile and mark it active.
5. Load the new profile's stats/history into the UI cache.
6. Re-apply the account's ADB host/port/serial.
7. Optionally boot the bot.

The header, sidebar chip and Accounts page all converge through the
`accounts_changed` event, so no view can show a stale active account.

### Fleet rotation

- **Round robin** (registry order, starting after the current account) or
  **priority** (highest priority, then least-recently-used).
- Cooldown per account after a session.
- Daily attack cap per account; the session stops itself when the cap is
  reached, not merely at the next switch.
- Session cap per account, or the fleet default
  (`rotation.attacks_per_account`) when rotation is enabled.
- Disabled accounts and ineligible accounts are skipped.
- Rotation reacts only to a session that ended on its own, so pressing Stop
  by hand never starts another village.
- No eligible account → the current session is left alone (logged, not
  force-stopped).

### Observability

- Per-account cards show status (running / ready / cooldown / daily limit /
  disabled), device, last-used time, today's attacks and lifetime loot.
- Fleet summary: account count, in-rotation count, today's attacks, gold
  banked.
- Daily usage can be reset per account after an intentional change of plan.
- Every switch, skipped rotation and failed hand-off is logged with account
  and device fields.

### CLI

```bash
bot_cli -accounts                  # list accounts + rotation policy
bot_cli -account mini -perf        # run as that account's profile + device
```

Explicit flags still win: `-account mini -device localhost:5557` overrides
the binding for an ad-hoc run.

---

## 4. Registry format

`<configDir>/accounts.json` (mode `0600`):

```json
{
  "version": 1,
  "active_id": "mini",
  "rotation": {
    "enabled": true,
    "order": "round_robin",
    "auto_start": true,
    "attacks_per_account": 20
  },
  "accounts": [
    {
      "id": "mini",
      "name": "Mini",
      "player_tag": "#2P0Y8LQGR",
      "town_hall": 12,
      "email": "hint only",
      "notes": "",
      "device": {
        "adb_host": "127.0.0.1",
        "adb_port": 5037,
        "device_id": "localhost:5556"
      },
      "enabled": true,
      "priority": 100,
      "cooldown_minutes": 10,
      "max_attacks_per_day": 25,
      "max_attacks_per_session": 5,
      "created_at": "2026-11-11T12:00:00Z",
      "last_used_at": "2026-11-11T12:30:00Z",
      "usage": { "date": "2026-11-11", "attacks": 12, "switches": 3 }
    }
  ]
}
```

Writes are atomic (temp + fsync + rename) and keep `accounts.json.bak`. A
malformed file is reported, never overwritten, so a bad edit is recoverable.

On first launch after upgrading, the legacy single-profile `config.json`,
`stats.json` and `attack_history.json` are **copied** (not moved) into the
`default` account, which becomes active. The old files stay in place.

---

## 5. Setting up a fleet

1. Open the emulator's multi-instance manager and create one instance per
   village.
2. Start each instance and confirm the ADB port it exposes:

   ```bash
   adb devices -l
   ```

   BlueStacks conventionally assigns `localhost:5555`, `localhost:5556`,
   `localhost:5557`, …
3. Sign each instance into its village once, with "remember me" enabled.
4. Add an account in ClashGO for each instance and enter its serial.
5. Use **Switch & start** to verify the pairing. If a boot error mentions a
   timeout, the serial and the running instance do not match.

Do not run ClashGO's GUI and CLI against the same config directory at the same
time; each process assumes it owns the active profile.

---

## 6. Limits and roadmap

Deliberate limits:

- **Sequential, not parallel.** One bot loop and one device at a time. A
  parallel fleet would need per-device capture loops, per-account stats
  multiplexing and a much larger scheduler; it is not silently pretended.
- **No credential automation.** Supercell ID login is operator work.
- **No proxy/IP management per account.** Out of scope for a local emulator
  fleet.
- **One process per config tree.** No cross-process locking yet.

Natural next steps, in order of value:

1. Per-account strategy presets (today the account inherits the active
   profile's tuning at creation and then diverges).
2. Scheduled rotation windows (reuse `ScheduleConfig` per account).
3. Parallel workers for hosts with enough RAM, gated behind a separate
   multi-engine app layer.
4. Optional in-game fast-switcher support via a guided, operator-confirmed
   template flow — never stored credentials.
5. Export/import of the fleet registry (secrets are already absent).

---

## 7. Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `account registry unavailable` | `accounts.json` is corrupt | Restore `accounts.json.bak` or fix the JSON; the file is not replaced |
| Switch says "bot did not stop within 30s" | A wedged ADB transport during teardown | Stop the bot, check `adb devices`, then retry |
| Boot targets the wrong port | Account device serial does not match the running instance | Edit the account's device serial |
| Rotation never advances | All accounts on cooldown/cap, rotation disabled, or auto-start off | Check the Accounts header and rotation panel |
| `daily_limit` but attacks remain | Daily counter is local to ClashGO and only resets at midnight or via **Reset usage** | Reset usage from the account card |
| Stats look reset after upgrade | New `stats.json` for the `default` profile | Check `accounts/default/stats.json`; legacy files were copied, not moved |
