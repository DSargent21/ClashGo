# Per-Attack Formula Authoring

Every ClashGO strategy can be paired with a `formula.json` that pins exact
deploy coordinates for every unit the bot will spawn. This file is the
authoritative reference for the workflow.

## TL;DR

```bash
# 1. Build the tool (one-time)
make build-cli

# 2. Author the formula for all 4 sides (pan the camera in CoC between runs)
go run ./cmd/design_attack -live -device emulator-5554 \
  -strategy assets/strategies/auto_edrag_rush.yaml \
  -out assets/strategies/auto_edrag_rush_formula.json -corner BR

go run ./cmd/design_attack -live -device emulator-5554 \
  -strategy assets/strategies/auto_edrag_rush.yaml \
  -out assets/strategies/auto_edrag_rush_formula.json -corner BL

go run ./cmd/design_attack -live -device emulator-5554 \
  -strategy assets/strategies/auto_edrag_rush.yaml \
  -out assets/strategies/auto_edrag_rush_formula.json -corner TR

go run ./cmd/design_attack -live -device emulator-5554 \
  -strategy assets/strategies/auto_edrag_rush.yaml \
  -out assets/strategies/auto_edrag_rush_formula.json -corner TL

# 3. Verify the 2x2 grid shows your coords per corner
go run ./cmd/design_attack -live -device emulator-5554 \
  -verify assets/strategies/auto_edrag_rush_formula.json

# 4. Run a live attack
./build/bin/bot_cli -deploy-only \
  -strategy assets/strategies/auto_edrag_rush.yaml \
  -device emulator-5554 -once
```

The bot's per-attack corner is selected by the strategy's `target_edge`:
- `"Random"` (recommended) — picks a random corner per attack
- `"Rotate"` — cycles TL → TR → BR → BL via a persistent on-disk counter
- `"TopLeft"` / `"TopRight"` / `"BottomLeft"` / `"BottomRight"` — pinned
- `"top"` / `"right"` / `"bottom"` / `"left"` — legacy side names (corner-aware via `cornerToSide` mapping)

At runtime the orchestrator (`internal/attack/orchestrator.go::DeployDynamicV2`):
1. Resolves `target_edge` to one of the 4 corner names.
2. Loads `formula.json` next to the strategy YAML.
3. **If `corner_overrides[<CORNER>]` is present**, merges it with `formula.units` (per-corner wins per-unit; partial overrides are fine) and uses the result as-is — no mirroring.
4. **Otherwise**, mirrors `formula.units` via `formula.MirrorForCorner`, which reflects across the axes of the frame the formula declares in `screen`. A formula authored with `-live` (the normal case) declares the live device frame — 1280×720 on the pinned 720p device — so `screen` is *where the points were authored*, not a fixed 860×732 reference.
5. Projects the (merged or mirrored) units onto the live screen with `formula.ProjectUniform(device.display_scale)`: one uniform scale about the frame centre, which leaves a formula already authored at the live size untouched. `ApplyScreenScale` (a per-axis frame ratio) survives only for the corner-authoring path in `cmd/design_attack`, which scales between two buffers of the same device.

This means the same authored formula lands on all 4 sides without re-authoring for each, but you can also pin a specific side's coords when the base geometry demands it.

## The `cmd/design_attack` tool

The one-stop CLI for authoring + verifying formulas lives at `cmd/design_attack/main.go`.

### Authoring flags

| Flag | Purpose |
|------|---------|
| `-strategy <yaml>` | Strategy file that drives the unit walk (required) |
| `-out <formula.json>` | Output path (required; re-running with the same path merges instead of overwriting) |
| `-corner <CORNER>` | Which corner is being authored: `BR` / `BL` / `TR` / `TL` (full names also accepted). `BR` (default) saves to `formula.units`; the others save to `formula.corner_overrides[<CORNER>]` |
| `-live` | Capture the screen live from adb (no pre-saved PNG needed) |
| `-device <adb-serial>` | adb device serial for `-live` (empty = the only connected device; use `adb devices` to list) |
| `-screen <png>` | Pre-saved battle PNG. Mutually exclusive with `-live` |
| `-include-event` | Append `_event_troop` (point, 1 click) + `_event_spell` (line, 2 clicks) planned rows for seasonal event units |
| `-auto` | Skip clicks; derive coords from `formula.AutoPickFor` heuristics. Useful as a starting point before manual refinement |

