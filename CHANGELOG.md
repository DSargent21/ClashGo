# Changelog

All notable changes to this project will be documented in this file.

## [0.8.0-beta] - 2026-10-08

### Fixed
- **Four-Sided Valkyrie Deployment Edge Scaling & Slot Reconciliation** (`internal/attack/executor.go`, `internal/attack/hero_manager.go`, `internal/attack/orchestrator.go`):
  - Fixed edge coordinate scaling in `TapDeployFourSides` to dynamically scale authored edges to physical screen resolution and push 20px outward into clear grass.
  - Fixed `applyCornerOverride` in orchestrator so existing pinned edges are preserved rather than overwritten by the primary target edge.
  - Added post-deploy verification and top-up pass to `DeployTroops`: verifies slot is genuinely empty before marking deployed; leaves undeployed slots for the sweeper to finish all remaining troops.
- **Accurate Star Counting Outcome Ground Truth** (`internal/bot/bot.go`):
  - Authoritative star count now follows deterministic Clash of Clans destruction rules derived from live battle percentage and Town Hall status (<50% = 0⭐, >=50% = 1⭐, TH+50% = 2⭐, 100% = 3⭐), preventing 0-star defeats from misreading as 2⭐ due to victory ribbon pixel overlap.
- **Obstacle Dialog & Return Home Dismissal Watchdog** (`internal/bot/bot.go`, `internal/attack/attack.go`):
  - Added fast dismissal for accidental obstacle dialog popups during village capture loops.
  - Handled `StateConnectionLost` and ticker deadline bounds in `WaitForBattleEndCtx` to eliminate battle end hangs.
- **Streamlined 1-Click Auto-Update & In-Place Replacement** (`app.go`, `internal/updater/updater.go`, `build/darwin/install_update.sh`):
  - Fixed premature state transition: `ApplyAuto()` and `Apply()` now permit both `StateReady` and `StateRestarting`, resolving the "download not ready" error that blocked auto-installations.
  - Spawns helper in a detached process group (`Setpgid: true`) with redirected logging to avoid broken pipe signals on process termination.
  - Implemented atomic bundle swap with rollback fallback in `install_update.sh`, ad-hoc code-signing re-seal, and quarantine cleanup.
- **Persistent In-App Update Banner & Settings Integration** (`web/src/App.tsx`, `web/src/components/UpdateBanner.tsx`, `web/src/components/SettingsView.tsx`):
  - Retains header update badge even when modal dialog is dismissed ("Later" or closed), ensuring 1-click update is always reachable.
  - Added direct "Update Now" / "Install Now" launcher in Settings App Version section.

## [0.7.0-beta] - 2026-10-07

### Added
- **Simultaneous 4-Sided Rapid Deployment (`FourSides` pattern)** (`internal/attack/executor.go`):
  Deploys high-capacity armies (such as Valkyries) across all four base edges simultaneously in under 1.2 seconds using interleaved coordinate bursts.
- **Centered Spell Targeting** (`internal/attack/spell_deployer.go`):
  Clustered point drop targeting dead-center `(640, 360)` for Earthquake spells and core breaches.
- **Per-Strategy Auto-End Battle at 50% Damage** (`internal/attack/attack.go`, `web/src/components/ConfigView.tsx`):
  Added operational UI switch to automatically end battles immediately once 50% damage (1 star) is secured. Defaults to ON for Valkyrie strategies to minimize troop losses and cycle times.
- **Saved Recipe Slot Configuration** (`internal/bot/bot.go`, `assets/army_slots.json`, `cmd/pick_army_slots`):
  Per-strategy army recipe slot selection (slots 1–4) with dropdown automation on the attack sheet and coordinate calibration tools.
- **Search Filter Bypass Performance Optimization** (`internal/bot/bot.go`):
  Bypasses `ReadAvailableLoot` OCR entirely when base search filters are disabled, eliminating 200–400ms OCR delay per matched village.

### Fixed
- **Strategy YAML Path Resolution** (`internal/bot/bot.go`):
  Added `resolveStrategyPath` to resolve bare strategy filenames selected from the UI dropdown against `assets/strategies/` in both development and production bundles.
- **Digit 1 Misread on Clipped % Symbol** (`internal/game/loot.go`):
  Guarded destruction percentage reads against right-edge ROI clipping where the '%' symbol stroke was parsed as a phantom digit '1'.

## [0.6.0-beta] - 2026-09-28

### Fixed
- **Battle loot and the league bonus are recorded only from a verified, settled
  read; a suspicious parse is refused instead of banked**
  (`internal/bot/result_settle.go`, `internal/bot/bot.go`). The result panel is
  now watched for at least 10 s and a read is accepted only when every resource
  is within generous per-resource bounds and the whole read repeats across
  consecutive captures. A win whose league gold and elixir bonuses are missing
  or unequal is an unfinished panel (or an OCR row shift), not a result, and is
  never banked. The old "keep the last non-empty read" fallback is gone: when no
  credible read settles inside the bounded 28-capture budget the attack is
  recorded as unparsed, while the independently measured destruction-rule star
  count is still kept, so an unreadable panel can no longer publish a
  half-counted loot row. This closes the misparse that recorded an impossible
  316,800 dark-elixir league bonus and the truncated reads that under-counted
  loot and the bonus column.

  Verified live 2026-09-28 (`--once`, one real 720p attack): the bonus column
  was read as 0 and refused across 15 captures while it was still empty, caught
  mid-count at 90,805, and was accepted only at `reads=20 watched_s=10.52`. The
  accepted values — gold 691,355 / elixir 723,129 / DE 7,006 and bonus
  288,000 / 288,000 / 2,160 — match an independent Apple Vision OCR of the saved
  frame exactly, and the session totals counted the bonus
  (979,355 = 691,355 + 288,000).
