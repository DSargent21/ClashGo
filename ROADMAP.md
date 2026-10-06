# ClashGO Roadmap

Reference: MyBot.run (`MyBotRun/MyBot`) — mature AutoIt CoC bot. Its public repo
now ships only version files (source stripped), so the feature list below draws
on MyBot's documented feature set (forum/README), not a code diff.

Ground rules:

- **P0 = attacking polish.** Nothing else starts until attacks are reliable.
- Each item: definition of done = one live verified attack (or N clean runs), not
  just green unit tests.
- Unit tests currently pass (`go test ./internal/attack ./internal/bot ./internal/game`),
  `go build ./...` clean. Large uncommitted diff (~4.7k lines) lands first.

---

## Current state (short)

Working end-to-end: boot/splash recovery → village classify → attack-button
detect → search loop (loot filter → Next) → formula/pinned deploy → sweep +
verify → hero/spell/siege passes → battle-end wait → settled result OCR →
attack history JSON → return home → wall upgrades.

Known rough edges are in the search loop and the failure paths around deploy
(see P0). Missing entirely vs MyBot: training, donations, war, boosts,
notifications, TH/trophy/dead-base filtering, multi-instance.

---

## P0 — Attacking polish (do first)

- [ ] **Army readiness gate BEFORE search, not at deploy.**
  Today the full-army gate fires inside `DeployDynamicV2`
  (`internal/attack/orchestrator.go:657`), i.e. after a base is found and the
  attack sequence is committed. Done when: an incomplete army never leaves the
  village — sequence waits/idles instead of searching.
- [ ] **Abort path on deploy failure.**
  `executeAttackSequence` (`internal/bot/bot.go:1616`) keeps going after
  `deployErr != nil` and sits in `WaitForBattleEndCtx(4m)` with nothing on the
  field. Done when: deploy error → EndBattle/surrender → ReturnHome → report
  marked FAILED, battle never waits out the timer.
- [ ] **Loot read failure must not blind-skip a base.**
  `ReadAvailableLoot` error leaves zero loot → base skipped + Next clicked
  (`internal/bot/bot.go:1592`). Done when: read error retries on the same base
  (bounded), skip only on positive evidence of low loot.
- [ ] **Wire (or drop) the unused Search filters.**
  `SearchConfig` carries MinTrophies/MaxTrophies/MinTownHall/MaxTownHall/
  SkipBigBase/SkipMaxTH/AttackIfDarkElixirGT (`internal/config/config.go:196`);
  the loop only checks three loot minimums. Done when: each knob either drives
  a real read (trophy/TH detection on the search screen) or is removed.
- [ ] **Next-loop economics.**
  Only bound is the 5-minute wall → `restartGame` (`internal/bot/bot.go:1563`).
  Done when: max-skip counter + loot-curve give up (return home / stop session)
  like MyBot's search abort; never restart the game just for clicking Next too
  long.
- [ ] **Hero deploy reliability pass.**
  `hero_manager.go` carries a ~450-line uncommitted rework + edited retry tests.
  Done when: live runs show heroes place on first or documented-retry path,
  ability pass never double-taps a card (test + one live battle).
- [ ] **Sweep/verifier: reduce `remainingUndeployed > 0`.**
  Track per-slot failure causes from `last_attack_report.json`; fix the top
  cause only (root cause, not more retries).
- [ ] **Battle-end detection hardening.**
  4-minute timeout → full game restart; stall-ROI garbage reads (381% case)
  already clamped — verify clamp holds at 1280x720 and that `end_at_percent`
  auto-end fires before timeout. Done when: no restart caused by "stuck in
  battle" in N live sessions.
- [ ] **Zoom/pinch robustness pre-deploy.**
  Prior fix: pinch opening a dialog breaks the pre-battle chain. Done when:
  repeated attacks in one session never need `restartGame` for zoom reasons.
- [ ] **Land the uncommitted diff.** Split into reviewable commits (hero work,
  wall upgrade, calibration, bot), run full `make test`, tag next beta.

### P0 exit criteria

50 consecutive live attacks with: no game restarts outside emulator faults,
deploy success ≥ 95%, result parsed ≥ 95%, zero wasted battles (deploy failure
that still consumed a battle timer).

---

## P1 — Attack depth (after P0)

- [ ] **Troop training system (minimal).** Legacy training was dropped
  (commit `4ecdfa1`); `TrainingConfig` knobs are mostly dead. Re-add: train
  strategy composition from army sheet, brew spells, wait for hero revive.
  Gate: bot idles as "waiting for army" instead of attacking on partial camps.
- [ ] **Dead-base / collector detection.** Farming quality: detect full
  collectors vs active bases; skip live bases below curve. MyBot standard.
- [ ] **TH level detection on search screen** (enables SkipMaxTH/TH filters,
  prevents TH-up snipes when pushing).
- [ ] **Strategy library growth.** Exists: `auto_edrag_rush`, `valk_spam`,
  `default.csv` (BARCH). Add: GOWIPE, mass dragons, lavaloon, QB/charge —
  via existing `design_attack` + formula authoring workflow
  (`docs/formula-authoring.md`).
- [ ] **Trophy-push mode.** Push profile: different strategy, no loot filter,
  league handling, surrender below threshold.
- [ ] **Early surrender rules.** Surrender when deploy failed, or 0% for N
  seconds with no troops alive (stall timer exists — extend to pre-first-deploy).
- [ ] **Attack replay regression.** `attack_record`/`attack-replay` already
  exist — formalize: keep a small corpus of recorded attacks as CI gate for
  deploy logic (device-free replay).

---

## P2 — Bot features (MyBot parity)

- [ ] Clan castle **donations** (request + donate loop).
- [ ] **Notifications**: Telegram/Discord webhook on session end, streaks,
  failures. (None today.)
- [ ] **Boosts**: troop/hero/potion boosts, gem-spend guard config.
- [ ] **Clan games / daily challenges / quests**.
- [ ] **War**: war map navigation, scripted war attack (MyBot's CSV war base
  attacks are the deep end — defer until farming is boring-proof).
- [ ] **Multi-instance**: several emulators/accounts, one supervisor process.
- [ ] **Stats store**: JSON history (500 rows) → SQLite + GUI charts.
- [ ] **GUI**: strategy selector, live log view, manual next/skip controls.
- [ ] **Windows port** (README help-wanted; CI matrix + ADB quirks).

---

## Suggested sequence

1. Land current diff, cut beta.
2. P0 items in listed order — gate, abort, loot-read, search filters first
   (they're small and each kills a whole class of wasted battles).
3. Hero/sweep/battle-end polish measured against P0 exit criteria.
4. P1 training + dead-base detection (farming quality jumps).
5. P2 one at a time, driven by what you actually hit while farming.

## Non-goals

- No OCR runtime dependency (template/pixel only — see `docs/DESIGN.md`).
- No new GUI framework, no SQLite until JSON actually hurts.
- No war before farming is reliable.
