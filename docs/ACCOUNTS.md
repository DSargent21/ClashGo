# Multi-Account Switching

ClashGO runs one bot session at a time. Multiple accounts are supported as a
**fleet**: every village owns a profile, an emulator binding and a Supercell ID
slot on that emulator, and the switcher taps the game's own account list before
handing the active session over — without ever mixing config, stats or history.

This document covers the model, the competitor landscape that shaped it, the
implemented feature set, and the deliberate limits.

---

## 1. The model: emulator + Supercell ID slot

Supercell ID offers two ways to play several villages on one machine:

1. **A separate game instance per village**, each signed in once and left
   signed in.
2. **The in-game fast switcher** (profile → Settings → Supercell ID), which
   lists every village already signed in on that device and switches with one
   row tap.

ClashGO supports both and treats them as one identity:

```
account = (ADB serial, slot)
```

- **ADB serial** (`localhost:5555`) selects the emulator instance.
- **Slot** is the 1-based row in that instance's Supercell ID list. Row 1 is
  the top of the list.

One emulator can host up to five villages, one per slot; one instance per
village is still fully supported.

### What a switch does

1. Stop the running bot and flush its stats to the **old** profile.
2. Tap the game UI: profile corner (top left) → Settings (if shown) → Supercell ID → the
   row for the target slot.
3. Read the profile screen and confirm the logged-in village is the expected
   one (player tag, or the name when no tag is set).
4. Only then select the new profile and boot the bot on the bound serial.

No password, token or cookie is stored. The fast switcher only taps rows that
are already signed in; enrolling a new village (email code, etc.) stays
operator work. ClashGO stays out of the login form, which is the least reliable
and most ToS-visible part of any bot.

### If the wrong village logs in

A row that lands on the wrong village must never be silent: the bot would farm
somebody else's base and book the loot under the wrong account. So:

- the switch aborts before the registry changes — the previous account stays
  active and nothing starts;
- the error names both villages (`slot 2 logged the game in as Somebody Else
  (#9XYZ0000), but this account expects Fleet Two (#2P0Y8LQGR)`);
- when the previous account lives on the same emulator, ClashGO taps its slot
  back so the operator is not left on a stray village.

This is the same sequential topology used by the mature bots surveyed below,
with the row tap automated instead of manual.

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
- **Supercell ID slot** (1-5): the row this village occupies in the game's
  account list. Two accounts may share an emulator only with different slots.
  A slot of `0` means "not set": the profile still works device-bound but
  cannot be switched in-game until the row is entered.
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

1. Refuse an account with no slot, naming the field to fix.
2. Cancel a running bot or in-flight boot.
3. Wait for teardown (bounded at 30 s; refuses the switch on timeout).
4. Flush stats to the **old** profile.
5. Drive the game UI to the target slot and verify the logged-in village
   (abort + restore the previous slot on mismatch).
6. Select the new profile and mark it active.
7. Load the new profile's stats/history into the UI cache.
8. Re-apply the account's ADB host/port/serial.
9. Optionally boot the bot.

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
      "slot": 2,
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

1. Decide the topology: one emulator with several slots (lowest RAM), or one
   instance per village. Both are supported at once.
2. Sign each village into the game once, with "remember me" enabled, then open
   **profile → Settings → Supercell ID** and note its row. Rows are numbered
   from the top: row 1 is the first entry.
3. Add an account in ClashGO. The drawer scans the running instances and lists
   model, resolution and the slots already used on each one, and pre-fills a
   `Village N` name, the first instance with a free row and its first free
   slot. Pick the row that matches the game's list and save.
4. Use **Switch & start** once per account. That is the verification: if the
   profile screen shows a different village, the switch refuses and names both.

Detection is read-only: it scans the emulator ADB port span (5555–5565) and
asks each answering serial for its model, without running `adb connect` or
restarting the adb server. The adb server's `emulator-NNNN` console entry is
folded onto the same instance's TCP serial, so one emulator cannot be counted
twice under two names.

### 5.1 Capturing the switch templates

The switch is template-based, so a game update that moves a button needs one
recapture, not a code change. Put the game on the main village of any signed-in
village and run one command:

```bash
go run ./cmd/account_templates
```

That is the whole setup. It taps the profile corner itself, opens Settings or
Supercell ID depending on what the build shows, captures each screen, and writes
everything the switch needs into the directory the runtime reads
(`<assets>/templates` + `<assets>/account_switch.json`):

