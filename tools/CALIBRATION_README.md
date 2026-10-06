# ClashGO Calibration Toolkit

Visual calibration utilities for picking tap targets, ROIs, and offset bands
from a live BlueStacks instance. All tools use OpenCV for the picker UI and
pull frames via `adb exec-out screencap -p`.

## The unified picker

`tools/picker.py` consolidates every earlier picker (`pinpoint.py`,
`select_builder_roi.py`, `select_wall_upgrade_buttons.py`,
`calibrate_battle_loot.py`) into a single CLI that drives the picker UI
based on task flags. Each invocation runs one picker session and saves to
a JSON asset file.

### Built-in presets

The presets are the common case — no flags to remember. Each saves to the
JSON file the Go code at `internal/bot/*.go` reads.

| Want to pick…                              | Command                                                      | Saves to                                |
| ------------------------------------------ | ------------------------------------------------------------ | --------------------------------------- |
| Builder-menu drag region                   | `picker.py --preset menu`                                    | `assets/builder_menu_roi.json`          |
| Wall-upgrade gold + elixir buttons         | `picker.py --preset buttons`                                 | `assets/wall_upgrade_buttons.json`      |
| Modal X-button (close unaffordable popup)  | `picker.py --preset modal-x`                                 | `assets/wall_upgrade_modal.json`        |
| Post-upgrade Confirm button (blind tap)    | `picker.py --preset confirm`                                 | `assets/wall_upgrade_confirm.json`      |
| Battle-loot search columns (battle + bonus)| `picker.py --preset battle-loot`                             | `assets/battle_loot_rois.json`          |
| Wall cost-text offset band (relative, **legacy** — see "Simplified wall-upgrade flow" below) | `picker.py --preset cost-roi`                                | `assets/wall_upgrade_cost_roi.json`     |

Add `--tap` to any preset that uses `--point` to replay the picked coord
on-device as `adb shell input tap x y` after the picker commits — that's
the live-verify behavior the old `tools/pinpoint.py` had:

```bash
picker.py --preset modal-x --tap   # pick the X-button then immediately tap it
```

### Power-user modes (raw flags)

Use these when the preset set doesn't cover what you need. The picker
runs each task in CLI-flag order.

```bash
# Single tap target with custom key
picker.py -o assets/foo.json --point x_button

# N rectangles with custom keys
picker.py -o assets/foo.json --rect gold --rect elixir --rect king_button

# Each task is shown on-screen: TASK 1/3: RECT for 'gold', TASK 2/3: ..., etc.
```

### Task types

| Flag      | Mouse interaction                      | Output schema (per key)                       |
| --------- | -------------------------------------- | --------------------------------------------- |
| `--rect`  | drag → rectangle normalized to (x1,y1,x2,y2) | `{"x1": ..., "y1": ..., "x2": ..., "y2": ...}` |
| `--point` | click → (x, y)                          | `{"x": ..., "y": ...}`                        |
| `--offset`| click center → drag band → 4 relative offsets | `{"x_min_off": ..., "y_min_off": ..., "x_max_off": ..., "y_max_off": ...}` |
| `--sample KEY` | click → pixel color (RGB + OpenCV-scale HSV + BT.601 gray) | `{"x":..., "y":..., "rgb":[r,g,b], "hsv":[h,s,v], "gray":N}` |
| `--scan KEY R,G,B,TOL` | none (auto) → connected-components on in-tolerance mask | array of `{"x1":..., "y1":..., "x2":..., "y2":..., "area":N}`, sorted largest-first |

### Discovery workflows (find new things)

`--sample` + `--scan` together solve the discovery step of “where on the
screen does this UI element live?” without manual drag-and-pray:

```bash
# Step 1: figure out what color the cost-text is right now
picker.py -o assets/cost_text_samples.json --sample cost_top --sample cost_mid

# Step 2: hand the color you learned to a scan task, get bounding boxes
picker.py -o assets/cost_text_rois.json --scan red_text 230,40,40,30 --scan white_text 240,240,240,15

# Step 3: combine with a tap target you're calibrating in the same session
picker.py -o assets/wall_upgrade_confirm.json \
  --rect confirm_button \
  --scan error_modal 200,30,30,25
```

The JSON shape for `--scan` is uniform (array of rects + area), so it
slurps cleanly into Go: any blob-getting code in `internal/...` can
read `KEY[0]` (largest) or walk the array without special-casing.

`--min-area N` (default 100) floors the blob size in pixels — raise
it to drop small/texture blobs, lower it to capture fine UI detail.
Only affects `--scan`.

Tolerance semantics for `--scan`: Euclidean distance from `(R,G,B)`
in RGB space (`√(ΔR² + ΔG² + ΔB²) ≤ tol`), *not* per-channel max
delta. A `tol=20` allows up to ±20 in any single channel and tighter
in the worst-case diagonal where all three channels differ; it does
NOT have a per-channel equivalent of 34 (that's the opposite
direction — a per-channel tol of 34 would have an inclusive
Euclidean radius of 34, not 20).