- **The released DMG now launches. It never did.** `make build-gui` passed only
  the version/commit ldflags to `wails build`, so the app's OpenCV references had
  no `LC_RPATH` to resolve against and dyld aborted before `main()` —
  "Library not loaded: @rpath/libopencv_gapi.410.dylib, no LC_RPATH's found".
  Every CLI target already carried that flag; the GUI was the one target excluded
  from it. The linker is also now given `-headerpad_max_install_names`, without
  which `install_name_tool` refuses to touch the binary at all ("larger updated
  load commands do not fit").
- **The app bundle is self-contained, so the DMG no longer needs OpenCV on the
  machine that runs it.** `tools/bundle_dylibs.sh` (new, wired as
  `make bundle-dylibs` ahead of `package` and `build-zip`) walks the load-command
  closure of the executable, copies all 182 non-system dylibs into
  `Contents/Frameworks`, rewrites every reference — in the executable and in the
  copied dylibs, including the ones they make to each other — to
  `@executable_path/../Frameworks/<name>`, thins fat slices down to the app's own
  architecture, deletes rpaths that point outside the bundle (which also stops
  the build machine's paths from being published inside a shipped binary),
  re-signs what it changed, and then fails the build if a single external
  reference survives. A released build previously depended on an absolute
  Homebrew path — `/opt/homebrew/opt/opencv@4/lib/libopencv_gapi.414.dylib` —
  that no rpath can redirect, so the DMG only ran where that exact formula was
  installed. Cost: `Contents/Frameworks` is ~227 MB, the DMG ~103 MB and the zip
  ~95 MB, against 7 MB and 6.5 MB before.

### Changed
- **Frame captures are no longer tracked, and the frames that were already in the
  repository are gone from its history.** A frame off the emulator carries other
  players' names and clan tags, the owner's village, and gem/loot counters; the
  `internal/game/testdata/corpus/` set, `internal/attack/testdata/` and
  `screen_ocr.png` / `screen_victory.png` were published that way, and a blob
  stays reachable from a public repository after it is untracked, so untracking
  alone was not a fix. `.gitignore` now lists the captures as local-only and the
  only image carve-outs left are functional inputs: the template crops, the
  appicon and logo, and `screen_defeat.png`, whose opponent-name strip was blacked
  out at the source. Every gate that reads a capture already skips when it is
  missing, so `make test` keeps its real-frame coverage on a developer's machine
  and degrades to a skip in CI. History was rewritten to drop the captures, the
  `assets/templates_captured/` and `assets/templates/debug/` grab trees, the
  dashboard screenshot, and the committed `logs/`, `attack_history.json`,
  `stats.json` and diagnostic dumps; all remote refs were force-pushed and
  `v0.6.0-beta` re-cut on the rewritten commit, so the release's source archives
  stop serving them too.
- **Docs point at the schema the picker actually writes.** `docs/formula-authoring.md`
  said the runtime projected authored points with `ApplyScreenScale` around a fixed
  860x732 frame; the live path is `ProjectUniform(display_scale)` about the frame the
  formula declares in `screen` (1280x720 for a `-live` authoring run), and
  `ApplyScreenScale` survives only for the corner-authoring tool. `_rage_inner` is
  documented as the optional pre-pin it is, with the deployer's own
  `deriveInwardLine` as the fallback.
- **Repository URLs point at `DSargent21/ClashGo`.** The release manifest's
  `-repo`, the DMG's read-me links and the helper's usage text still named
  `Ducky705/ClashGO`, which only resolves through GitHub's redirect. The DMG
  read-me also told users to configure their emulator at the retired 860x732 / 160
  DPI instead of the 1280x720 / 320 DPI device the bot runs on, and carried a
  stale version in its title. The Go module path still reads
  `github.com/Ducky705/ClashGO`; that rename is separate and untouched.

### Fixed
- **A siege machine is now placed with exactly one field tap, per battle, from
  every path that can tap.** A siege leaves the bar on its first field tap, and
  every tap after that lands on the machine now standing on the field, which
  destroys it (user-reported). "Exactly one" was a convention each call site kept
  separately, and two of them did not: the event-troop pass re-deploys any card
  the strategy did not declare — including a siege slot whose label does not match
  the strategy's spelling, and siege cards never leave a bar looking empty, so
  nothing ever closed the slot out — and the verifier's retry fires at any slot a
  previous pass reported FAILED. Both are now impossible: the field tap lives in
  one function (`TapExecutor.PlaceSiege`) that records the placement on the slot
  (`TrackedSlot.SiegePlaced`) and refuses to fire twice, selecting the card again
  is refused once the tap is spent, and the sweep, the verifier and the
  event-troop pass all close such a slot out without tapping. The refusal is
  logged as a refusal, so a future path that tries again is visible in the run
  rather than silent.
- **Rage spells are spread across the line the user drew: one on each END, one on
  the CENTRE.** 5 rage spells still go 3 on Line A and 2 on Line B, but the casts
  now sit on the line's ends instead of at 0.10 / 0.50 / 0.90 of it. The insets
  left 10% of the line unused at each end, and on the live 720p TopLeft rage line
  (140 px at display_scale 1.325) that put the three Line A casts 56 px apart —
  inside one rage's own reach, which is why the post-run report called the line
  TIGHT and the five spells landed as a single cluster. Using the whole line moves
  the same three casts to 70 px apart there and the two-cast line to its full
  121 px. The end points are the ones the deploy-band clamp moves first, so each
  cast is now pulled back onto the field (below the top HUD, above the troop bar)
  AFTER its jitter, and the deployer logs its own closest cast pair in the same
  units the report grades with, so "how spread is it" is a number in the run log
  next to the taps that produced it.
- **The deploy stops firing blind top-up taps at cards nobody measured, which is
  where most of a 17-second deploy went.** Live 720p runs of the same attack
  measured 14-17 s from the first drop to the last cast. The largest single cost
  was the troop reconcile: once a card is selected and mid-drop its count label
  animates away, so the post-deploy read comes back "label present but not
  readable" — a fact about the FRAME — and the old loop answered it with a blind
  3-tap batch, three rounds in a row: 9 extra taps at a 10-Balloon card the deploy
  had already drained, ~2 s per troop unit, and then the sweep fired its own blind
  batch at the same card. The loop now remembers whether the main pass fired a
  MEASURED count (live OCR, the orchestrator's settled battle-start read, or the
  strategy amount) and, when it did, re-reads instead of re-firing: briefly, and if
  the label never returns it stops and hands the slot to the sweep rather than
  guessing. The blind batch is kept exactly where it was written for — a main pass
  that had no measurement either. The per-unit pre-deploy capture is skipped when
  the battle-start read is trustworthy (a capture is ~300 ms of a window the user
  asked to keep under 7 s), the siege confirm's two 700 ms waits are one 400 ms
  wait, and every phase now logs its own elapsed time plus a `deploy window` line
  with the total against the 7 s goal (`DeployGoal`), so the next run reports the
  split instead of requiring it to be inferred from timestamps.
- **One config tree for every binary, so the app finally reads settings the CLI
  tools wrote.** The packaged app (which is what `wails dev` runs) resolved its
  config dir to `~/Library/Application Support/ClashGO` while the unbundled CLI
  (`pick_coords`, `attack_verify`, `resprobe`, …) used a `…/ClashGO/dev`
  subdirectory — two trees for one user, and the app only ever read its own. The
  measured display scale pinned in the CLI tree (`device.display_scale: 1.325`,
  and with it `max_attack_per_session`, `restart_on_startup`, the shell-pipe
  settings) was therefore invisible to the app: all 8 runs logged
  `display_scale_pinned=false` and fell back to the 1.9%-off derived scale, which
  is 12px at the troop bar — a whole digit glyph — so every count label read
  `count label present but not readable`. Resolution is now the same for both
  layouts, and the first `GetConfigDir` copies the legacy `dev` tree's state files
  into the unified dir (copy, never move, and never over a file that is already
  there), so an existing pin is adopted instead of lost. The calibration line now
  names the config file it read (`config_path=`), because a pin in a tree the
  running binary does not read is indistinguishable from no pin at all in a log.
- **Overlays are dismissed from evidence, so a run stops tapping the screen at
  random points.** Getting out of a dialog used to be a coordinate table: four
  blind taps across the middle of the screen for the obstacle sheet, a blind tap
  at (400,300) for whatever dialog was up, predicted reference coordinates for the
  gem/shield X, the Welcome-Back centre, the splash prompt and the
  lost-connection TRY AGAIN button, a random point inside the chest rect, the
  per-geometry TRY AGAIN pin, plus single stray taps on empty village ground
  (startup "focus click", post-attack side tap, wall-menu dismiss) and a
  blind-tap fallback in `findAndClick` that fired after every located path had
  already failed. Every dismissal now goes through one function with two checks on
  the frame it captures: the state is still on that frame (the classifier's own
  rule for it reports Matched, through the same scorer `ClassifyState` uses), and
  the thing it moves is where the action expects it — a probe the classifier
  verified, a template-located button, or the Back key. Failing either check fires
  nothing and leaves the case to the stuck-watchdog ladder, because (as
  docs/RESOLUTION.md measured) a tap on a predicted point during an animation
  lands on whatever is behind it. The overlays whose only safe exit is the cancel
  action (gem popup, shield info, obstacle sheet, chat) are closed with Back
  instead of a tap: the gem popup's primary button spends gems.
- **The chest fallback taps the rect centre, not a random point.** `randomPointInRect`
  made every Skip/Confirm/Continue dismissal a different gesture, so a failure
  could not be replayed from the log; the rect the user picked still decides where,
  but the code no longer picks a spot inside it. Pinned by a test that runs the
  same dismissal twice and requires identical taps.
- **Removed `Transport.TapRandomized`,** a second, unused implementation of a
  "random" tap (uniform ±5 px offset, uniform 50-200ms sleep) sitting one interface
  away from `Client.TapHuman`, which is the jitter and reaction delay the bot
  actually uses.
- **Heroes now deploy and their abilities fire.** A placement the game refuses
  *deselects* the hero's card — the code had assumed the opposite, so every retry
  field tap was fired at an unselected card and could never land. Measured on
  recorded frames: the card arms at ~48/255 brightness, the refused field tap
  returns it to ~86, and the sweep's identical arm+drop seconds later arms it
  again (94) and places the hero. That is why all eight hero drops across the four
  pinned sides failed at every ground they were tried on. The hero phase now
  re-arms the card once (`maxHeroRearms`) before its retry ladder, and the whole
  sequence — card-tap jitter, arm settle, drop settle — is shared with the sweep,
  which had drifted into a near-identical copy that worked while this one did not.
- **Hero retries walk OUTWARD.** With the red zone honestly rejected there was no
  alternative ground to offer, so the only retry candidate was the line's
  *midpoint* — just as far inside the base as the point that had just been
  refused. Retries now step along the user's own deploy line toward the screen
  border, which is the ground the troop pass just used.
- **A pinned side owns the hero ground.** `resolveHeroTarget` and
  `heroDropCandidates` took their hero points from the strategy formula first, so
  a "user-pinned" battle dropped heroes at the formula's mirrored x while the log
  claimed the pin was in use.
- **The 720p bar-read tests are green again.** They read the bot's runtime
  artifact (`~/Library/.../dev/last_troop_bar.png`), which every live battle
  overwrites, so they asserted counts against an arbitrary frame and went red
  after every run. They now read a committed deploy-time fixture.

### Added
- **An unreadable count label leaves a frame behind** (`debug_bar_unreadable_NN.png`
  in the config dir, 6 per battle). "Count label present but not readable" is the
  one reader outcome that costs battle time — the deployer has neither a number to
  fire nor proof the card is spent, so it fires a blind top-up batch and reconciles
  again, and three rounds of that is what turned a 10-tap balloon drop into 20 taps
  and a 26s deployment on 2026-09-27. The log line is identical whether the badge
  sits outside the searched window (a geometry bug) or inside it with digits the
  templates cannot match (a matching bug), and those need opposite fixes; the frame
  says which. The saved band is the full bar width at 2× with the badge windows the
  read searched drawn on it (red = the row measured from that frame, orange = the
  geometry fallback, yellow = the slot anchor) plus the values that produced them.
- **The picker now checks your points for you** (`Charts`/`Checks` panel, per-side
  badge on each tab, red dashed rings on the frame). It flags a point under the
  top HUD or the troop bar, a deploy point too far from every border to sit
  outside the base's no-deploy pocket, and a line too short to spread an army.
  Spells are exempt from the distance rule — a spell line is meant to reach
  inland. Answers "does it look good?" without fighting a battle.
- **Pinned points under the game's chrome are clamped onto the field**
  (`ClampToDeployBand`). A pin in the top HUD or under the troop bar was silently
  lost, since the game gives the tap to the UI instead of the battlefield; the
  bot now moves only `y` back into the band, keeping the side and the direction
  of the line you drew.
- **`cmd/pick_coords` — pick your own deploy points, per side, on the live
  frame** (`make pick-coords`). It serves a picker on `127.0.0.1:8791` showing
  the emulator screen; you click a troop line (p1 → p2), two spell lines, a hero
  point and a spell point for each of the four sides, and it writes
  `assets/precision_config.json`. The overlay draws the line **and the 15 taps
  the bot will fire** along it, and the shaded band marks the outer ring that is
  deployable on every base. Documented in `docs/PICKING.md`.

  The picker also has a one-click **Fill standard ring** button that writes a
  safe outer-ring plan for all four sides — a working attack with no clicking
  and no coordinates to reason about — plus **Clear this side**, keyboard
  `1`–`5` tool selection, `Esc` to cancel, `⌘/Ctrl+S` to save, and per-side
  merge so pinning one side never disturbs the other three.

  The tool also closes the coordinate trap that made hand-editing that file
  unreliable: the pins are authored in the file's own reference geometry
  (860x732) and scaled per axis into the live frame on load, so a raw pixel you
  type is not the pixel the bot taps. The picker converts both ways — you click
  live pixels, it stores reference pixels.

### Fixed
- **User-pinned deploy points were being discarded** (`internal/attack/orchestrator.go`,
  `hero_manager.go`, `sweep.go`, `spell_deployer.go`). The log said
  "using user-pinned deploy line" and then every troop tap went to the strategy
  formula's ground instead, because the formula substitution was unconditional;
  the strategy `offset` also pushed a pinned line up to 40% of the way toward
  the middle of the base, into the no-deploy area. With pins present the formula
  is no longer substituted, the offset no longer applies, and troops, spells,
  heroes and the sweep all use the pinned ground (`src=pin` in the log).
- **An occupied no-deploy pocket was retried along the same line instead of out
  of it** (`internal/attack/hero_ground.go`). A refused drop is a REGION: live
  2026-09-27 both heroes were refused at x=192 and the sweep's later tap at the
  SAME point was accepted only because the base had been destroyed back off it.
  Field-tap retries now step 2/4/6 tiles TOWARD the nearest screen edge first
  (three retries instead of two), which walks out of the pocket in one or two
  taps — the Dragon Duke was placed on the second retry this way and went on to
  fire its ability.
- **A hero that deployed was still reported unconfirmed** (`heroPlaced` in
  `internal/attack/hero_ground.go`, used by the hero phase, the sweep and the
  verifier). The confirmation was a delta on the card's activity ratio, but the
  same transition measures +0.08 on the Barbarian King and -0.32 on the Dragon
  Duke (their highlight changes in opposite directions), so a real drop could
  read as a failure in either direction. A card that has left the bar measures
  0.094-0.132 against 0.387-0.719 for any on-bar state, so the absolute
  consumed-card reading is now accepted as proof alongside the delta. Without
  it the ability pass skipped the hero and the sweep spent its second and last
  card tap re-placing it — zero abilities fired in either live battle.
- **The troop-count reader sampled the unit-LEVEL row, so every card reported a
  level as its count — the event card's 40 troops were deployed as 18**
  (`internal/attack/troop_counter.go`). CoC draws two numbers on a card: its
  count in an `xN` badge at the card's TOP edge (y 596..614 at 720p), and its
  unit level at the bottom-left (y 683..694). The reader measured and sampled the
  level row. On the live event bar the event card carried badge `x40` and level
  `16`; the reader reported 18, the sweep fired 25 taps, the card kept ~29 troops,
  and the run still finished "100% Deployed / SUCCESS" — the false success is what
  made the bug invisible for two runs.

  The badge row is now measured from the frame (consensus across the cards' badge-
  shaped glyphs, anchored to the bar rather than to the slot point), and only that
  row is read. The badge also gets its own acceptance numbers, measured on the real
  frames: correct badge digits score 0.650-0.809 while every piece of card art and
  every `x` prefix glyph in the same windows scores at most 0.480, so the floor is
  0.60 and the required lead 0.10 (the level font's 0.50/0.20 rejected the correct
  badge `0`, `4` and `9`, which is how `x40` and `x9` stayed unreadable).

  Which card a badge belongs to is now taken from the badge itself: it is drawn
  right-aligned against its card's right edge, so that edge identifies the card,
  and a run further from its slot than any card's badge can be is DECLINED rather
  than moved to the neighbouring card — firing the neighbour's count spends a
  whole unit batch on the wrong card. For the same reason the geometry-derived
  band is no longer a second chance for one card on a frame that measured its own
  row: on the corpus pre-deploy frame it turned a 17px-tall piece of card art at
  y 587..603 into a `2`.

- **The troop-bar slot geometry was authored in the wrong frame, so two slots
  landed on one card and the right-hand cards had no slot at all**
  (`assets/manual_slots.json`). `slot_xs` is consumed as live 1280x720 pixels, but
  the list it held (62, 134, 212, …, 798 at pitch 73) was authored against the
  860-wide reference frame and against a card 60 reference px wide. Measured on
  real battle frames, the live bar's cards are 88px wide on a pitch of ~100, the
  first at x 93. The old list therefore drifted up to 45px off the cards it was
  meant to name, put two anchors on a single card (287 and 360 both on the card at
  330, so that card's count was fired twice), and stopped at x 798 — leaving the
  rightmost cards, where the spells and the event units sit, with no anchor at all.
  On the live deploy bar one of those cards carried `x1` that the bot never read.

  `slot_xs` is now the card centres measured off real 1280x720 frames (137, 234,
  349, 450, 550, 643, 751, 848, 948, 1048, 1148), each cross-checked against that
  card's badge, and `card_width`/`card_height` are the measured live 88/84. The
  reader's anchor tolerance drops from 42 to 20 reference px with them, which is
  what it was hiding before: at ±56px a slot sitting between two cards could claim
  the further card's badge. With the anchors on the cards, the same frames read
  every badge Vision confirms — the event bar 40/10/9/5/1 with no duplicate or
  missed card, the deploy bar 10/9/5 AND the `x1` at 848 that used to fire the
  neighbouring card's `5`.

  Ground truth for the tests is now the badge row, read independently (Apple
  Vision via `tools/ocr.swift` on the row band upscaled 4-8x, cross-checked against
  the glyph structure in the pixels), and the values the corpus previously asserted
  as "confirmed labels" — 21, 12, 11, 8 at slots 433/511/584/798 — are in the tests
  as FORBIDDEN values, because they are the levels. Verified on 720p captures: the
  event bar reads 40/10/9/5/1 at the five cards that carry a badge, the deploy bar
  10/9/5/1, the corpus deploy bar 3 and 1, and no frame reports a level or a
  number no card carries.
- **An event-troop card was written off as spent mid-volley, so it kept its
  remaining troops** (`internal/attack/sweep.go`). The event army's card carries
  its count in a badge, and the sweep resolved that count once and then fired a
  volley at a time, re-reading the live card between volleys. On a fast-draining
  card the live read comes back well behind the game's own decrement (a card
  resolved at 40 read 26 after the first volley), and the sweep treated the
  smaller live number as the new target — volley 27, then 3, then it declared the
  slot spent, logged it, and walked away with troops still on the card while the
  run still reported "100% Deployed / SUCCESS".

  The resolved count is now the volley target for an event card, and a trusted
  live read may only raise it, never shrink it: `eventVolleyTarget(resolved,
  live, trustedLive)`. When the card is not visibly empty and the re-read is
  unreadable, the sweep fires the resolved count again (logged as "card still not
  empty and unreadable; firing the resolved count again") on a budget of
  `maxEventStallVolleys` (3) before it gives up, and giving up now returns FAILED
  rather than deploying — a card that never drains can no longer be recorded as
  deployed. Retry displacement also moves off-screen for event cards, not only
  spells.

- **A card the sweep had marked FAILED still counted as deployed, so a run with
  troops left could report 100% Deployed** (`internal/attack/verifier.go`).
  `GetUndeployedSlots()` excludes both `SlotDeployed` and `SlotFailed`, so a
  slot the sweep gave up on could never make `DeploySuccess` false — that is the
  exact channel by which a battle with a non-empty event card printed a clean
  success. `checkRemainingSlots` now skips only `SlotDeployed`: a `SlotFailed`
  slot whose activity ratio is at or above `EmptyThreshold` is added to
  `remaining` (with a warning that it "was reported failed but still shows
  content; counting it as undeployed"), so the visible troops make the verdict
  fail. Pinned both ways in `internal/attack/verifier_failed_slot_test.go` — a
  visibly non-empty failed slot is counted, an empty one is not.

- **A card the sweep gave up on was recorded as deployed while the bar still
  showed its troops** (`internal/attack/sweep.go`). The stall write-off fired when
  two reconcile rounds observed the same card state after a resolved count had
  been fired, and it marked the slot deployed without consulting the frame. Live
  on 2026-09-25 a TopLeft attack projected the sweep's deploy line to x=-11, the
  Electro Dragon card's taps went off-screen, its badge never moved, and the slot
  was closed out as deployed with `x8` still on the bar — the readable OCR of the
  post-deploy frame says `x8`, and the run printed FAILED only because two hero
  cards also failed. On a run whose heroes landed, that leftover would have been
  invisible.

  The frame's own emptiness measurement (`slotActivity < 0.08` — the same one the
  verifier uses, so the sweep can no longer mark deployed what the verifier would
  count as surviving content) is now the only thing that closes a card out
  (`sweepStallOutcomeFor`). A stalled card that still shows units is reported
  FAILED, which is the honest verdict: the taps went out and the card did not
  drain. Event-troop cards keep their volley budget and then report FAILED the
  same way. Pinned in `internal/attack/sweep_spent_card_test.go`, including the
  invariant that a card which is still non-empty is never deployed for any
  combination of event flag and remaining volley budget.

- **Battle results were parsed before the panel finished writing, so loot and
  the league bonus were recorded short or missing** (`internal/bot/bot.go`,
  `internal/bot/result_settle.go`). The end-of-battle panel animates: the star
  emblem paints first, then the "You got" loot rows count up, then the
  league-bonus column lands seconds later and counts up itself. The parser
  accepted the first capture in which ANY single field was non-zero, which the
  star emblem satisfies on its own — so it read the paint frame and locked in
  a 2-star victory as 0 gold / 0 elixir / 0 dark elixir / 0 bonus (run17
  18:57:14, run18 19:49:11, and 12 of the last 22 recorded attacks). The
  league bonus had read zero in every attack since the 720p geometry landed.

  Frames recorded live at 1280x720 (`tmp/live_check/anim`, `anim4`) show the
  real order — stars at +0.0 s, loot at +1.0 s, an unchanging loot read for
  the next four seconds, the bonus column at +4.8 s, bonus values still
  climbing at +5.8 s (84805 -> 268800). Two traps: accepting on "any field
  non-zero" reads the emblem's paint frame, and accepting on "two captures
  look the same" reads the five-second stretch where the loot is complete and
  the bonus column is not drawn yet.

  `resultSettle` now waits at least 10 s (based on the longest measured
  8-second bonus animation plus margin) and accepts only a plausible full read
  that repeats across consecutive captures. League gold and elixir bonuses must
  be present and equal; suspiciously large resource values are rejected rather
  than credited. A genuine all-zero defeat is accepted only when panel pixels
  stay still after the observation window. If no credible read settles within
  the bounded 28-capture budget, the report is marked unparsed instead of
  banking partial or implausible loot. Panel-pixel stability is not required
  for nonzero reads because the translucent panel sits over an animated
  battlefield.

  Live verification (run26, real 720p victory): held six captures with
  `bonus_gold=0`, caught the bonus mid-count at 84805, and accepted only once
  it stopped at 268800 — `bonus_gold=268800`, matching the settled frame
  exactly, and the session totals counted it (gold 960221 = 691421 + 268800).

- **The league-bonus column was clipped two digits short at 720p**
  (`internal/game/loot.go`, `assets/battle_loot_rois.json`). The bonus rows
  render a "+" then six digits and reach reference x ~702 at 16:9 — past the
  676 right edge the search zone carried, which was authored from a
  reference-era screenshot where the column sits ~25 px further left. Every
  row lost its last two digits (`+288000` arrived as `288`), the third row was
  cut out of the zone entirely, and the clipped "90% BONUS" header above it
  then filled the third slot of the bottom-three anchoring: a real bonus of
  288000 / 288000 / 2160 was recorded as 90 / 288 / 288. Worse, a `+2160`
  dark-elixir bonus is indistinguishable from no bonus, so the column looked
  empty.

  The zone now runs to reference x 745, which keeps the whole row inside it
  and still ignores the header line above. Verified against the recorded
  frame: `bonus=(g288000 e288000 de2160)`, matching the independent OCR of
  the same frame glyph for glyph, with the reference-resolution victory
  fixture unchanged at 312400 / 312400 / 2310.

- **Heroes got one attempt at a tile that could never take them**
  (`internal/attack/hero_manager.go`). A live 720p run (2026-09-22, run17) had
  the Stone Slammer and all three heroes pinned to the SAME reference point —
  the formula's per-corner override puts every hero and the siege on one
  (x,y) — so the siege dropped there first and each hero fired its single
  attempt at an occupied/illegal tile one second later. One attempt per
  battle per failure mode was how every hero ended up on the sweep's plate,
  and the sweep recovered only the heroes its random line point happened to
  clear. The evidence: the Archer Queen and Grand Warden post-drop ratios went
  UP (+0.16 / +0.22), the signature of a rejected drop leaving the card
  selected, while the sweep's Warden succeeded (delta 0.56) from a random
  deploy-line point — proving the drop-site, not the hero, was the problem.

  `deploySingleHero` now retries ONCE on displaced ground: a fresh random
  point from the battle's validated deploy line (the same ground every live
  sweep success came from), with its own pre/post ratio pair so the retry's
  delta measures only the retry. Live verification (run18): the Archer Queen
  became the first main-phase hero confirmed deployed (delta 0.267), and the
  retry demonstrably fires on line ground for the heroes whose pin failed.

  `attacklog` also now recognizes the sweep's `hero sweep deploy succeeded`
  line — run17's Warden deployed via the sweep yet the report still called it
  UNVERIFIED — plus the displaced-retry success and failure lines.

- **Rage sub-lines separated at the midpoint, not at the casts**
  (`internal/attack/spell_deployer.go`). The guard that pushes a Rage Spell's
  inner line away from the outer one checked only the lines' **midpoint**
  distance. A live 720p run (2026-09-22) had a user-pinned `_rage_inner` rotated
  a few degrees against the outer line: the midpoint guard pushed it to a 56 px
  midpoint clearance, but two non-parallel lines **converge** toward their ends,
  and the inner line's far cast sat 17 px from the outer line's far cast —
  outer (505,517) vs inner (512,501) — so 2 of 5 Rage casts shared ground and
  the attack report called the line OVERLAPPED. Midpoint distance bounds cast
  distance only when the lines are parallel, and nothing about a free-hand
  pinned line preserves parallelism.

  The guard is now cast-level (`ensureInnerCastsClearOuter`): it models the
  casts each sub-line will actually fire (outer at the conservative ceil/2
  density, inner at its own count) and shifts the inner line inward until every
  inner cast clears every outer cast by the full cast chord — the same bar
  `distributeAlong` holds along a line, including the worst-case per-axis
  jitter. The auto-derive is sized from the same chord so a parallel derived
  line starts compliant and the guard has nothing to do.

  Live verification (run17, same 720p device): Rage `5 taps, closest pair
  64 px → tight` (run16: 17 px OVERLAPPED). Pinned by
  `TestEnsureInnerCastsClearOuter_RotatedInnerLineConvergingAtEnd`, whose
  fixture asserts it reproduces the convergence before it asserts the fix.

- **A ring of N spells put every cast on the same ground**
  (`internal/attack/spell_deployer.go`). The ring radius was decided in three
  places and none of them could guarantee the one thing a cast ring exists for:
  two casts a minimum spacing apart. A live 720p run's 8-cast Ice Spell ring
  measured a closest pair of 41 px against a 50 px bar — all eight freezes on
  ground another already covered, which is one freeze cast eight times.

  - *The spacing rule and the ceiling disagreed.* A ring radius is
    `chord/(2·sin(π/n))`, which for 8 casts at the minimum spacing is 63 reference
    px, but the radius was capped at 45. The cap cut the radius to 60 live px and
    the chord to 45, below the bar. The comment justifying the cap claimed it
    "only bites for a formula that asks for a dozen-odd casts"; the run that
    exposed it was eight. The ceiling is now 120 reference px (16 casts at the
    minimum spacing) and it logs when it does bite, because a clamp that silently
    breaks an invariant is how this shipped.
  - *Both point-target rings used a hardcoded 18 px radius.* The chord falls as
    the cast count rises, so 5 casts sat 28 px apart and 8 casts 18 px apart. Both
    now size the ring from the spacing rule.
  - *The chord did not account for jitter.* Jitter is drawn per axis, so two
    neighbours can lose up to `2·√2·j` between them, not `2j` — a ring sized for
    `spacing + 2j` does not hold the spacing it looks like it holds. Rings are now
    sized from `ringChord(jitter)`, which does.

  All three ring sites now call one helper, and the tests assert the invariant
  (closest pair clears the bar) instead of a magic radius. Live after the fix:
  Ice Spell `16 taps, closest pair 56 px → tight`, with the run log showing
  `count=8 min_spacing=50 ring_chord=58 ring_radius=79` where the radius used to
  be clamped to 60.

- **Troop cards whose count label sits on bright art read as "spent", so their
  troops were never placed and the run still reported 100% deployment**
  (`internal/attack/troop_counter.go`, `internal/attack/live_count.go`). Two
  live 720p attacks lost Electro Dragon and Balloon this way. The saved pre-deploy
  frame shows both labels plainly on screen — the Electro Dragon card carries a
  clean `9` — while the reader returned a measured zero for them:

  - *The extraction masks could not separate a glyph from the art it sits on.*
    The label is drawn near-white over a dark outline, but the sprite behind it is
    bright too, so both existing bright masks welded glyph and artwork into one
    band-sized component that every size filter rejects; the dark mask isolated
    the outline instead and reported it several px taller than the glyph. A fourth
    hypothesis now cuts at the label fill's own level (`labelFillCut`), added last
    so it can only contribute candidates no other mask found and the cards that
    already read keep their higher-confidence detections.
  - *The crop above the label bridged glyph to art.* The band was taken a full
    slack above the measured row, which on these cards added the one row of
    artwork that touches the glyph. The reader now re-reads a row with a tight
    crop when the wide band finds nothing, instead of concluding the card is empty.
  - *An unreadable label was downgraded to a trusted zero.* A card showing a label
    the digits could not settle fell through to the frame-level probe ("is any card
    in the bar readable?"), which on a bar where 3 of 10 cards read answered yes —
    and the zero then drove the sweep's zero-streak rule to log "card is spent or
    locked — marking deployed" on a full card. `liveCountVerdict` now keeps that
    case unknown, so the card can never be written off on it.

  Live result (same army, before → after): `counts_agreed` 3 → 4,
  `measured_zero` 7 → 3, and Balloon and Electro Dragon both deploy and are
  confirmed drained instead of being written off while still full. Reading the
  frame's pixels by hand also exposed a real `8` on the last slot that an earlier
  test had asserted absent — that assertion encoded the reader's blindness, and
  is now the measurement it should have been.

- **Every multi-digit troop count was truncated to its first digit — and the
  whole bar read as blank on a live attack** (`internal/attack/troop_counter.go`).
  Three defects in the count reader, found by pointing it at a real 720p attack:

  - *Glyph-run assembly used the wrong axis.* Whether two glyphs belong to one
    number was decided with `image.Rectangle.Intersect`, which needs an overlap on
    **both** axes — but the digits of one label sit side by side, so their x ranges
    are disjoint by construction. Every pair was judged "not on the same row", the
    number was never assembled, and `21` was read as `2` while `16` was read as
    `1`. Those truncated numbers were then used as tap counts.
  - *The label row was anchored to a slot point that can be 11 px out.*
    `manual_slots.json` carries a reference-space `slot_y` that the slot manager
    uses verbatim (682 at 720p) while the geometry derives 671, and the label row
    is physically at y 683..694 in both cases. Anchored blindly, the search window
    started inside the glyphs, every component was clipped by the window edge, and
    a live attack logged `troop count read readable=1 slots=10` with the one hit
    coming from a fallback band. `DetectCounts` — the only call that sees the whole
    bar — now measures the row from the frame itself by requiring two or more
    distinct cards to show count-like glyphs on the same row, remembers it, and
    every later per-slot read (deploy reconcile, sweep, verifier) reuses it; the
    geometry's row remains a second candidate. Same attack after the fix:
    `readable=8 slots=11`.
  - *Anything on the card could be read as a number.* Art blobs taller than the
    font were accepted (one scored 0.53 as a `4` on a card whose real label was
    never extracted), a component that merely hung near the row counted as a
    count, and a glyph scoring at the confidence floor with a large margin was
    accepted even though its runner-up was one digit away. The accepted glyph
    height is now capped at the font's own height, candidates must sit on the
    label row (measured, not assumed), and a glyph is accepted only when its lead
    over the runner-up is decisive or its absolute score is above the level at
    which every measured ambiguous reading sits. A card that shows a count-shaped
    glyph the reader cannot settle is now reported as **unknown**, never as a
    measured zero — `0` means "no label here", i.e. the card is spent.

  Gated by `TestLiveBarReadsAtAnySlotAnchor` (a real live frame, read at both
  anchors), plus tests for the glyph-run axis, the window at both anchors, the
  size ceiling, and unreadable-label-is-unknown.
- **Two verification tools reported a geometry the bot never runs** (`cmd/see`,
  `cmd/attack_verify`, `internal/config`). Both mapped reference coordinates
  with a display scale derived from the screen diagonal instead of the one the
  bot pins in `device.display_scale`, so `attack_verify` drew its planned-tap
  overlay up to 30 px away from the live tap points — approving plans the bot
  could not execute — and `see` read four of five real-frame corpus fixtures
  as classifier bugs (the derived 1.3004 against the pinned 1.325 flips four
  of the five battle fixtures between their correct state and `Unknown 0`).
  `config.PinnedDisplayScale` is now the single source of truth: both tools
  use it, both print which geometry their output describes, and passing `-k`
  over a pinned value is reported as a departure from live behaviour rather
  than silently honoured.
- **The deploy path projected formula taps with the wrong law**
  (`pkg/formula/auto.go`, `internal/attack/orchestrator.go`). Per-unit deploy
  coordinates were mapped by `ApplyScreenScale`, which stretches x by
  `physW/860` and y by `physH/732` — at 1280×720 that is x 1.4884 against
  y 0.9836, a **−33.9% aspect drift**. The game does not do this: `resprobe`
  measured the world as a *uniform* scale about the viewport centre, and the
  same session's top-right storage probes matched at live `(1240,46)` and
  `(1240,127)` for reference y 35/116 — vertical scale 1.325, not 0.9836. So
  every authored deploy point sat **19–76 px** (one to two village tiles) off
  its building, in the direction that hides itself: a tap lands on ground, the
  unit walks, and nothing looks like a failure. `Formula.ProjectUniform`
  applies the measured law (`phys_center + (ref − ref_center) × k`) and the
  orchestrator now uses it with the calibration's own `k`, so the deploy
  geometry and the classifier share one measurement. Legacy
  `ApplyScreenScale` remains only for `cmd/design_attack`'s authoring
  preview.
- **Deploy points can project outside the tappable band at *either* edge, and
  nothing said so** (`pkg/formula/auto.go`, `internal/attack/deploy_line.go`,
  `internal/attack/orchestrator.go`). `Formula.ClampY` now pulls them into the
  band the deploy code is already willing to tap — `[yTopMin, uiCutoff]`, the
  same range `DeployLineCalculator` refuses to place line points outside, with
  `YTopMin()` exported so the renderer and the clamp cannot keep two private
  ideas of the band — and the orchestrator logs one warning naming the count.
  Measured on the shipped `auto_edrag_rush` formula at 1280×720: **3 of its 14
  tap points are outside the band on a normal bottom-corner attack** — Balloon
  (y 645) and Electro Dragon (y 644) project under the troop bar at 612, and
  **Dragon Duke projects to y 108, behind the top resource HUD**. The
  bottom-only clamp this replaces had been hiding the first two behind a
  y-squash; the top edge was misfiled as harmless ("no authored point has ever
  needed it") until the new deploy gate measured otherwise. Mirroring for a
  **Top** corner reflects those same points to y 75–76, so the top bound is
  load-bearing there too. A tap in either place hits chrome and the unit never
  deploys — the "it didn't use all the troops" report — so the warning tells
  the operator to re-author the points for this geometry.
- **The post-attack evidence frame was annotated in the wrong geometry**
  (`internal/attack/orchestrator.go`). `dumpAttackEvidence` reloaded the
  formula, re-applied the corner override (a third copy of that precedence),
  re-projected it with the legacy per-axis `ApplyScreenScale`, and judged the
  result against a band of its own invention (6%–78% of the height). Measured
  across two real 720p attacks: the run whose taps were all inside the deploy
  band was reported as `taps_outside_band=2`, and the wrong projection put the
  markers up to 76 px from the taps that were actually fired. This is the
  artifact a human reviews after a bad attack, so the one thing it must not do
  is disagree with the run. It now annotates the formula the deploy path used —
  corner-resolved, projected at the live display scale, clamped — draws the
  band into the image, and logs `taps_in_band` alongside `taps_outside_band`.
- **The deploy-geometry gate modelled a path the bot never takes**
  (`internal/attack/deploy_geometry_test.go`). It mirrored the formula for
  every corner, while the orchestrator only mirrors when the corner has no
  authored override — and the shipped formula carries overrides for all four.
  The gate therefore measured coordinates nothing would tap, and its numbers
  did not match the live run. The precedence now lives in one place,
  `applyCornerUnits`, used by both the orchestrator and the gate, so they
  cannot drift. The corrected gate reproduces the live run exactly: TopRight
  3 points outside the band (balloon y=26, electro dragon y=25, rage y=43),
  BottomRight 3 (dragon duke y=108 above; balloon 645, electro dragon 644
  below), BottomLeft 3, TopLeft 4.
- **`cmd/screendump` defaulted to a geometry the bot does not run**
  (`cmd/screendump`). It derived the display scale from the screen diagonal
  when `-k` was absent, which at 1280×720 reports a village it recognises
  (`MainVillage 260`) as `Unknown 0` — the same 2% trap already fixed in
  `see` and `attack_verify`. It now resolves the pinned scale through
  `config.ResolveDisplayScale` like the others, prints the effective `k` and
  where it came from, and gained `-derive` to reproduce the derived-value
  comparison in `docs/RESOLUTION.md` explicitly.

### Added
- **`attack_report` — a post-attack account of what the deploy phase actually did**
  (`cmd/attack_report`, `internal/attacklog`, `make attack-report`). The two
  existing harnesses cover the moments either side of this one: `attack_verify`
  reviews the *plan* before a battle is spent, `see` reports what the bot
  *perceives* in a frame. Neither looks at what the run did, and using one to
  answer the other's question is how a failed attack gets called a success — on
  a real 1280×720 run the log said `all units successfully deployed` and
  `remaining=0` while, 20 lines earlier, it recorded the Barbarian King and
  Grand Warden failing, the Balloon and Electro Dragon reconciles exhausting and
  then being written off on a zero troop-count OCR reading *against a slot the
  same check called still non-empty*, and five Ice Spells tapped 11 px apart.
  `attack_report` reads the run's own log and prints a verdict per unit —
  `deployed`, `FAILED`, `UNVERIFIED` or `not in bar`, each with the reason the
  log gives — plus the spell placement geometry (closest pair, span, and how
  many casts landed on ground another cast already covers). It exits non-zero
  when the log contradicts a success, so it can gate a script. `UNVERIFIED` is
  the category that matters: a unit whose completion the bot could not establish
  is exactly the one a human later reports as "it didn't use all the troops",
  and nothing was surfacing it. Unit names are folded case-insensitively — the
  same card is logged as `balloon` by the slot manager, `Balloon` by the planner
  and `balloon` by the sweep, and treating those as three units splits one
  failure across three rows and understates it.
- **A real-frame classifier corpus** (`internal/game/corpus_test.go`,
  `internal/game/testdata/corpus/`, `testdata/corpus.json`). Eleven captured
  frames across **both** geometries — at 1280×720 the village twice, the boot
  logo, three battle views, the victory overlay and the Star Bonus popup; at
  the 860×732 reference a victory and a defeat overlay and a battle view, so
  the geometry the live 5/5-attack run was validated at cannot be regressed by
  this work. The fixtures are carved out of `.gitignore`'s blanket image rule —
  without that they were ignored, and since a missing fixture is a *skip* rather
  than a failure the gate would have reported success in a fresh clone while
  proving nothing. Each fixture carries the state the bot must report and the scale
  it must be evaluated at (per fixture, because pinning a scale onto a
  reference-sized frame resizes the template PNGs and breaks every
  template-gated rule), checked by `go test ./internal/game/ -run RealFrames`
  in about two seconds. Every other classifier
  test paints probe colours onto a blank canvas, which proves the probes agree
  with themselves and nothing else: a rule whose prototypes were measured off
  the wrong pixel of a real screenshot passes all of them and still fails in
  battle. The harness is unit-tested for its own pass/fail/skip logic, every
  fixture must be referenced by the manifest, and a mismatch prints the
  `see look -why <State>` command that explains it. Setting the manifest to
  the derived scale fails 4 of 5 frames, which is the regression it exists to
  catch.
- **`see` — the debugging agent's eyes on the emulator** (`cmd/see`,
  `internal/vision/render.go`, `internal/vision/ocr.go`,
  `internal/vision/store.go`, `docs/SEEING.md`). A coding agent can read text
  but not pixels, which made every visual question — *did the popup close,
  where did the spells land, why is this frame Unknown* — cost a round trip
  through a human with a screenshot. `see` closes that loop with three views
  of one frame: **what the bot perceives** (the same classifier, calibration
  and button finder the bot runs, plus every recognised string with its
  bounding box), **what it looks like** (a character render ruled in
  screenshot pixel coordinates, so any glyph maps back to a tappable point;
  `-rect x,y,w,h -cell 1` zooms enough to read a button's label), and **what
  changed** (`diff` against an earlier, recorded or live frame — the direct
  answer to "did my tap land"). `rec`/`timeline`/`show` keep a bounded
  on-disk ring buffer so a run that already happened can still be reviewed,
  and `-rules`/`-why <State>` explain a state verdict probe by probe. Never
  taps.
- **`Classifier.EvaluateRules`** (`internal/game/classifier.go`) scores every
  state rule and returns a verdict per rule — the effective evidence bar, the
  pixel and template outcome, and (on request) every colour probe's reference
  point, mapped point, wanted colour, sampled colour and distance.
  `ClassifyState` is now defined in terms of it, so a diagnostic cannot drift
  from the classifier it explains: the first version of `see -why` re-derived
  the rules and reported "should have matched" for frames the classifier was
  right to reject, because it did not know that a multi-probe rule's bar rises
  from 1 to 2 outside the reference geometry. `see -why` also reports a
  uniform brightness shortfall across failed probes, which names a translucent
  dimming overlay (a popup) as the cause instead of sending the reader after a
  geometry error.
- **Live action-button location — overlay panels are found, not predicted**
  (`internal/vision/findbutton.go`, `internal/bot/bot.go`). `FindActionButton`
  locates a panel's primary button in the live frame (widest bright saturated
  gold/green/blue blob with a button-like aspect and a solid fill inside a
  caller-supplied window), so a 16:9 panel needs no per-geometry coordinates.
  Measured on 21 live frames: the army sheet's Attack! face at `(728,536)` on
  the 860×732 reference and `(1131,641)` at 1280×720, the reward dialog's Okay
  at `(640,461)`, and nothing at all on village, army-camp, search and battle
  frames. The pre-battle chain now runs as verified steps away from the
  reference geometry: tap Attack, wait for the menu's located button and press
  it, wait for the sheet's located button and press it — tapping only what the
  frame shows. This replaces two failure modes measured live at 1280×720: a
  panel that reflows (no anchor predicts it — the sheet's button is 180 px
  below the mapped point) and a blind tap fired into a panel that is still
  animating in, which landed on the village behind the menu and closed it
  again, stalling every run.
- **A deploy-geometry gate** (`internal/attack/deploy_geometry_test.go`). The
  classifier has a real-frame corpus; the deploy path — the only part of the
  bot that spends resources — had no test at all, so a projection regression
  could only be found by attacking. These tests load the *shipped* formula,
  mirror it into each of the four corners, project it at 1280×720 with the
  pinned `k`, apply the same clamp the orchestrator applies, and assert every
  tap lands inside the tappable band (using the package's own `YTopMin()` and
  `uiCutoff` so the test cannot disagree with the code). They fail on the
  clamped plan, log the pre-clamp debt so it cannot grow unseen, and found the
  three broken units above on their first run.
- **`cmd/screendump -buttons [-buttons-frac fx0,fy0,fx1,fy1]`** prints the
  located primary button for a frame or a live capture, and **`-tap x,y
  -settle <dur>`** drives the game by hand through the repo's own ADB transport
  (live capture and taps no longer shell out to the host `adb` binary, which
  fails outright when a second device is attached and cannot reach a wedged
  BlueStacks WindowManager).

### Added
- **Display-geometry migration, phase 1 — resolution-independent calibration**
  (`internal/game/calibration.go`, `internal/game/classifier.go`).
  `Calibration` now carries the game's display scale `K` and maps reference
  coordinates through the layout the game actually renders: `AnchorEdge` for
  HUD chrome (buttons, bars, top-left resource icons), which keeps its
  distance to the nearest screen edge, and `AnchorCenter` for centred
  overlays, world coordinates and full-screen art. `ClassifyState` maps every
  probe through the live calibration and matches templates on the raw frame
  with the scaled-template cache, sweeping `k×0.9 .. k×1.1` — identical to
  the previous `0.9–1.1` sweep at the reference geometry (verified live: same
  state, same score, same per-rule evidence), while a scaled geometry now
  matches instead of silently failing. The per-frame
  `ResizeToHeight(732)` is gone. `cmd/screendump -anchors` dumps every rule
  anchor under both models with the sampled colour and a hit/miss verdict —
  the verification instrument for the remaining phases.
  See [`docs/RESOLUTION.md`](docs/RESOLUTION.md) for the measured evidence that
  the HUD follows that model while overlay panels reflow, which is why the
  pre-battle chain now locates its buttons instead of predicting them.
- **`cmd/resprobe`** — measures how the CoC HUD and world scale between two
  framebuffer geometries by template-matching a reference patch over a grid
  of (scaleX, scaleY) pairs. It is the instrument behind
  [`docs/RESOLUTION.md`](docs/RESOLUTION.md).
- **`docs/RESOLUTION.md`** — the measured display-geometry law (world scales
  *uniformly about the viewport centre* by the screen-diagonal ratio; the HUD
  is edge-anchored at the same scale; density is irrelevant), why the current
  single-affine reference model cannot survive an aspect change, and the
  step-by-step migration plan for adopting a real 720p phone geometry, plus
the phase-1 status (what is landed, what is verified, what remains).

- **Display-geometry migration, phase 2 — pin the scale, raise the bar**
  (`internal/game/calibration.go`, `internal/game/classifier.go`,
  `internal/config/config.go`, `internal/bot/bot.go`). `Calibration`
  gained `SetDisplayScale` and `DeviceConfig` a `display_scale` field, so a
  scale measured with `cmd/resprobe` can supersede the diagonal-ratio
  derivation: at 1280×720 the derivation is 1.9% low, and that was enough for
  the village's elixir-icon probe to miss and drop `MainVillage` to 1 of 7
  passing anchors. With the measured scale pinned, real village frames score
  260 — the reference score — at 1280×720, 1600×900, 1920×1080 and 1290×1098.
  Off the reference geometry a rule with several probes and `MinPass == 1` now
  needs two passes, so a single accidental mapped hit cannot outrank a state
  (measured: one `ObstacleDialog` probe was enough at 1920×1080); single-probe
  states keep their bar. The bot logs a warning at boot when the geometry is
  not the reference, naming the pin and `docs/RESOLUTION.md`.
- **`cmd/screendump -k <scale>`** — runs the anchor dump and classifier under a
  pinned display scale, which is how a `display_scale` value is verified
  against a live frame.
- **`cmd/tplprobe`** — locates one stored template on a frame across a chosen
  scale sweep and prints the ranked matches (confidence, centre, scale). Built
  to answer "is this button where the mapping says it is, and at what scale is
  it drawn" against live frames.
- **`cmd/wmresize`** — changes or resets a running device's display geometry
  through the repo's own wedge-safe ADB transport, which matters because the
  host `adb shell` returns `error: closed` on this BlueStacks instance while
  SurfaceFlinger still serves frames. Reports both `wm size` and the
  authoritative screencap header.

### Fixed
- **No battle result screen could be classified, so no session could finish a
  single attack at any geometry** (`internal/game/classifier.go`). The
  `StateBattleEnd` probes were authored against an older overlay: two of its
  three anchors (a white header at ref `(430,120)` and a light blue-gray band at
  `(430,180)`) sample the *dimmed battlefield* on the current result panel, and
  `StateReturnHome`'s single anchor sat 141 px left of the RETURN HOME button
  the game draws. `WaitForBattleEndCtx` polls for those two states, so a finished
  battle spun until its 4-minute deadline, restarted the game and repeated —
  which is what the observed "stall detected but End Battle button not visible"
  loop after 81% destruction actually was. The anchors were re-measured off a
  live defeat overlay and now sit on the panel's opaque gold star/bonus band
  (both flanks of the damage banner, which is also what separates the result
  overlay from the connection-lost dialog) and on the button face at
  `(431,600)`. Verified live: the reference geometry completes **5/5 attacks**
  in one session with loot parsed and a clean shutdown. Regression coverage:
  `internal/game/classifier_result_test.go`.
- **The result overlay still classified as `Unknown` at 1280×720, so a battle
  ended by hand instead of returning home** (`internal/game/classifier.go`).
  The re-measured probes above are reference-geometry pixels, and the
  centre-anchored mapping moves them off the features they were authored
  against: on a live 720p defeat overlay the gold star/bonus band renders 30 px
  lower and darker (a translucent panel over a dimmed battlefield) and the
  RETURN HOME face 24 px lower, below the probe. `WaitForBattleEndCtx` therefore
  never saw a terminal state: it logged "stall detected but End Battle button
  not visible" once per stall tick for the whole 4-minute deadline, returned
  false, and the session restarted the game instead of finishing the attack.
  Each rule now carries three additional probes holding the same features as
  measured at 720p, expressed in reference space as the inverse of the centre
  mapping at the pinned display scale (`k=1.325`): gold band at ref
  `(249,223)`/`(521,265)` and button face at ref `(400,577)`, which map to live
  `(400,171)`/`(761,226)`/`(600,640)`. Adding checks can only widen a state's
  evidence, so the reference geometry keeps its 3-probe bar. Deliberately one
  green and two golds: the post-battle Star Bonus popup draws a green button
  where RETURN HOME sits, and a second green would let that popup reach the bar
  alone. The stall log is also throttled to one Warn per stall episode (then
  Debug), because an unreadable result overlay used to bury the log for the
  entire wait. Verified live at 1280×720: the wait exits on the result overlay,
  the result is parsed (`gold=685337 stars=1`) and the session returns home and
  shuts down cleanly on its attack cap. Regression coverage:
  `internal/game/classifier_result_test.go` (`720p` cases).
- **`make build-cli` produced a binary that aborted at spawn** (`Makefile`).
  The CLI links the same `@rpath` OpenCV dylibs the test binaries do, so it needs
  the same `-Wl,-rpath,$(OPENCV_LIBDIR)`; without it every run died with
  `Library not loaded: @rpath/libopencv_gapi.410.dylib` before printing a single
  line. Scoped to the CLI target, because the packaged GUI bundle must not bake a
  developer machine's library path into it.

### Changed
- **Pinpoints declare one anchor per axis, measured instead of inferred**
  (`internal/bot/bot.go`). `villagePinpoints` entries carried a single
  `Anchor`, so the classifier's HUD band heuristic picked the vertical anchor
  from the widget's reference y — right for the village HUD, wrong for the
  attack menu, whose buttons keep their **left** offset and are **centred
  vertically** (live, same menu at both geometries: `Find a Match` at ref
  `(161,482)` renders at 1280×720 `(217,515)`; the bottom-edge model put it at
  `405`, 110 px above the button, so the search never started). Each entry now
  declares `XA`/`YA`, re-authored from that measurement. Reference-geometry
  behaviour is unchanged (the mapping is the identity there).
- **`docs/RESOLUTION.md`** — a "Live run, 2026-09-16" section recording the
  result-overlay finding, the per-axis anchor measurement, the 720p run's
  measured blocker (a `SHINY ORE METEORITE` modal that the zoom-out pinch opens
  at 720p because the world scales about the centre, and that
  `StateObstacleDialog` does not detect at 720p), the state-by-state order that
  unblocks it, and the observation that the stored button templates are ~3.3×
  the reference scale and match nothing live (the pinpoints are why the flow
  works regardless).

### Notes
- The device geometry is **unchanged** (860×732 @320) until the migration's
  remaining consumers are done. A config flip alone is not enough: it
  misplaces every classifier pixel anchor (measured live: `MainVillage` drops
  2/7 → 1/7 passing anchors, score 260 → 160) and pushes the template scale
  outside the previous `0.9–1.1` window.
- Two obvious ways of closing the remaining scale residual were implemented,
  measured and **removed**; the numbers are in
  [`docs/RESOLUTION.md`](docs/RESOLUTION.md) so they are not re-litigated:
  measuring `k` at runtime from a template's confidence peak (no template in
  the store is a usable anchor — `btn_attack`, which `MainVillage` is built on,
  peaks at confidence 0.30 at its own geometry, and the resource icons peak at
  the bottom of the sweep), and widening each single-pixel probe to a small
  window (it absorbs the residual, but turns a 1-pixel fingerprint into a
  plausible positive: real 720p village frames flipped from `MainVillage` to
  `ObstacleDialog` / `NewsSplash`).
- Remaining before a resolution flip is safe: re-author and validate the centred
  states, which **clip** at a wider aspect (a centred anchor 355 px above the
  reference centre maps past the top of a 1280×720 frame), validate every state
  at the target geometry rather than only the village, and migrate the attack
  path's geometry (button ROIs, slot bar, troop counts, loot/result panel,
  deploy points). See `docs/RESOLUTION.md`.

### Fixed
- **The hero phase could not be exercised from a hand-built `HeroManager`**
  (`internal/attack/hero_manager.go`). `heroDropCandidates` read the siege
  machine's tile through `hm.executor` unconditionally, so the two tests that
  build a `HeroManager` literal — the outward-retry ladder and the pinned hero
  point — died on a nil dereference instead of asserting the property they were
  written for, and `make test` was red on the branch. The tile is only known
  when an executor actually ran, which is the same nil check the sweep and the
  spell deployer already carry.
- **The spell contract tests pin the formula schema the picker writes today**
  (`tests/test_spell_placement.py`). Rage now ships as one `"type": "lines"`
  entry carrying its outer and inner sub-lines, and the pinpoints are authored
  in LIVE pixels with the captured frame recorded in `screen` — both changed
  with the geometry work, and the three tests still asserting a separate
  `_rage_inner` entry against an 860x732 reference header went red. They now
  take the sub-lines from either schema, require the inner one to sit closer to
  the base centre, and require `screen` to describe the pixels the points live
  in (landscape, every authored point inside it) — the mismatch that shifts
  every tap by tens of pixels.

## [0.5.0-beta] - 2026-09-16

### Added
- **Deploy budget — the deploy phase is now wall-clock bounded**
  (`internal/attack/attack.go`). `StartDeployBudget` arms a deadline
  (~2× the longest legitimately-observed full deployment) and every
  reconcile / sweep / verify loop consults `DeployBudgetExhausted()`
  between rounds. Observed live before this: a spent Earthquake card whose
  cooldown icon never reads "empty" kept the sweep re-firing taps for
  ~2 minutes *after* the battle had ended. Leftover slots are now reported
  as undeployed so the attack report shows the partial failure instead of
  a phantom success.
- **`end_at_percent` per-strategy auto-end** (`pkg/strategy/yaml_parser.go`,
  `internal/attack/attack.go`) — a strategy can declare the destruction
  percentage at which the win is already secured (e.g. `valk_spam.yaml`
  ends at 50%). In threshold mode the stall timer is deliberately disabled
  *below* the threshold so a stall can never abandon the win early, and the
  battle only ends when the red **End Battle** button is actually on screen.
- **Battle-outcome correctness** (`internal/attack/attack.go`):
  `LastDestructionPercent` latches the highest destruction read during the
  battle (monotonic, so a transient 0 as the overlay renders can't win),
  `ThDestroyed` latches the golden "Town Hall destroyed" banner from an
  optional `stall_config.th_banner_zone`, and `StarsFromOutcome` scores
  stars from the outcome (≥50% = 1★, TH = +1★, 100% = 3★) when the result
  panel's OCR fails. `ResetBattleOutcome` clears both at the top of
  `WaitForBattleEndCtx` — live, battle 1's garbage 381% read was still
  latched when battle 2's result was parsed, turning a 32% defeat into
  "3 stars".
- **`StateConnectionLost` + `StateConfirmExit`** (`internal/game/types.go`,
  `internal/game/classifier.go`, `internal/bot/bot.go`) — the disconnect
  dialog (TRY AGAIN / RETURN HOME) was misread as `StateBattleEnd`, so the
  bot tapped result-screen coordinates forever (live: 18:26 boot → 18:28
  ReturnHome fallback → 18:33 restart loop). TRY AGAIN reconnects in
  place; the quit-confirm dialog is dismissed with CANCEL.
- **Saved-army slot selection** (`cmd/`… `internal/bot/bot.go`,
  `pkg/strategy/yaml_parser.go`) — strategies declare `army_slot` and
  `selectArmySlot` clicks that recipe card in the army list (measured
  geometry: first card at ref-y 227, ~54px stacking). `valk_spam.yaml`
  targets the 4th recipe.
- **Inter-attack cooldown** (`min_seconds_between_attacks`, default 30s)
  — without it the bot re-attacked ~8s after every Return Home with
  whatever the camps held (live: three near-identical defeats in under
  four minutes).
- **BlueStacks adbd wedge workaround** (`internal/adb/transport.go`,
  `internal/adb/shellpipe.go`). In the wedged state adbd answers every
  bare command with `FAIL "closed"` while compound commands
  (`getprop x; <cmd>`) run normally. `shellWedgeFallback` rewrites the
  service string, `stripPrefixLine` drops the extra output line, and the
  exec-service path falls back to `shell:` (with a 16→12-byte screencap
  header normalizer) — the wedge survives `adb kill-server`, so this is
  the only durable fix at the transport layer.
- **`screenSize` recovery ladder** (`internal/bot/bootorchestrator.go`) —
  a failed `wm size` now escalates retry-transport → `adb kill-server` +
  start-server between attempts instead of burning the budget against the
  same wedged socket.
- **Phase-level deploy offset propagation** (`internal/attack/deploy_planner.go`,
  `pkg/strategy/yaml_parser.go`) — a phase pin like `offset: 130 # Deeper
  in for EQs` now applies to every unit in the phase (per-unit `offset`
  still wins).
- **Post-deploy spell reconciliation** (`internal/attack/spell_deployer.go`)
  — `Amount: "All"` resolves to the card's live OCR count (11 rage on one
  card, 4 EQs on another) instead of a hardcoded 5, and
  `VerifyAndReconcile` re-reads the slot afterwards and re-fires the
  difference. A no-progress stop (two identical positive reads) and a
  zero-OCR-streak bound stop a spent card from eating taps forever.
- **`cmd/wedge_probe`** — standalone live harness for the adbd-wedge
  detection + compound-command fallback.
- **Test coverage for every new decision path** — `deploy_budget_test.go`,
  `battle_end_test.go` (destruction → stars, trophy/star pixel anchors,
  defeat fixtures), `spell_deployer_test.go` (tap-level assertions + formula
  geometry), `slot_manager_test.go` (duplicate manual-label hijack),
  `testharness_test.go`, `bootorchestrator_test.go` (wm-size fallback),
  `classifier_stuck_test.go`, `formula_test.go`, `yaml_parser_test.go`, plus
  Python strategy-contract tests (`tests/test_spell_placement.py`,
  `tests/test_valk_strategy.py`) wired into `make test`.

### Changed
- **Loot OCR is content-located, not slot-located**
  (`internal/game/loot.go`) — loot rows are found by detecting bright,
  low-saturation connected components (the white/gold digits) inside a
  generous search zone and grouping them into horizontal text lines, so the
  reading no longer depends on fixed HUD geometry. Digit ROIs get relaxed
  padding for narrow columns (the League Bonus gold was read 10× low when
  the right edge clipped the trailing digit), and implausible reads are
  rejected instead of being fed to the stall timer.
- **Frame loop 10 Hz → 4 Hz** (`internal/bot/bot.go`) — during a battle the
  deploy and battle-end goroutines run their own captures at their own
  cadence, so the full-screen classify at 10 Hz was mostly redundant
  (~55ms captures + classify pinned a core). 4 Hz still catches the result
  overlay fast enough for ReturnHome and the stuck-watchdog.
- **ArmyCamp rule hardened** (`internal/game/classifier.go`) — the lone
  brown pixel anchor also passed on ordinary village frames (live:
  `(479,149) = RGB(61,53,62)`), which made `processFrame` press Back on the
  real village and open the quit-confirm dialog the bot then sat on for the
  5-minute boot grace; the rule now requires both army-overview tab-header
  anchors, and a frame that still shows the real Attack! button is treated
  as the main village and never Back-pressed.
- **Battle-search / bonus ROIs widened** (`assets/battle_loot_rois.json`) to
  match the new content-located loot reader.
- **`make test`** now runs the full contract suite — `go vet`, Go tests
  (with the local OpenCV `LC_RPATH` injected via `pkg-config`), and the
  Python strategy tests, with a graceful skip when no pytest-capable
  interpreter is present.
- **`valk_spam.yaml`** declares `end_at_percent: 50` and `army_slot: 4`.

### Fixed
- **Fabricated 3-star victories** — the destruction-rule read used to
  OVERRIDE the result panel's parsed stars, so a garbage stall-ROI percent
  (381%, 851%, 991%) turned live defeats into wins. The visual parse is now
  ground truth and the rule read is only a fallback when the panel parse
  fails outright.
- **Star art read as victory on the defeat screen** — the result-panel star
  parse is now two-pass and defeat-safe; pinned by the new
  `internal/game/testdata/screen_defeat.png` fixture.
- **Stale `manual_labels.json` entries creating duplicate unit identities**
  (`internal/attack/slot_manager.go`) — a manual label now loses to a
  template match on a *different* slot. Live, a stale "grand warden" label
  on the unidentified Dragon Duke slot stole the warden's main-phase deploy
  and the Duke was never swept (it looked deployed).
- **`wm size` wedge aborting the boot** — falls back to the live
  screencap's header for authoritative pixel dimensions (this is the same
  fix family as 0.4.0's boot fallback, now with the retry ladder above).
- **Pipe read deadline left armed after service negotiation**
  (`internal/adb/shellpipe.go`) — the idle `discardReader` inherited the
  shell-timeout deadline and marked a perfectly healthy pipe broken
  ("adb shell pipe reader stopped: i/o timeout") mid battle-end wait.

### Verified
- Full live session on BlueStacks Air (860×732, SM-G998B profile): boot →
  classify → attack search → loot OCR → EDrag Rush and Valkyrie/EQ runs →
  result parse (stars derived from destruction, defeat fixture matched) →
  graceful shutdown.
- `make test` green: `go vet` clean, all Go packages pass, 24/24 Python
  strategy-contract tests pass.

## [0.4.0-beta] - 2026-08-31

### Changed
- **Frame-hot-path template matching is single-scale by default**
  (`internal/vision/vision.go`) — `MatchTemplate` no longer blindly
  escalates every call to a 5-step multi-scale search (0.8–1.2). That
  escalation cost 5 `Resize` allocations + 5 cgo matches per call on the
  per-frame path. Callers that genuinely need density-scale tolerance
  call `MatchMultiScale` explicitly (named templates hit the scaled
  template cache). Expected effect: per-frame template matching cgo work
  drops from 5×N to N matches (N = templates checked).
- **Pooled result Mats on the classifier and loot-OCR paths**
  (`internal/game/templates.go`, `internal/game/loot.go`) — the result
  Mat for `MatchTemplate` results now comes from the shared vision Mat
  pool (`vision.GetMat`/`PutMat`) instead of `gocv.NewMat()` + `Close()`
  per template per frame. Zero result-Mat heap allocation on the
  classifier tick and loot OCR hot paths.
- **Persistent ADB shell pipe is now enabled by default**
  (`internal/config/config.go`) — `UseShellPipe: true`. Removes the
  ~100–300ms `app_process` JVM spin-up per tap during attack cycles
  (measured 30–80ms/command on USB, more on WiFi ADB). Automatic
  fallback to the legacy one-shot transport is verified
  (`routeTap` → `markPipeBroken` → legacy tap path), and pipe start
  failure logs a warning and degrades gracefully.

### Fixed
- **`--once` CLI runs now exit cleanly** (`cli.go`, `internal/bot/bot.go`)
  — main() previously blocked forever on `ctx.Done()` after the bot
  finished its final attack, so a `--once` run hung with the process
  alive after the session summary printed. `Bot.Done()` exposes a
  channel that closes on graceful attack-cap shutdown, and main()
  selects on it.
- **Boot survives a wedged `wm size`** (`internal/bot/bootorchestrator.go`)
  — when the `wm size` shell call fails (BlueStacks returning
  "ADB: closed" while SurfaceFlinger still serves frames), the boot
  orchestrator now falls back to the live screencap's 12-byte header
  for authoritative pixel dimensions instead of aborting the whole
  boot and leaving the Start button dead.

### Verified
- Full end-to-end live run on BlueStacks Air (860×732): boot → calibrate
  → attack search → loot OCR → EDrag Rush attack (100% deploy, 3 stars,
  2.1M elixir) → graceful shutdown, exit code 0.

## [0.3.0-beta] - 2026-08-10

### Added
- **Auto-pop update dialog** — when a new release is published, ClashGO
  now opens the update window by itself (no pill click needed): one-click
  *Update & Restart*, SHA256-verified download, in-place bundle swap, and
  a non-dismissible restart splash. "Later" silences it for the session;
  "Skip version" silences it permanently (`web/src/components/UpdateBanner.tsx`).
- **One-command release publishing** — pushing a `v*` tag now runs
  `.github/workflows/release.yml`, which builds the macOS zip, DMG, and
  `latest.json` on a fresh runner and publishes them to a GitHub Release
  automatically. Uses only the built-in `GITHUB_TOKEN` — no personal
  access token ships in the app, the repo, or the artifacts.
- **Autonomous-run resilience hardening** (all live-verified on BlueStacks Air):
  - **Emulator-death recovery ladder** — when screen capture dies mid-session
    the bot now escalates transport reconnect → adb-server reset →
    BlueStacks relaunch → boot re-orchestration instead of force-stopping
    the game against a dead transport and spinning forever
    (`recoverEmulator` in `internal/bot/bot.go`).
  - **Panic containment** — recover guards around the capture frame loop and
    the attack-sequence goroutine, so one bad frame or missing asset can no
    longer kill the whole process.
  - **Nil-template fallback** — the template store degrades to an empty
    (non-nil) store when assets can't be resolved, so loot/battle OCR keeps
    running instead of panicking mid-attack (`internal/game/templates.go`).
  - **Process keepalive** — `tools/run_bot_keepalive.sh` respawns the CLI
    bot ~10s after ANY exit (panic, OOM, kill) and pins
    `CLASHGO_ASSETS_DIR` so templates/strategies resolve even under launchd
    (whose default cwd is `/`).
- **Web UI overhaul** — polished dashboard console (severity chips,
  text filter, copy, match highlighting, pause-on-hover auto-scroll),
  config view with live range validation + `role=switch` toggles + keyboard
  accessible combobox, two-step armed reset confirm, ADB status pill with
  semantic colors, analytics donut, dark-mode toggle, and an a11y pass
  (`role=log`, focus-visible rings, `prefers-reduced-motion`).
- **Human-gesture input layer** (autonomous-hardening iteration 1):
  - `adb.SwipeBezier` — low-level `sendevent` quadratic-bezier swipe with
    ease-in-out velocity (accelerate → coast → decelerate) and a random
    10-20% perpendicular arc so every drag reads as a real finger; wired
    into the navigator's village camera pans; degrades to the linear path
    on devices that can't inject raw input. Unit-tested
    (`internal/adb/bezier_test.go`).
  - `TapHuman` now emits ~1-in-5 taps as a 60-130ms press-and-release
    instead of an instant down/up pair, and hesitates with a Gaussian
    reaction delay (base 250ms / σ 70ms — mass in the 180-320ms human
    band, tail ~460ms) before committing. Hot deploy loops (`TapFast` /
    `TapAsync`) are untouched and stay fast.
  - `Navigator.IdlePan` — randomized bezier camera pan + micro-pause +
    mirrored return for idle base-wandering; throttled (~18s) in the
    bot's idle-in-village path and deliberately NOT `recordActivity`, so
    the stuck-watchdog still fires on genuinely stuck idles. Unit-tested
    (`internal/game/navigator_test.go`).
  - `cmd/swipe_probe` — standalone live-gesture verification harness
    (`launcher` / `game` / `idlepan` modes) for Phase-1 style isolated
    emulator action checks.
- **Logging** — `logs/autonomous_loop.log` records each hardening-loop
  iteration's audit findings, live-verification results, and regression
  status.
- **Post-boot splash auto-handling — the bot can now boot unattended.**
  After every relaunch, Clash of Clans shows a short splash chain (the
  "ТАР!" tap-to-continue / collect screen → the CoC castle logo, which
  sits static 1-3 min while the session connects → an optional news /
  announcement splash with a Continue button) before the village loads.
  Previously the classifier misread the collect splash's orange artwork
  as `StateBattle` (1/9 pixels), the stuck-watchdog force-stopped the
  game, and every relaunch re-showed the splash — an endless
  force-stop/relaunch loop with zero attacks. Now:
  - 3 new states — `StateTapToContinue`, `StateNewsSplash`, `StateLogo`
    (`internal/game/types.go`) with classifier rules
    (`internal/game/classifier.go`) verified at 0 distance on live
    captures and 0 false-positives on the village.
  - Auto-dismiss in `processFrame` (`internal/bot/bot.go`) — taps the
    "ТАР!" prompt text (ref 450,195) and the news Continue button (ref
    403,535), each through a single in-flight goroutine guarded by the
    atomic `splashDismissInFlight` flag. Deliberately does NOT
    `recordActivity` so a genuinely-stuck splash variant still trips the
    stuck-watchdog.
  - Boot-grace in `checkStuck` — splash states get a 5-minute window
    (the logo has no progress indicator) instead of the generic 35s
    timeout that caused mid-boot force-restarts.
  - Mirrored dismissal cases in `internal/bot/wall_upgrade.go`.
  - Live-verified: restart loop eliminated (1 startup restart vs 15+),
    splash auto-dismissals, and full attacks completing with correct
    result parsing.
- **Text-based observability tooling — the bot's "eye view" for a
  terminal-only session.**
  - `cmd/screendump` — captures the live screen (or reads a saved PNG),
    runs the bot's real classifier with per-rule pixel evidence, renders
    a color-mapped ASCII layout, probes key button pixels, and
    optionally OCRs on-screen text. `-watch` loops every 3s.
  - `tools/observe.sh` — one-shot bundle: capture + classify + color map
    + OCR, artifacts saved to `obs/<timestamp>/` with an `obs/latest`
    symlink for convenience.
  - `tools/ocr.swift` — on-device Apple Vision OCR (no tesseract / no
    network); prints `x,y,w,h | text` per recognized region in
    screenshot pixel space.
  - `cmd/classify_probe` / `cmd/result_probe` / `cmd/swipe_probe` —
    standalone diagnostics for classifier state, battle-result OCR
    parsing, and human-gesture verification respectively.
  - See `docs/OBSERVABILITY.md` for the full workflow.

### Changed
- **Massive dead-code cleanup** — removed ~16.5k lines of unreachable code
  and pruned the repo tree:
  - **Deleted the NATS event-bus subsystem** (`internal/bus`,
    `internal/agent`, `internal/geo`, `internal/world`,
    `game/enricher.go` + `game/explorer.go`, `docker-compose.yml`) — zero
    production references; NATS deps dropped from `go.mod`/`go.sum`.
  - **Removed ~35 verified-dead functions** across `attack`, `bot`, `game`,
    `vision`, `logger`, `adb`, `training`, `updater`, `strategy` — incl. the
    NDJSON log mirror, `ParseCSV`, `ClassifyStateFast`, `SpellLine`,
    `isUnitSelected`, `hasColorSignature`, `WaitForSlotEmpty`, the legacy
    `SlotManager` helpers, the validator `Planner` engine
    (`internal/attack/plan.go` + its test), `game.Calibrator`, and the
    `adb.WithDeviceID` option.
  - **Deleted 48 unreferenced `cmd/` diagnostic tools** — kept the five wired
    into Makefile/scripts (`attack_record`, `capture_template`,
    `design_attack`, `release_manifest`, `test_wall_upgrade`).
  - **Dropped dead struct fields** — `app.echo`, `transport.seq`,
    `bot.lastFrameTime`, `bootorchestrator.mu`, `recovery.onApply`,
    `updater.manifest`, `troop_counter.calibrated/scaleX/scaleY`.
  - **Consolidated duplicated logic** — `app.go` stats merge extracted into a
    `mergeStats` helper; `Transport.ExecRaw` merged into `Exec`; the
    `version.go` build-date + Makefile ldflag removed.
  - **Frontend** — fixed 4 unused-variable TS errors (`App.tsx`, `main.tsx`,
    `Sidebar.tsx`).
  - **Docs + tree** — README / DESIGN / PERFORMANCE / CHEST refreshed for the
    new layout; orphaned `assets/grab/` screenshots and stale `.gitignore`
    entries removed.

### Fixed
- **BlueStacks Air cold boot burned 90s then failed** — `waitForVMProcess`
  waited for `qemu-system`/`hd-adb` process names that never appear on
  BlueStacks Air for Apple Silicon (the VM runs in-process), so every cold
  boot hit the full wait and logged a spurious "VM did not start" failure.
  An open adb port (5555+) now counts as a VM-up signal — the same signal
  the ADB-connect wait already uses (`internal/adb/emulator_mac.go`).
- **Nil-pointer panic on missing templates** — `NewLootRecognizer` panicked
  on a nil template store when assets didn't resolve (e.g. running from a
  launchd cwd of `/`), killing the bot at attack entry. The store now
  falls back to an empty store and the attack goroutine is panic-guarded.