### Verification flag

| Flag | Purpose |
|------|---------|
| `-verify <formula.json>` | Load the existing formula + the screen, then show a **2×2 grid** where each quadrant is the formula mirrored to one of the 4 corners (BR green / BL cyan / TR yellow / TL magenta). The grid PNG is also saved as `verify_grid.png`. Press any key in the window to exit. |

**Note**: the verify grid shows the **mirror** of `formula.units`, not the per-corner overrides. To see your explicit per-corner coords, run a live attack and watch the log for `rotated to next edge` + `formula.json loaded` with the merged unit count.

### Author-mode key bindings

| Key | Action |
|------|--------|
| Left-click | Drop point (1 click for points, 2 clicks for lines) |
| ENTER | Commit current unit + advance to next |
| `m` | (Line/ring units) Add sub-line OR bump count on the last sub-line (rage 3+2 pattern); on a ring unit, adds the next side |
| `u` / BACKSPACE | Undo last click |
| `c` | Clear current unit's clicks + sub-lines |
| `s` | Save and quit |
| `q` / ESC | Quit without saving |

## The 4-side ring (`"type": "sides"`) — Valkyries and other `FourSides` phases

Some armies place the SAME card on every side of the base in one battle instead
of picking a side per attack. `valk_spam.yaml` is the shipped example: the
Valkyrie phase is `pattern: "FourSides"`.

The corner mirror is the wrong tool for those phases — the point is to wrap the
base, not to reflect one flank onto the other three. So the picker authors them
differently:

- **Troops (`FourSides` phase)** → **two clicks per side**, `m` between sides,
  and the row is saved as `"type": "sides"`. The deploy path spreads the card's
  live count across every sub-line, so 15 Valkyries on a 4-side ring fire
  `4/4/4/3`. The per-line `count` in the JSON is ignored for a ring: the live
  card count decides.
- **Spells (`Point` phase)** → **one click**, saved as `"type": "point"`. The
  whole spell card drops on that spot (the casts are spread in a tight ring
  around it so they do not all land on one tile).

A `"sides"` entry is **not mirrored** — `formula.MirrorForCorner` skips it,
because the user already drew every side they want troops on. A ring authored
for one corner therefore deploys identically for all four, and `target_edge`
only moves the point entries (heroes, siege, spells).

### Authoring the valk ring

```bash
# 1. Get into a battle in CoC, with the whole base visible (do NOT zoom in).
go run ./cmd/design_attack -live -device localhost:5555 \
  -strategy assets/strategies/valk_spam.yaml \
  -out assets/strategies/valk_spam_formula.json
```

The walk is, in order:

| Step | Unit | How to click |
|------|------|--------------|
| 1 | `valkyrie` | P1 then P2 for the **left** side → `m` → **top** side → `m` → **right** side → `m` → **bottom** side → ENTER |
| 2 | `barbarian king` | 1 click |
| 3 | `archer queen` | 1 click |
| 4 | `grand warden` | 1 click |
| 5 | `dragon duke` | 1 click |
| 6 | `stone slammer` | 1 click |
| 7 | `earthquake spell` | 1 click — the single spot the whole spell card drops on |

Controls: left-click drops a point, `u`/BACKSPACE undoes the last click, `c`
clears the current unit, `s` saves and quits. A ring needs at least **2** sides,
but 4 is what wraps a base.

`cmd/design_attack -auto` writes a starting ring (one line down each screen edge,
inside the 20–80% band) plus a point per other unit, so you can eyeball the shape
before spending clicks:

```bash
go run ./cmd/design_attack -auto -live -device localhost:5555 \
  -strategy assets/strategies/valk_spam.yaml \
  -out assets/strategies/valk_spam_formula.json
```