| File | Shows |
|---|---|
| `acct_profile.png` | the village HUD corner the switch taps to open the profile |
| `acct_settings.png` | the Settings tab/gear on the profile screen |
| `acct_supercell.png` | the "Supercell ID" row (or its Open button) |
| `acct_slot_list.png` | the first account row, which anchors the slot list |

Nothing is cropped by hand, and nothing is installed unverified:

- each PNG is matched back against the screenshot it came from, in that screen
  region, at the runtime's threshold;
- a flat crop (empty background) is refused, because it would match itself and
  then nothing else;
- the Settings and Supercell ID crops must actually cover those controls;
- the derived rows (anchor + `slot_row1_offset` + `slot_row_spacing`) must land
  on the account-list rows, not between them.

Anything that would fail later fails now, naming the file, the coordinate and
the flag to adjust (`-profile`, `-settings`, `-supercell`, `-slot-list`,
`-slot-row1-offset`, `-slot-row-spacing`). Screens are saved under
`output/account_templates/<ts>/` so a refusal can be replayed offline:

```bash
go run ./cmd/account_templates -source output/account_templates/<ts> -slot-row-spacing 0.1172
```

`-source DIR` reads `village.png`, `profile.png`, `settings.png`, `panel.png`
and an optional `text.json` (OCR boxes, so a machine without the game can
reproduce a run). Omit it to capture live from `config.json`'s device or
`-device`.

Then press **Switch** (or **Switch & start**) on the account. The switch taps
through the in-game list, reads the profile's player tag and refuses with both
identities if the wrong village logged in.

Most builds show Supercell ID directly on the profile screen; if the game hides
it behind a Settings tab, the helper taps that tab itself, using its text when
it is visible and the top-right HUD corner when the build is icon-only.

`assets/account_switch.json` holds the two row fractions as fractions of screen
height:

- `slot_row1_offset` — where row 1's centre sits below the list anchor (0 is
  normal: the anchor is row 1);
- `slot_row_spacing` — pitch between consecutive rows.

If a slot taps the neighbouring row, the identity check refuses the start and
names the village it actually got; re-run the helper (it re-derives both) or
adjust the fractions by the difference.

The file is optional: missing values fall back to the defaults, and a
present-but-malformed file is reported, not ignored.

Do not run ClashGO's GUI and CLI against the same config directory at the same
time; each process assumes it owns the active profile.

---

## 6. Limits and roadmap

Deliberate limits:

- **Sequential, not parallel.** One bot loop and one device at a time. A
  parallel fleet would need per-device capture loops, per-account stats
  multiplexing and a much larger scheduler; it is not silently pretended.
- **No credential automation.** The fast switcher is driven; enrolling a new
  village (email code) is still operator work, and no Supercell credential is
  stored.
- **Templates are per game UI.** Four PNGs must be captured once per game
  version; a moved button fails with the template name instead of tapping
  blind.
- **Identity check needs the OCR helper.** Reading the player tag uses
  `tools/ocr.swift` (macOS Vision). Without it the switch refuses to start
  rather than hand over an unverified village.
- **Start on the already-active account does not re-verify.** Pressing Start on
  the active card boots the bot as before; use **Switch** (even to the same
  account) when the game may have been left on another village by hand.
- **No proxy/IP management per account.** Out of scope for a local emulator
  fleet.
- **One process per config tree.** No cross-process locking yet.

Natural next steps, in order of value:

1. Per-account strategy presets (today the account inherits the active
   profile's tuning at creation and then diverges).
2. Scheduled rotation windows (reuse `ScheduleConfig` per account).
3. Parallel workers for hosts with enough RAM, gated behind a separate
   multi-engine app layer.
4. Export/import of the fleet registry (secrets are already absent).
5. Per-game-version template packs, so a UI update is a download rather than a
   recapture.

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
| `in-game account switching needs 3 template PNGs` | The switch templates were never captured | Run `go run ./cmd/account_templates` once (5.1) |
| `could not find "acct_settings"` / `"acct_supercell"` on screen | Game is not on the expected screen, or the button moved after an update | Put the game on the main village and re-run `go run ./cmd/account_templates` |
| `slot N logged the game in as …` | Wrong row number, or the row order changed in-game | Check profile → Settings → Supercell ID and fix the account's slot |
| `account "X" has no Supercell ID slot yet` | Legacy profile (slot 0), or the drawer was closed before saving | Edit the account, pick its row, save |
| `slot N lands at y=…` | Row geometry does not match this emulator | Adjust `slot_row1_offset`/`slot_row_spacing` in `assets/account_switch.json` |