- **Attack History lagged the loot totals** — the history entry was only
  appended after Return Home + wall upgrades (which can take minutes), so
  the dashboard showed fresh gold/elixir totals long before the new attack
  row appeared — and the entry was lost entirely if Return Home failed.
  `executeAttackSequence` now records the report and fires the UI refresh
  at battle-result parse time (`internal/bot/bot.go`).
- **League Bonus gold misread by a factor of 10** — the bonus column's
  right padding clipped the trailing digit (bonus gold "+256 000" read as
  25600): the digit lost its right edge and its template-match score fell
  below the 0.5 floor. Narrow columns now get relaxed padding
  (`internal/game/loot.go`) and the bonus ROI's right edge was widened
  (`assets/battle_loot_rois.json`). Regression-tested via
  `TestLootVictory/screen_victory` on the tracked fixture
  (`internal/game/testdata/screen_victory.png`); the former
  `screen_victory_live.png` fixture was removed for privacy (it was a live
  capture containing real player names and was gitignored, so it silently
  skipped on fresh clones anyway).
- **Endless force-stop/relaunch loop on the post-boot collect splash** —
  the "ТАР!" tap-to-continue screen was misclassified as `StateBattle`, so
  the stuck-watchdog force-stopped the game and every relaunch re-showed
  the splash. Root cause, the mapped boot-splash chain, and the fix are
  documented in the Added entry above and in `docs/OBSERVABILITY.md`.
  Verified live: restart count dropped from 15+ to 1, and the bot
  completed multiple attacks with correct result parsing.

