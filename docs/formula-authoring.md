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
4. **Otherwise**, mirrors `formula.units` around the formula's authored 860×732 reference frame via `formula.MirrorForCorner`.
5. Projects the (merged or mirrored) units to the live screen via `ApplyScreenScale`.

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
| `m` | (Line units only) Add sub-line OR bump count on the last sub-line (rage 3+2 pattern) |
| `u` / BACKSPACE | Undo last click |
| `c` | Clear current unit's clicks + sub-lines |
| `s` | Save and quit |
| `q` / ESC | Quit without saving |

## Per-corner authoring walkthrough

The full authoring session is **4 runs of `cmd/design_attack`**, one per corner, with the camera panned to the matching side in CoC between each.

1. **Pan the camera in CoC** to show the BottomRight side of the base. The screen now shows the red deployment zone on the right.
2. **Run the BR authoring pass**:
   ```bash
   go run ./cmd/design_attack -live -device emulator-5554 \
     -strategy assets/strategies/auto_edrag_rush.yaml \
     -out assets/strategies/auto_edrag_rush_formula.json -corner BR
   ```
   Click 1 or 2 points per unit (11 total: balloon, ED, slammer, BK, AQ, Warden, Prince, Duke, rage, ice, `_rage_inner`). For line units (balloon, ED, rage, ice, _rage_inner) click **P1** (line start) then **P2** (line end). For point units (heroes, siege) click **once**. Press ENTER to commit + advance, `s` to save when done.

3. **Pan the camera** to show the BottomLeft side. Re-run the command with `-corner BL`. The tool loads the existing `auto_edrag_rush_formula.json` (preserving the BR `units`) and adds a new entry under `corner_overrides.BottomLeft`.

4. Repeat for `TR` and `TL`. After all 4 runs, the file has the structure:
   ```json
   {
     "screen": { "w": 860, "h": 732 },
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
    // fallback: mirror units around the formula's 860x732 reference frame
    formulaPtr.MirrorForCorner(targetEdge)
}
// then scale to live screen
formulaPtr.ApplyScreenScale(...)
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
    Type   string         `json:"type,omitempty"`  // "point" | "line" | "lines"
    P      *Point         `json:"p,omitempty"`     // for point
    P1, P2 *Point         `json:"p1,p2,omitempty"`  // for line
    Lines  []LinePoint    `json:"lines,omitempty"`  // for lines (rage 3+2)
    Count  int            `json:"count,omitempty"`
    Jitter int            `json:"jitter,omitempty"`
}

type Point struct { X, Y int }
type LinePoint struct { P1, P2 Point; Count, Jitter int }
```

`CornerOverrides` is **omitted from JSON when nil** (so old formulas without the field round-trip cleanly). Each per-corner value is a per-unit map with the same schema as `Units`.

## Reference: `formula.MirrorForCorner` reflection rules

| Corner | MirrorX | MirrorY | Effect |
|--------|---------|---------|--------|
| `BottomRight` / `BR` | no | no | identity (formula as-authored) |
| `BottomLeft` / `BL` | yes | no | reflect X (newX = W − X) |
| `TopRight` / `TR` | no | yes | reflect Y (newY = H − Y) |
| `TopLeft` / `TL` | yes | yes | reflect both |

Freeform values like `"left"` or `"right"` also work via a substring fallback (mirrors X for `"left"`, mirrors Y for `"top"`).