Then verify the geometry against a saved battle frame — the tool draws the ring
and colours every point by class:

```bash
./build/bin/bot_cli -deploy-only \
  -strategy assets/strategies/valk_spam.yaml -device localhost:5555 -once
# afterwards, inspect last_attack_taps.png in the config dir:
#   white  = troops / siege    green = king + queen    magenta = spells
#   blue ring = the point fell outside the deployable band (a bug — re-author)
```

### The browser picker (`tools/line_picker.html`)

`cmd/design_attack` needs a desktop window and a walk through every unit. When all
you want is "here is the battle screen, draw the four Valkyrie lines and click
where the Earthquake goes", use the browser picker instead:

```bash
python3 tools/line_picker.py                       # capture live + build the page
python3 tools/line_picker.py --shot battle.png     # or draw on a saved frame
python3 tools/line_picker.py --device localhost:5555 --ring-unit valkyrie \
    --spell-unit "earthquake spell"
```

It captures one frame with `adb exec-out screencap -p`, inlines it into a
self-contained page under `tmp/line_picker_live.html` (open it, or register it
with the preview panel), and carries the other units over from the existing
formula so the result is a complete document. Every coordinate it reports is a
**device pixel** — the frame is inlined as a JPEG at the device's own
dimensions, so nothing is resampled or scaled.

- **`1` drag** to draw one line per side. Each drag is one side; the bot fires
  the card evenly along all of them.
- **`2` click** to put one spot under every ticked hero/siege unit, all together,
  instead of each on its own point. It snaps onto the nearest drawn side line.
- **`3` click** for the single spot the whole spell card drops on.
- **`4` drag** to re-pick the tap band (see below).
- `u` / `c` undo the last action / clear. Anything outside the band is drawn red
  and named in the sidebar with the reason it is a problem.
- **Download formula.json** saves the document; the page also exposes the same
  JSON as `window.__picker.json()`.
- Re-running the script seeds the page from the existing formula, so a fresh
  screenshot does not throw away the sides you already drew.

#### The tap band (`tap_band`)

The blue box is the row range a tap reaches: below the top resource HUD and
above the troop bar. The bot's built-in range is `yTopMin`..`UICutoff(h)`
(`internal/attack/deploy_line.go`) — one guess applied to every device and zoom,
and measurably too conservative in the middle of the frame, where the field is
clear well above `yTopMin`. So a formula may carry its own band, and the deploy
path **clamps to it instead of the built-in range**:

```json
{
  "screen": { "w": 1280, "h": 720 },
  "tap_band": { "y_top": 61, "y_bottom": 599 },
  "units": { ... }
}
```

Two rules matter when reading a log:

- The band is honoured **only when it describes the live frame.** The chrome it
tracks is edge-anchored while `ProjectUniform` moves points about the frame
centre, so a band written on another geometry has no correct projection; the
bot says so and uses the built-in range rather than clamping live points to rows
nobody measured.
- A band reaching **under the troop bar** is logged loudly and still used. It is
your measurement from the screenshot; silently reverting it is how a deploy ends
up somewhere neither of you chose.

Watch for `using the formula's own tap_band` (honoured) versus `using the
built-in band` (rejected, with the reason) in the deploy log.

### Reading the deploy log for a ring

A ring deploys per side, so the log carries one line per side plus a summary:

```
INF deploying troops along one side of the ring side=1 count=4 p1={256,144} p2={1024,144}
INF deploying troops along one side of the ring side=2 count=4 p1={1024,144} p2={1024,576}
INF deploying troops along one side of the ring side=3 count=3 p1={1024,576} p2={256,576}
INF deploying troops along one side of the ring side=4 count=3 p1={256,576} p2={256,144}
INF ring deploy: card spread across all authored sides sides=4 requested=14 fired=14
```

`fired` below `requested` means a side got a zero share (fewer units than sides);
`src=formula` on the `deploying troop` line means the ring was used rather than a
fallback edge line. If you instead see `FourSides rapid spam`, the formula was
not found or did not carry a `sides` entry — the bot fell back to the legacy
`pCfg.Edges` corners.