### Fixed
- **Window close no longer freezes the app** — the async log writer now
  flushes blocked callers immediately instead of deadlocking on shutdown
  (`internal/bot/asyncwriter.go`).
- **Packaged-app Config crash** — assets are injected into the .app
  bundle for BOTH packaging paths (DMG and zip), the Config page no
  longer crashes on a nil strategies slice, and the sidebar cleanup was
  removed (`internal/paths`, `Makefile`, `web/src`).
- **`latest.json` min_supported default** — beta releases now default to
  the previous minor instead of the current version, so beta users still
  get the update banner (`Makefile`).
- **App icon + bundle signature** — ClashGO logo lands on the app icon
  and the DMG bundle is re-signed (ad-hoc) after asset injection
  (`tools/build_dmg.sh`).
- **Release workflow hardening** (all caught while shipping this release):
  - `go.sum` is now committed — it was gitignored, so a fresh CI checkout
    had no checksums and `go build` failed (`go.sum`, `.gitignore`).
  - CI pins the **`opencv@4`** formula — `brew install opencv` now ships
    OpenCV 5, which drops the `opencv4.pc` file that gocv v0.43.0 compiles
    against (`release.yml`).
  - The publish step is idempotent — re-running the workflow or
    force-pushing a tag no longer fails because the release already
    exists (`release.yml`).

