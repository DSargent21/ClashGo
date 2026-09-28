# Seeing the screen

`see` turns an emulator screenshot into text a coding agent can read. It exists
because the debugging loop "change code → run the bot → look at the screen →
decide" is where this project's bugs actually live, and a text-only reader has
no way to look. Without it, every visual question — *did the popup close? where
did the spells land? why is this frame Unknown?* — costs a round trip through a
human and a screenshot.

```bash
make see                              # report on the live screen
make see ARGS="look -img tmp/x.png"   # or a saved frame
make see ARGS="timeline"              # what happened during the last recording
```

`see` never taps. Reading the screen is always safe, including mid-attack, and
it goes through `internal/adb` (the bot's own transport), never the host `adb`
binary: on BlueStacks the host binary answers `error: closed`, and a second
attached device makes an unfiltered `exec-out` fail outright.

## The three questions

**What does the bot perceive?** `see look` runs the *same* classifier,
calibration and button finder the bot runs, so the report shows the bot's view
rather than a parallel one. If the report says `Unknown score=0` for a frame
that obviously shows a battle, the bug is in the classifier, not the tool.

```
=== SEE 14:52:03 | tmp/live_attack/stall.png | 1280x720 | k=1.3004 (derived) ===
STATE     Unknown score=0
BUTTON    none found [searched 0.5,0.5,1.0,1.0]
REDZONE   valid=true bbox=340,24 828x468 contours=68
          NOTE: state is not Battle: treat this zone as terrain noise, not a deployable area
TEXT      21 regions
          k (  33, 527 120x20 ) centre(  93, 537)  End Battle
          ...
```

**What does it look like?** The report ends with a character render of the
frame, ruled in screenshot pixel coordinates:

```
     0      100    200    300    400    500    600     700    800    900
     +-------------------------------------------------------------------
   0 |   .  1111111ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ
  22 | .:::33333333Z33111111      ...............:Y::::-----=+=+*==------R
```

- The left gutter is the **y coordinate** of that text row; the top ruler
  labels the **x coordinate** of periodic columns. Column *c* starts at
  `x = c * cellW`, so a glyph can be converted straight into a tap point.
- Ink is brightness: `@%#*` is bright game content (label text, button faces,
  health bars), blank is dark scenery or an empty panel.
- Letters name saturated colours: `R` red, `O` orange/gold, `Y` yellow,
  `G` green, `C` cyan, `B` blue, `P` purple, `W` white.
- Digits and lowercase letters are **marks** the report placed: every text
  region (`1`, `2`, … then `a`, `b`, …), `B` the action button, `Z` the red
  zone, `T` a `-mark` point. Each marked OCR region is listed with the same
  label, so a glyph in the picture leads straight to its text and box.
- `GRID` states the scale (`1 char = 11x22 px`). `(TRUNCATED)` means the region
  did not fit the output caps and the render covers only part of it.

**What changed?** `see diff A B` answers "did my tap land" without guessing:

```
RESULT  0.02% changed (184/921600 px), bbox (592,600)-(650,625), max channel delta 96
```

A tap that dismisses a dialog changes a large area; a tap that silently misses
changes almost nothing. `B` may be `live` (fresh capture) or `last` (the newest
recorded frame), so `see diff last live` is the "what did that do?" one-liner.
`#` in the change map marks changed cells, `.` a sub-threshold difference, and
` ` cells that are identical.

## Recipes

**A bot is stuck.** `see look` → read `STATE` and `TEXT`. Recognised text tells
you what the screen actually says; `-rules` lists every state rule's pass
count; `-why <State>` explains one rule check by check, which is what turns
"the classifier says Unknown" into "this probe lands 6px above the button face
on a panel that is dimmed":

```bash
make see ARGS="look -why Battle"
     ref(  34, 588) -> hud(  44, 533) want RGB(206, 13, 14)±50  got RGB(  7,  7,  7) d=166 miss
     ...
     0 of 9 checks pass (minpass=1) -> state would NOT match
```

**Read small text.** Zoom a rectangle; `-cell 1` is one source pixel per
character column, the finest a text render can manage:

```bash
make see ARGS="look -img tmp/run/stall2.png -rect 580,585,130,50 -cell 2 -no-render"
 613 |GGGGGGGGjjjjjjkkkkkkkkkkkkkkkkkkkkkkkkkkkkkjjjjjjjjGGGGGGGGGGGG
 621 |GGGGGGGGGGGGGGkG#@%**@@Y#@%%%+#@@*-@@@Y*%%%@kGGGGGGGGGGGGGGGGGGGG
```

**Review a run that already happened.** Recording is a dashcam: one line per
frame with the classifier's verdict and the change from the previous frame.

```bash
make see ARGS="rec -every 2s -label attack -stop-on BattleEnd"
make see ARGS="timeline -n 40"      # table plus state transitions
make see ARGS="show last -ocr"      # re-read a recorded frame later
```

Frames and the index live in `tmp/see/` (`frames/*.png`, `index.jsonl`). The
index is JSON lines, so `jq` works on it. `-keep N` bounds the disk.

**Programmatic use.** `look -json` emits the whole report (state, score, OCR
regions with boxes, button, red zone, marks, change) as JSON instead of prose.

## The display scale, and why a report can lie

Every probe is placed through the calibration, which scales reference
(860x732) coordinates onto the live screen. `see` therefore uses **the scale
the bot pins in `device.display_scale`** (1.325 on the 1280x720 device this
project is debugged on), not the ratio it can derive from the screen diagonal
(1.3004). The two are 2% apart, and on these frames that is the difference
between `Battle` and `Unknown`:

```bash
make see ARGS="look -img internal/game/testdata/corpus/battle_deploy_720p.png"            # Battle 290
make see ARGS="look -img .../battle_deploy_720p.png -k 1.3004"                            # Unknown 0
```

Passing `-k` when the config pins a different value prints a warning that the
report does **not** describe live behaviour. Trust that warning: the first
version of this tool defaulted to the derived scale and reported four of the
five corpus frames as classifier bugs, which they were not.

## The real-frame corpus

`internal/game/testdata/corpus/` holds real captures and
`internal/game/testdata/corpus.json` says which state the bot must report for
each, and at which geometry. Eleven fixtures across both: at 1280x720 the
village twice, the boot logo, three battle views, the victory overlay and the
Star Bonus popup; at the 860x732 reference a victory and a defeat overlay and a
battle view, so work done for 16:9 cannot quietly regress the geometry the live
5/5-attack run was validated at. `go test ./internal/game/ -run RealFrames`
checks them in about two seconds.

The scale is **per fixture**, not global: pinning 1.325 onto an 860x732 frame
would resize the reference template PNGs during the sweep and break every
template-gated rule. The manifest's `scale_comment` says so, and the harness
resolves the calibration from each fixture's own size.

It exists because every other classifier test paints probe colours onto a
blank canvas: those prove the probes agree with themselves, not that they land
on the right pixel of a real screenshot. The corpus is the only place a
geometry or prototype error can show up, and the manifest's notes carry each
frame's story so a failure explains itself. When you change a rule
deliberately, update the manifest in the same commit.

Adding a fixture is cheap and pays for itself the first time a state regresses:
capture with `make see ARGS="rec -label <state>"`, copy the PNG into
`corpus/`, and add a manifest entry with the state and the scale it is
meaningful at. A fixture the manifest does not reference fails the build, which
is deliberate — an unasserted PNG is worse than none, because it looks like
coverage.

## Relationship to the other screen tools

| tool | what it is for |
| --- | --- |
| `see` | agent-facing perception report: text + coordinates, zoom, diff, frame history |
| `make attack-verify` | pre-attack plan review: renders the *planned* taps, spell lines, bar slots and deploy line onto a frame |
| `make attack-report` | post-attack account from the run log: per-unit deploy verdicts and spell spacing |
| `cmd/screendump` | hand-probing a single frame: anchor dumps under both geometry models, key button pixels, colour map |
| `cmd/classify_probe` | just the classifier's score for every state |

### Before, during, after

These three cover different moments, and using one for another's question is how
a bad attack gets called good:

| moment | tool | the question it answers |
| --- | --- | --- |
| before spending a battle | `attack_verify` | where *would* the bot tap, and is that inside the band? |
| while debugging the screen | `see` | what does the bot perceive in this frame? |
| after the attack | `attack_report` | did each unit actually deploy, and did the spells land somewhere useful? |

`attack_report` reads the run's own log, so it can only report what the bot
recorded. That is the point: a unit it calls `UNVERIFIED` is one whose completion
the bot could not establish either. On a real 1280×720 run the log said
`all units successfully deployed` while, 20 lines earlier, it recorded two heroes
failing, two troop cards written off on a zero count *against a slot its own
check called still non-empty*, and five Ice Spells tapped 11 px apart. Checking
the geometry (all taps in band, `taps_outside_band=0`) is necessary and nowhere
near sufficient — the geometry harness was never going to see any of that.

```bash
make attack-report ARGS="-log tmp/live_attack/run.log"
make attack-report ARGS="-log tmp/live_attack/run.log -json"
make attack-report ARGS="-log tmp/live_attack/run.log -spell-radius 90"
```

It exits non-zero when the log contradicts a success, so a script can gate on
it. `-spell-radius` is the reach one cast covers (default 100 px, ~3.5 tiles at
this device profile); taps closer than half of it are reported as `OVERLAPPED`,
closer than all of it as `tight`. The measured spacings are always printed
beside the verdict so a reader can disagree with the threshold.

`see` is the general-purpose one: reach for it first, and drop to
`screendump -anchors` when you need the reference-geometry comparison it does
not print.

## Limits

- OCR is Apple Vision, so stylised game fonts get misread (`RefURN Home`,
  `VicTORY`). A misread is still useful evidence — it explains why pixel
  probes exist for buttons whose labels cannot be trusted.
- The red zone is only meaningful in `Battle`; outside one the heuristic
  latches onto terrain. The report says so in a `NOTE` rather than letting the
  number pass as a deployable area.
- The character render is a lossy view. It is enough to spot layout, panels,
  buttons and text presence; it is not a substitute for the annotated PNGs
  `attack_verify` and the orchestrator's evidence capture write.