## Per-corner authoring walkthrough

The full authoring session is **4 runs of `cmd/design_attack`**, one per corner, with the camera panned to the matching side in CoC between each.

1. **Pan the camera in CoC** to show the BottomRight side of the base. The screen now shows the red deployment zone on the right.
2. **Run the BR authoring pass**:
   ```bash
   go run ./cmd/design_attack -live -device emulator-5554 \
     -strategy assets/strategies/auto_edrag_rush.yaml \
     -out assets/strategies/auto_edrag_rush_formula.json -corner BR
   ```
   Click 1 or 2 points per unit (11 rows: balloon, ED, slammer, BK, AQ, Warden, Prince, Duke, rage, ice, `_rage_inner`). For line units (balloon, ED, rage, ice, `_rage_inner`) click **P1** (line start) then **P2** (line end). For point units (heroes, siege) click **once**. Press ENTER to commit + advance, `s` to save when done.

   The `_rage_inner` row is **optional**: it only pre-pins the second, deeper rage line. Skip it (press `s`) and `spell_deployer.go` derives the inner line itself; the shipped `auto_edrag_rush_formula.json` carries no `_rage_inner` key.

3. **Pan the camera** to show the BottomLeft side. Re-run the command with `-corner BL`. The tool loads the existing `auto_edrag_rush_formula.json` (preserving the BR `units`) and adds a new entry under `corner_overrides.BottomLeft`.

4. Repeat for `TR` and `TL`. After all 4 runs, the file has the structure:
   ```json
   {
     "screen": { "w": 1280, "h": 720 },
     "units": { "balloon": { ... }, "slammer": { ... }, ... },
     "corner_overrides": {
       "BottomLeft": { "balloon": { ... }, "slammer": { ... }, ... },
       "TopRight":   { "balloon": { ... }, ... },
       "TopLeft":    { "balloon": { ... }, ... }
     }
   }
   ```

5. **Verify**:
   ```bash
   go run ./cmd/design_attack -live -device emulator-5554 \
     -verify assets/strategies/auto_edrag_rush_formula.json
   ```
   The 2×2 grid shows the mirror of `units` (BR default) in all 4 quadrants. The per-corner overrides are visible only at runtime — the verify command is for sanity-checking the mirror, not the overrides.

6. **Run a live attack**:
   ```bash
   ./build/bin/bot_cli -deploy-only \
     -strategy assets/strategies/auto_edrag_rush.yaml \
     -device emulator-5554 -once
   ```
   Watch the log for the per-corner override path:
   - `INF rotated to next edge` (or `random edge selected`) with the corner name
   - `INF formula.json loaded; per-unit explicit coordinates will override edge-based deploy` — with a unit count matching the merged formula for that corner
   - **No `INF MirrorForCorner`** — the per-corner override path skips the mirror
   - Subsequent `INF deploying troop/hero/spell` log lines should show coords matching what you pinned for the active corner

## Partial overrides (recommended)

A per-corner override is typically **partial** — only the units that need to differ from the mirrored BR default. The orchestrator merges `formula.units` and `formula.corner_overrides[<CORNER>]` per-unit, with the override winning:

```json
{
  "units": { "balloon": {...}, "slammer": {...}, "bk": {...}, ... },
  "corner_overrides": {
    "BottomLeft": { "balloon": {...} }   // only balloon differs for BL
  }
}
```

For targetEdge=`BottomLeft`, the orchestrator uses the override's `balloon` coords and the BR `units` `slammer` / `bk` / etc. coords. This means you only re-pin the units that actually need it, not all 11.

## The merge order matters

The orchestrator's per-corner logic (see `internal/attack/orchestrator.go::DeployDynamicV2`):