## [0.2.0-beta] - 2026-08-05

### Added
- **Event-agnostic chest detection** — `StateChestReward` classifier rule
  (`internal/game/classifier.go`) now fires ONLY on the `hammer`
  ("TAP TO OPEN") template match, so dark bottom pixels on a normal
  village no longer false-trigger dismissal. Survives CoC event art swaps.
- **Template-driven Continue button** — `chestContinueTap`
  (`internal/game/chestdismiss.go`) now auto-detects a `btn_continue`
  template (`assets/templates/btn_continue.png`) and taps the matched
  point, falling back to `assets/continue_button.json` only if no template
  is loaded. Removes the need for a hand-measured Continue rect. See
  `docs/CHEST.md`.
- **Device-independent CPU metric** — `internal/bot/cputime.go` reads
  process CPU time via `getrusage(RUSAGE_SELF)` (user + system) and exposes
  `cpu_time_sec` (absolute, kernel-accurate, comparable across machines)
  plus `cpu_cores` (fraction of one core over the last sample window).
  Wired into `game.SystemHealth` + `bot.BotStats`, surfaced in the GUI
  Settings view. A non-unix fallback returns zero so the build stays
  portable. See
  [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md#cpu-metric-absolute-time-not-a-percentage).
- **Bot boot orchestration + ADB recovery module** — a self-contained
  BlueStacks bring-up path that survives the common failure modes on
  macOS Sequoia:
  - `internal/adb/bootprobe.go` — repeated `CaptureScreen` luma probe that
    verifies BlueStacks is past the boot animation (not just responsive to
    `adb shell`).
  - `internal/bot/bootorchestrator.go` + `bootprofile.go` +
    `boot_report.go` — orchestrates the bring-up sequence; profile
    selection is gated on prior launch results so re-runs don't retry the
    same known-failing step.
  - `internal/bot/recovery.go` — fallback recovery ladder:
    `softResetAndroid` (cheap, non-destructive `adb shell stop && start`)
    before the heavier `ResetAdbServer` (`adb kill-server && start-server`).
  - `internal/adb/emulator_mac_test.go` — table-driven tests for the
    single-launch `EnsureBlueStacksMac` path.
- **Context-aware ADB shell/capture wrappers** — `ShellWithContext`,
  `CaptureScreenWithContext`, `ExecWithContext` plus the `ShellRunner`
  interface. The context-aware variants force the transport closed on
  `<-ctx.Done()` so a 2s probe timeout can't hold the transport mutex for
  the full 30s budget.
- **`AutoDetectDevice` verifies BlueStacks** — no longer trusts
  `adb devices` verbatim. Opens a fresh transport and checks `getprop` so a
  stale `localhost:5555 device` row is skipped, plus a best-effort
  `adb connect localhost:5555` refresh.
- **`EnsureBlueStacksMac` is single-launch + smart pre-check** — scans
  candidate ports `5555..5565` (TCP-only, 2s timeouts), honors an existing
  healthy BlueStacks session (no kill+launch), then one
  `open -a BlueStacks.app` attempt. Removed the kill-and-retry path.
- **`ResetAdbServer` / `SoftResetAndroid`** on `internal/adb.Client` —
  exposed for the recovery ladder. Both documented with SAFETY notes
  (`adb kill-server` is global to the host Mac).
- **Wall-upgrade single-tap-each-X dismiss flow** — when a popup survives a
  single X tap, the bot does ONE additional tap on the alt X (if configured)
  instead of offset-jittering the same one. The `aborted` flag is now
  per-wall-iteration, and `defensiveDualTapAndLogClose` centralises the
  tap-both-X + uniform `close_failed` payload. `asset_driven_modal_*`
  events now carry `name + reason + primary_still_up + alt_still_up` and
  `primary_px + alt_px + all_up`.
- **In-app update system** — ClashGO now talks to GitHub Releases. On
  startup (and every 6h) a lightweight service checks for a new release,
  compares versions with a pre-release-aware semver, and surfaces a banner
  in the dashboard. `internal/updater` does ETag-cached polling,
  SHA256-verified downloads into
  `~/Library/Application Support/ClashGO/updates/`, Finder-open install
  plus a bundled `install_update.sh` in-place .app replacement helper.
  Every release ships a `latest.json` manifest alongside the zip, emitted
  by the new `cmd/release_manifest` helper. `make release VERSION=...`
  now produces `ClashGO-v{VERSION}-macOS.zip` + `latest.json`.
- **New diagnostic tooling** — `cmd/test_wall_upgrade` (Phase-logging
  harness), `cmd/adb_probe` / `cmd/boot_debug` (bring-up probes),
  `cmd/capture_template` (template ROI capture), plus `tools/picker.py`
  + calibration scripts documented in `tools/CALIBRATION_README.md`.
- **`docs/CHEST.md`** — capture + tuning workflow for chest/continue
  templates and the detection rule.
- **Classifier detection tests** — synthetic-frame guards that the chest
  rule fires on a chest screen and does NOT false-positive on MainVillage.
- **Resource-usage documentation** — README, `docs/PERFORMANCE.md`, and
  `docs/DESIGN.md` carry RAM/CPU estimates for ClashGO alone and combined
  with BlueStacks at 860×732 / 160 DPI (incl. the 15 FPS battle case).

### Changed
- **`auto_edrag_rush.yaml` switched to `target_edge: "Random"`** — picks a
  random corner per attack for an unpredictable per-attack landing side,
  ideal for unattended runs. Backward compatible: `"Rotate"` (deterministic
  cycle), specific corner names, and legacy side names still work.
- **Per-corner formula override workflow** — `Formula.CornerOverrides`
  (`map[string]map[string]UnitEntry`, omitempty) holds optional per-corner
  overrides authored via `cmd/design_attack -corner BL/TR/TL`. The
  orchestrator (`DeployDynamicV2`) merges a per-corner override with `Units`
  per-unit and uses the result INSTEAD of mirroring when present — fixing
  the "mirror lands too close to the red line on BL/TR/TL" symptom. See
  `docs/formula-authoring.md`.
- **`Formula.MirrorForCorner(targetEdge)`** — reflects every `P` / `P1` /
  `P2` / line point across the screen axes (`BottomRight: identity`,
  `BottomLeft: mirrorX`, `TopRight: mirrorY`, `TopLeft: mirror both`).
  Accepts full + abbreviated corner names plus a substring fallback.
- **`Formula.LoadFile(path)`** — loads a formula from a direct path, used
  by `cmd/design_attack -verify`.
- **`cmd/design_attack` is the one-stop authoring/verify tool** —
  `-live` captures from adb directly (no pre-saved PNG needed),
  `-verify <formula.json>` renders a 2×2 mirror grid saved to
  `verify_grid.png`, `-corner <BR|BL|TR|TL>` picks the authored corner.
  Required-flag check now fails fast before the capture.
- **Removed 5 redundant strategy files** — `top_edrag_rush.yaml`,
  `bottom_edrag_rush.yaml`, `left_edrag_rush.yaml`,
  `right_edrag_rush.yaml`, `balloon_edrag_rush.yaml` were single-side
  variants of `auto_edrag_rush.yaml`. `valk_spam.yaml` is kept (different
  attack system — FourSides Valkyrie ring).
- **Build metadata via ldflags** — the version string is injected at
  compile time into `main.version` so `bot_cli --version` and the Wails GUI
  show the same value. The Makefile picks up `VERSION` from `wails.json`.
- **Stray PNG cleanup** — removed untracked runtime debug artifacts
  (`verify_grid.png`, `last_failure.png`, `last_battle_result.png`,
  `diag_*.png`).

### Fixed
- **Chest Continue tap timing** — `chestContinueTap` now waits for the
  button to render and retries the tap (up to `chestContinueMaxTaps`,
  default 3) with a settle between attempts, because the overlay can lag
  the chest-open animation by ~1s and a single early tap classifies as a
  transitional `Unknown`. On final failure it dumps
  `debug_chest_continue_fail.png` for ROI recapture.
- **MatPool key collision (silent corruption)** — `getPoolKey` previously
  cast dimensions to a single `rune`, collapsing distinct sizes (e.g. rows
  100 vs 256) into one key and risking panics / mis-sized Mats. Keys are
  now encoded numerically (`%d_%d_%d`).
- **Frame-encode Mat leak** — the Live View encode path now closes the JPEG
  buffer on every branch and always returns the pooled half-frame Mat, so a
  failed encode cannot leak it.
- **`lastNav` global race** — moved the ArmyCamp navigation cooldown
  tracker from a package-level `var` to a `Bot` field, removing
  cross-instance state sharing / data races.
- **`Health().LastCapture` lied** — now reports the real last successful
  capture time instead of `time.Now()` on every call.
- **Attack-history wipe on read error** — the in-memory `historyCache`
  (seeded from disk at `NewBot`) is now the source of truth, so a transient
  `attack_history.json` read failure can no longer silently drop all prior
  attacks.
- **Duplicated ROI switch** — extracted `Bot.buttonROI()` so the
  wait-for-button and find-and-click paths share one definition.
- **`MirrorForCorner` failed for abbreviated corner names** — replaced
  `strings.Contains` substring matching with a switch on the normalized
  corner name covering full + abbreviated forms.
- **Mirror pointer-aliasing in `runVerify`** — `MirrorForCorner` mutates
  `*Point` in place, so 4 sequential mirrors on shared pointers collapsed
  to TL geometry. Fixed by deep-copying points and lines per iteration.
- **2×2 grid scaling in `runVerify`** — quadrants are at `w/2 × h/2`;
  `ApplyScreenScale` is applied after the mirror so deploy lines land on
  the quadrants.
- **Required-flag check happened after the live capture** — missing
  `-strategy` wasted an adb screencap before showing usage. Reordered.
- **Theme defaults to light** instead of honoring OS
  `prefers-color-scheme` — macOS dark default + `bg-zinc-950` produced an
  indistinguishable-black surface (perceived "black screen"). The Settings
  view lets users opt back into dark.
- **`safeEventsOn` defensive wrapper** — `EventsOn` no longer throws
  synchronously when `window.runtime` is undefined (the Wails runtime
  bridge isn't present during `npm run dev`), so the React tree no longer
  unmounts on the failure path.
- **`UpdateBanner` Rules-of-Hooks violation** — the SHA256-flash effect was
  placed after three early returns, so its hook index varied across renders
  and React threw "Rendered more hooks than during the previous render".
  Hoisted above all early returns.
- **Sidebar brand swap** to `clashgo-logo.png`.

### Performance
- **Audit-driven perf pass** — zero behavior changes, multiple-order-of-
  magnitude CPU/RAM reductions across the bot + UI. Full rationale +
  methodology in [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md):
  - Removed dead `b.OnFrame` EventsEmit (`live_feed`) — up to 10 FPS in
    battle state, every emit pushing a 50–150 KB base64 JPEG through
    WailsIPC to zero React subscribers. **~7–15% battle-state CPU freed,
    ~5–15 MB transient RSS eliminated.**
  - Removed `bot_log` EventsEmit in `WailsLogWriter.Write` — every zerolog
    line generated a wasted WailsIPC round-trip. **~1–3% CPU freed.**
  - Tab-gated screenshot poll — the 1 Hz interval now only mounts on the
    Live View tab. **~0.1–0.3% continuous CPU saved.**
  - `vision.MatchMultiScaleROICached` keyed by rule name — fixes the
    empty-name bypass that re-allocated 3 scaled-template Mats per call
    (180 alloc/free per second eliminated). **~1–2% CPU freed.**
  - `world.MinWriteInterval` 250 ms → 1 s — 4× fewer tmp+rename flushes.
    **~0.5–0.8% CPU freed.**
  - `GetAttackHistory` in-memory cache — no more disk re-read on every
    0.5 Hz React poll. **~0.1–0.2% CPU freed.**
  - Detached heavy teardown in `StopBot` — UI Stop now returns ~10–50 ms
    instead of 1–3 s. **~95% Stop-IPC latency cut.**
- **Aggregate battle-state estimate** — **~10–20% CPU freed + ~10–20 MB RSS
  + ~40–60% GC-cycle reduction** during battle state. Idle (bot on, no
  battle) saves ~3–5% CPU. **Verify on your machine** with the recipe at
  the bottom of [`docs/PERFORMANCE.md`](docs/PERFORMANCE.md).

### Security
- **`.gitignore` covers `rotation_state.json`** — runtime state of the
  `Rotate` cycle. Not code, not committed.
- **Downloads are verified against the SHA256 in `latest.json`** before the
  apply step. Mismatches abort cleanly and the corruption file is removed.
- **macOS quarantine + ACLs are stripped** on the bundled helper before
  relaunch, avoiding Gatekeeper's "damaged" error.

---

## [0.1.0-beta] - 2026-06-12

### Added
- **GUI Dashboard**: Modern Wails v2 + React interface for remote bot management.
- **Vision Engine**: High-performance GoCV/OpenCV integration for real-time base analysis.
- **Dynamic Navigator**: Dijkstra-based state traversal for reliable UI navigation.
- **Anonymization Layer**: Core engine now strictly excludes identifying base screenshots from diagnostic logs.

### Changed
- **Safety First**: Updated `.gitignore` and build pipelines to ensure no account-leaking data is ever committed.
- **Project Structure**: Streamlined repository layout; consolidated scripts and expanded documentation.
- **Theme**: Set Light Mode as the default initialization state for new launches.

### Fixed
- **App Icon**: Corrected missing branding in the macOS application bundle.
- **Interface Stability**: Resolved regressions in mock devices and interface satisfaction for unit tests.

---
*Initial Beta Release - Use with caution. Report bugs via GitHub Issues.*
