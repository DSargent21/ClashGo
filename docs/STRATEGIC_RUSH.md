# Strategic rush upgrades

The bot's builder automation follows **CallMeTee's Strategic Rush Bible v1.6**
(last updated 24 August 2026). This page documents the rules the bot implements
and the guide logic behind them, so a setup choice can be checked without
opening the guide.

Source: CallMeTee's Strategic Rush Bible. Community guide, not affiliated with
Supercell.

## The two rules

1. **Offense before defense.** Every builder-hour spent on offense makes loot
   and war stars; defense only protects what is already there.
2. **New building or capability before an existing level.** A new Army Camp,
   pet house, or Blacksmith changes what the account can do. Another level on a
   Wizard Tower does not.

The guide's per-Town-Hall order is the practical form of those rules:

| # | Priority |
|---|----------|
| 1 | Hero closest to 8-day upgrades — Archer Queen first |
| 2 | Army Camps, Clan Castle, Blacksmith, Hero Hall |
| 3 | Laboratory, Barracks, Spell Factories |
| 4a | Pet House, resource buildings |
| 4b | New buildings and Town Hall requirements |
| 5 | A second hero (lowest, or next closest to 8 days) |
| — | Defenses only when the Town Hall requires them |

## What the bot does

### Strategic rush (recommended)

`upgrade_strategy: "strategic_rush"`

Scrolls the Build Menu list, reads every page (up to four), deduplicates rows,
and ranks them with OCR-tolerant name matching (case, spaces, and punctuation
are ignored). Then it scrolls back to the winning row, re-reads it for a fresh
tap coordinate, and upgrades the best one:

1. Heroes: Archer Queen → Grand Warden → Royal Champion → Minion Prince →
   Barbarian King → Dragon Duke → any other hero.
2. Army buildings: Army Camp → Clan Castle → Blacksmith → Hero Hall →
   Laboratory → Barracks → Spell Factory → Pet House → Workshop → other army.
3. Resources.
4. Defenses.
5. Traps.
6. Anything the OCR did not classify.

Equal ranks keep the in-game suggestion order. Defense is last, never skipped:
if it is the only thing left, it gets built.

The category toggles still apply first. Turning Defenses off means they are
never chosen, even when everything else is done.

### Max

`upgrade_strategy: "max"`

No priority order: every allowed category is eligible and the **most expensive**
row is chosen. Use it when the goal is to push everything up, not to rush a
Town Hall.

### Legacy orders

`heroes_first`, `cheapest_first`, `most_expensive_first`, and `balanced` remain
available for the Config tab and older config files.

## Heroes

- Keep one hero upgrading at all times. The Archer Queen is the default first
  hero: she is usable in every composition and has the longest path to 8-day
  upgrades.
- Second is the Grand Warden; he costs elixir and does not compete with dark
  elixir heroes.
- Once a hero reaches 8-day upgrades, books of heroes get full value. Finish
  that hero with books, then move the builder to the next-closest hero.
- Before max Town Hall, one hero down is enough. At max Town Hall, run as many
  heroes as builders allow.

The bot only chooses the next building row; it does not start hero upgrades
itself (heroes are not rows in the builder overview).

## Army buildings

Army Camps and Clan Castle first: every attack is balanced around the full
troop and spell complement. Blacksmith next, because it raises ore storage and
equipment caps. Lab, Barracks, and Spell Factories only when they unlock
something the account needs now. Pet House as soon as it exists.

## Walls — keep one builder

The wall toggle on the setup page sets:

- `upgrade_walls: true`
- `reserve_builders: 1`

The reserve makes the builder automation stop when only one builder is free, so
that builder is available for wall dumping instead of disappearing into another
long upgrade. Wall spending then follows the wall resource and minimum reserves
in the Config tab.

The guide is explicit that an **idle** builder for walls is wrong. Walls are
filled from overflow loot between upgrades, not by parking a builder forever.
Turning the toggle off sets `upgrade_walls: false` and `reserve_builders: 0`.

## Town Hall notes

- **TH2–TH12:** get to TH11 fast (Grand Warden, Eagle, super troops). Keep the
  lab on farming troops, then donation troops.
- **TH13–TH15:** do not linger. Ranked ore and war ore are still below the top
  tiers.
- **TH16–TH17:** upgrade only the Town Hall requirements (cannons, archer
  towers, Eagle, merged defenses) and move on. Keep the Giga upgrade running at
  TH17.
- **TH18 (max):** unlock every offensive capability first, then run multiple
  heroes, then core defenses. Use hammers and books on the longest timers.

## Magic items and ore (short version)

- Spend books and hammers on the longest upgrades available; a 14-day+ timer is
  the target for building hammers.
- Do **not** buy Hammer of Heroes; builder potions during time-discount events
  are better value.
- Buy orange ore first from raid medals, then purple; research potions after.
- CWL medals are the main engine: higher league placement means more hammers
  and more skipped time.

## Where this lives in the app

- Setup: army picker → **Auto upgrades** page (enable, plan, types, wall
  builder).
- Config → Upgrades: same plan and categories, plus spend caps, lab, pets, and
  dry run.
- Config file: `upgrade.upgrade_strategy`, `upgrade.upgrade_walls`,
  `upgrade.reserve_builders`, and the `upgrade_*` category booleans.

The bot never upgrades the Town Hall, places buildings, or spends magic items.
Those stay manual; this automation only picks the next builder row.