### Simplified wall-upgrade flow (color-free)

Replaces the old "read the cost text pixel color before tapping" check
with a **blind-tap + observation** loop:

1. tap gold/elixir (`assets/wall_upgrade_buttons.json`)
2. wait ~1.0s for the post-tap dialog to settle
3. tap **Confirm** blindly (`assets/wall_upgrade_confirm.json`)
4. wait ~1.0s for either the gem-buy modal OR the build queue to appear
5. **detect the gem-buy modal** → if modal-up, tap close-X
   (`assets/wall_upgrade_modal.json`); otherwise the upgrade succeeded,
   advance to the next resource.

This removes the `cost-roi` asset from the hot path — the bot no longer
needs the cost-text ROI at all. The picker setup for the new flow is:

```bash
picker.py --preset buttons   # already calibrated
picker.py --preset modal-x   # already calibrated
picker.py --preset confirm   # NEW: pick the post-upgrade Confirm button
picker.py -o assets/gem_modal_features.json \
  --sample modal_red  --sample modal_bg  \
  --scan red_text 230,40,40,30   # tune R,G,B,TOL post-pick
```

The `--sample` step is for finding the dominant color in the gem-buy
modal (so you know what RGB to pass to `--scan`); `--scan` then locks
the ROI by walking the largest matching blob. Saved scan results are
uniform JSON arrays so the Go side can read `KEY[0]` (largest blob)
without special-casing.

If you'd rather not use `--scan` and just want a hardcoded "the modal
is up if pixels at coord (X,Y) match (R,G,B,T)" check, drop the
modal-x hardcoded coord into your Go-side check directly — that's
the same primitive `--scan` uses under the hood.

### Controls

- Left-click + drag — draw a rectangle (rect/offset)
- Left-click — place a point (point/offset center)
- Enter / Space — confirm the current task and advance
- `r` — reset the current task's selection
- `q` / Esc — cancel the entire run (no save)

### Reference-resolution consideration

The picker captures at the device's actual screen size and saves in
**physical pixel coordinates**. The Go bot scales these to its 860x732
reference resolution at runtime (see `game/calibration.go`). If you need
the picker to write the normalized reference frame **in addition** to
the physical frame, use `--preset menu` (which captures both by design)
or feel free to add a `--reference-also` flag for your custom schema.

## Multi-screen wizards

Some calibration flows span multiple dialog screens (you can't see
both at once). Rather than teach the picker to capture per-task,
the easier answer is a thin bash wrapper that chains two picker
runs with a screen-navigation prompt between them:

| Want to calibrate…                        | Run                                       | Saves                                          |
| ----------------------------------------- | ----------------------------------------- | ---------------------------------------------- |
| Wall-upgrade exit flow (confirm + X-popup) | `tools/pick_upgrade_exit.sh`             | `assets/wall_upgrade_confirm.json` + `assets/wall_upgrade_x_roi.json` |

`tools/pick_upgrade_exit.sh` walks you through two picker runs:
first the post-upgrade Confirm dialog (point by default, or rect
via `--rect-confirm`), then the unaffordable gem-buy popup (rect,
the X-popup ROI). See the script's `--help` for the full flag set.

If you find yourself writing a third multi-screen wizard of your
own, the pattern is `cd $(repo-root); picker.py --preset X; ask-user-to-navigate; picker.py --rect Y`.
Keep it under `tools/` so its script-style invocation stays
consistent with the rest of the toolkit.

## Legacy tools (kept for migration convenience)

| Old filename                             | Replaced by                       | Status  |
| ---------------------------------------- | --------------------------------- | ------- |
| `tools/pinpoint.py`                      | `picker.py --point <KEY> --tap` (e.g. `--preset modal-x --tap`) | Deprecated — kept for offline use |
| `tools/internal/select_builder_roi.py`   | `picker.py --preset menu`         | Deprecated |
| `tools/select_wall_upgrade_buttons.py`   | `tools/wall_pick.sh` (browser session, no OpenCV) | Active — kept as the no-browser alternative |
| `tools/internal/calibrate_battle_loot.py`| `picker.py --preset battle-loot`  | Deprecated |

The legacy scripts still work but won't gain new features. New asset JSON
schemas should be picked via `picker.py` from the start so the team
has a single mental model.

## The wall-upgrade flow: `tools/wall_pick.sh`

The wall assets are the one family whose **coordinates are consumed through the
HUD law** (`internal/game.Calibration`) rather than read verbatim, so a picker
must record a `reference` block that maps back onto the box the user drew.
Dividing the drag by a scale factor is *not* that inverse — it is the deprecated
per-axis law — which is why `picker.py --preset buttons` (flat x1/y1/x2/y2 of the
raw drag) produced an asset whose boxes moved on every run, and why the loop's
blind taps landed on builder-menu rows on the live 1280x720 device.