```go
if formulaPtr.CornerOverrides != nil {
    if cornerUnits, ok := formulaPtr.CornerOverrides[targetEdge]; ok {
        // merge units + overrides (override wins per-unit)
        formulaPtr.Units = merged
        usedOverride = true
    }
}
if !usedOverride {
    // fallback: mirror units across the axes of the frame `formula.screen`
    // declares (the live device frame for a -live authoring run)
    formulaPtr.MirrorForCorner(targetEdge)
}
// then project onto the live screen with the device's uniform display scale
formulaPtr.ProjectUniform(k, liveW, liveH, liveW, liveH)
```

**Override wins** when present. The mirror is the fallback for corners without an explicit override (so old single-side formulas continue to work).

## Why a single per-corner mirror isn't enough

A pure mirror around the BR default works for symmetric bases but is too coarse for real bases where the red deployment line is at different positions on each side. The mirror reflects the BR coords to BL/TR/TL, but if the BL side's red line is shifted in by 20px, the mirrored line lands too close (overlap) or too far (gap). The per-corner override lets you re-pin just the units that need it on each side.

For the user's first-time setup, expect to author all 4 corners. After that, you can typically get away with partial overrides for the 1–2 units that drift most between sides (often balloon — the line is most sensitive to red-line position).

## Auto-pick (optional starting point)

`cmd/design_attack -auto` derives every unit's coords from per-class heuristics (no manual clicks). Useful as a starting point before manual refinement:

```bash
go run ./cmd/design_attack -auto -live -device emulator-5554 \
  -strategy assets/strategies/auto_edrag_rush.yaml \
  -out assets/strategies/auto_edrag_rush_formula.json -target-edge right
```

The auto-pick falls back to `Left` when the strategy's `target_edge` is `Random` (for determinism). The output is reproducible across runs.

## Schema reference

`formula.Formula` (in `pkg/formula/formula.go`):

```go
type Formula struct {
    Name            string                     `json:"name"`
    Screen          struct { W, H int }        `json:"screen"`
    Units           map[string]UnitEntry            `json:"units"`
    CornerOverrides map[string]map[string]UnitEntry `json:"corner_overrides,omitempty"`
}

type UnitEntry struct {
    Type   string         `json:"type,omitempty"`  // "point" | "line" | "lines" | "sides"
    P      *Point         `json:"p,omitempty"`     // for point
    P1, P2 *Point         `json:"p1,p2,omitempty"`  // for line
    Lines  []LinePoint    `json:"lines,omitempty"`  // for lines (rage 3+2)
    Count  int            `json:"count,omitempty"`
    Jitter int            `json:"jitter,omitempty"`
}

type Point struct { X, Y int }
type LinePoint struct { P1, P2 Point; Count, Jitter int }
```

`TapBand` and `CornerOverrides` are **omitted from JSON when nil** (so old formulas round-trip cleanly). Each `corner_overrides` value is a per-unit map with the same schema as `Units`.

`type` is what the picker writes, so it is the field to trust: a single tap is `point` (or a legacy `p1`/`p2`-less entry), a straight sweep is `line`, a multi-cast sweep like the rage 3+2 split is `lines` — one `LinePoint` per sub-line, each with its own `count` — and a 4-side ring is `sides`, whose sub-lines are all deployed in the same battle (the mirror skips it, and the live card count is split evenly across the sides). `_rage_inner` is a separate, optional key, never a sub-line: when it is absent the deployer derives the inner line (`deriveInwardLine`). The shipped `assets/strategies/auto_edrag_rush_formula.json` therefore has ten `units` and no `_rage_inner`.

## Reference: `formula.MirrorForCorner` reflection rules

| Corner | MirrorX | MirrorY | Effect |
|--------|---------|---------|--------|
| `BottomRight` / `BR` | no | no | identity (formula as-authored) |
| `BottomLeft` / `BL` | yes | no | reflect X (newX = W − X) |
| `TopRight` / `TR` | no | yes | reflect Y (newY = H − Y) |
| `TopLeft` / `TL` | yes | yes | reflect both |

Freeform values like `"left"` or `"right"` also work via a substring fallback (mirrors X for `"left"`, mirrors Y for `"top"`).
