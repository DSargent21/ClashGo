# ClashGO — Long-Term Product Roadmap

Vision: **the bot that plays Clash of Clans for you — village, army, and
economy — unattended, on any desktop.**

ClashGO proves the hard part already: a native vision engine that reads the
game screen and acts through ADB in real time (Apple Silicon first; OpenCV +
persistent ADB transport; self-healing boot and recovery). The roadmap turns
that core into a full auto-player and, beyond it, a platform.

**Scope: farming and village progression.** No war, no CWL, no trophy push.
ClashGO is a goldmine, not a competitor — that keeps the product focused and
the engineering honest.

---

## Phase 1 — Reliable farming core *(shipped)*

Unattended loop: search → evaluate → attack → results → walls.

- Multi-strategy attacks (YAML + per-unit deploy formulas, replayable
  recordings), hero/spell/siege orchestration.
- Self-healing session: boot recovery, stuck watchdogs, verified results.
- Attack history, session stats, auto-updater.

**Milestone:** days-long unattended sessions, ≥95% attack success.

## Phase 2 — Full-auto village *(next)*

The village runs itself between attacks — how CoC actually progresses:

- **Auto Army**: train the strategy's composition, brew spells, wait for
  hero revive — never attacks on a partial army. (Drops what the game's
  training queue already guarantees us for free.)
- **Super troops**: sneaky goblins and friends on their 12h cycle — the
  core farming troop, on schedule.
- **Auto Lab**: research from a priority list (farming army first), safe
  book/hammer rules — lab time is the real progression bottleneck.
- **Auto Builders**: hero upgrades → key buildings → walls (already
  shipped), builder-allocation policy, magic-item rules (books, runes,
  research potions).
- **Heroes + equipment**: upgrade order; hero sleep state gates attacks.
- **Dead-base economy**: collector detection, loot curves, TH/trophy
  filters — loot per hour becomes a measured number.

**Milestone:** "set a target, walk away" — village progresses with zero
manual play for a week.

## Phase 3 — Platform

- **Multi-account / multi-instance**: one supervisor, N emulators,
  per-account profiles and schedules.
- **Windows + macOS**: the community demand already on the table (README).
- **Notifications & remote control**: Telegram/Discord alerts, pause/resume
  from your phone.
- **Web dashboard**: per-account cards, loot/hour and star charts
  (SQLite-backed history).
- **Strategy sharing**: open YAML format → community formulas, gallery to
  start; marketplace later if demand shows.

**Milestone:** a farm of accounts managed from one dashboard, Mac and PC.

## Phase 4 — Intelligence

- **Base evaluation model**: we already record every deploy and outcome —
  score bases before attacking so loot, stars, and raid time become
  predictable.
- **Auto formula authoring**: today `design_attack` needs a human; record a
  live attack, get the per-corner formula back.
- **Replay-tuned strategies**: candidate deploy changes run against the
  recorded corpus, winners get promoted — strategies improve without
  hand-authoring coordinates.
- **Adaptive humanization**: pacing that moves as the game's detection
  does (ongoing, engineering-continuity item).

**Milestone:** the bot measurably out-farms manual play on the same account.

---

## Why this wins (investor notes)

- **Hard part is done**: real-time screen understanding + action loop
  (classifier, calibration, deploy verification, result settlement) —
  ~40k lines of battle-tested edge cases; new entrants start from zero.
- **Clean layering**: vision / state machine / strategy executor / UI —
  each phase is additive, not a rewrite.
- **Open format as growth lever**: shareable strategies and formulas let
  community content compound without headcount.
- **Distribution automated**: GitHub Releases + in-app updater — shipping
  is a tag push.
- **Expandable market**: the vision layer is game-agnostic — same engine
  extends to other Supercell titles and screen-driven games.

## Sequencing

| Phase | Horizon | Anchor metric |
|-------|---------|---------------|
| 1 Farming core | shipped | unattended days, ≥95% attack success |
| 2 Auto village | next major | zero-touch week; loot/hour vs manual |
| 3 Platform | parallel tracks | accounts per dashboard, platform split |
| 4 Intelligence | ongoing | loot/hour lift from model + replay tuning |

Dates omitted on purpose: sequence is dependency-driven (2 before 3; 4 runs
continuously), and live-verified metrics gate each phase.