There are two inverses because the flow consumes two laws. Every control is read
as an `assetRect` through `HudRect`, which picks one anchor per axis from the
rect's centre (`HudRectReferenceSnap`). `assets/builder_menu_roi.json` is instead
mapped **corner by corner**, because that panel's top edge is pinned under the top
bar while its bottom edge is mid-screen (`HudCornersReferenceSnap`). The two laws
agree on the device a box was dragged on and diverge on any other, so inverting
the wrong one writes a file that looks right and drifts on the next geometry.

```bash
./tools/wall_pick.sh            # guided capture of all seven states, then opens the page
./tools/wall_pick.sh serve      # local picking server: page + Save button + captures
./tools/wall_pick.sh 3          # re-capture one step only
./tools/wall_pick.sh capture 3  # capture one step and nothing else
./tools/wall_pick.sh page       # rebuild + open the page from what is captured
./tools/wall_pick.sh apply      # paste the page's boxes on stdin; writes the assets
```

Capture comes first because a box only means something against the frame it was
drawn on, and every frame has to come from one run at one screen size. The session
prints what must be on screen for each step (village, builder menu, wall tray,
confirm dialog, gem popup), waits for Enter, and captures. Picking then happens in
a browser — no OpenCV, no Python — where each frame carries the same note, and the
reported coordinates are native device pixels whatever zoom the panel applies.
Paste the page's output into `apply`.

### The picking server

`./tools/wall_pick.sh serve` runs `cmd/wallserve`, which serves that same page
over loopback with a **Save** button. It exists because the picks are the input to
a conversion the browser cannot do: a drag is device pixels and an asset file is
860x732 reference values, so something on this side has to receive them, and a
file:// page can send nothing anywhere. Serving also lets the page drive the
device — each card has a capture button, so the whole session can be one window
with no terminal prompts.

```
GET  /            the picking page
GET  /frame/{n}   one captured frame, for refreshing a card in place
POST /capture     {"step":N}   run the capture session for that step
POST /save        {"picks":...}  write <dir>/picks.txt
POST /apply       {"picks":...}  write picks.txt, convert, and write the assets
```

`save picks` writes `output/wall_pick/picks.txt` — one `key x1 y1 x2 y2` line per
box behind a `#` header, which `cmd/refmap` skips, so the file can be piped
straight back into the converter. `save & write assets` does that and then the
conversion, showing the converter's own report in the page: the per-box offset
between the drag and what the reference frame can express. A box the reference
frame cannot reach is refused rather than written, and a wide-offset box is
warned about before the file is overwritten.

It shells out to this script for both capture and conversion rather than
reimplementing either, so it needs no OpenCV and there is still one tested path
for each. It binds loopback only, and `-dir` keeps a probe server's captures and
picks out of the real session's directory.

`apply` takes the frame size from the captured PNG header and the display scale
from the device config (`device.display_scale`, the same number the bot pins), so
it needs nothing on screen and no `-k` to remember. It routes every key to its
asset file, which is why a multi-file set requires `-write`: stdout would have to
interleave several JSON documents. `assets/builder_menu_roi.json` is written as a
top-level block pair, not wrapped under its own key — the wrapped form unmarshals
into nothing and the loader now reports that instead of silently keeping its
default ROI.

`cmd/refmap` is the converter underneath, and is usable on its own:

```bash
make refmap ARGS="-describe"                          # list the schemas
printf 'gold 591 503 692 601\n' | \
  make refmap ARGS="-w 1280 -h 720 -schema buttons"    # print one asset file
```

It writes `{"physical": …, "reference": …}` per key, tells you how far the
recorded box sits from the drag, and refuses a box whose centre falls in a band
seam the reference frame cannot express (nudge it ~80 px and retry). The loaders
prefer `reference`, still read the flat shape for files picked at 860x732, and
reject a physical-only box instead of mapping live pixels as reference ones.

`tools/select_wall_upgrade_buttons.py` is the same seven steps as a keyboard
drag session over a fresh screencap per step, for a machine that has cv2 and
prefers not to use a browser:

```bash
make refmap
python3 tools/select_wall_upgrade_buttons.py            # GUI drag session
python3 tools/select_wall_upgrade_buttons.py --check    # plan + dependency check
```

## Output schema reference

Each preset produces JSON that the Go code expects verbatim. To verify
after calibration, run:

```bash
python3 -c "import json; print(list(json.load(open('assets/foo.json')).keys()))"
```

To inspect everything (including the values):

```bash
less assets/foo.json
```

The Go loader in `internal/bot/wall_upgrade.go` (e.g.
`loadWallUpgradeButtons`, `loadWallUpgradeXButton`) hardcodes the keys
it reads and the integer types it expects — if your JSON file uses a
different key name, the loader returns `ok=false` and the bot falls
back to a different path. After picking, search the bot's Go code for
the asset filename to confirm the schema is exactly what's expected.
