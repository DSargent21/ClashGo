# Device geometry & resolution changes

Everything the bot "sees" is authored in a **reference frame of 860×732**
(`internal/game/calibration.go`). This document records what was measured
about how Clash of Clans actually lays out that frame at other display
geometries, so a future resolution change is a calibration job with known
steps instead of an experiment.

## Current geometry

| Knob | Value | Where |
| --- | --- | --- |
| Android display (`wm size`) | `860×732` | BlueStacks `Guests/Android/FrameBuffer/0/Guest{Width,Height}` |
| Android density (`wm density`) | `320` | BlueStacks `FrameBuffer/0/Dpi` |
| Bot reference frame | `860×732` | `game.RefWidth` / `game.RefHeight` |
| Device profile | `SM-G998B` (Galaxy S21 Ultra), product `p3sxxx` | BlueStacks multi-instance manager |

Note the density mismatch: `config.DefaultConfig()` still says `DPI: 160`
while the emulator actually reports `320`. Density does **not** affect the
game's layout (see measurements below), but the config value is written into
the BlueStacks framebuffer prefs on a cold boot, so it should eventually be
made truthful.

860×732 is not a real device resolution and its aspect ratio (1.175) does not
match any shipping phone or tablet, which is the reason a "more realistic"
resolution gets proposed. The obstacle is not the config value — it is the
layout model below.

## How CoC lays out a non-reference geometry

Measured with `cmd/resprobe` (see *Measuring* below) on a live BlueStacks Air
instance, comparing a reference capture at 860×732 against the same village
at other geometries:

| Geometry | World/village scale `k` | Anisotropy | HUD anchor scale |
| --- | --- | --- | --- |
| 860×732 @ 320 | 1.000 (reference) | 1.000 | 1.000 |
| 1280×720 @ 320 | **1.325** | **1.000** | ≈ 1.32 (x margins), ≈ 1.2–1.3 (y margins) |
| 1290×1098 @ 320 (same aspect as ref) | **1.500** | 1.000 | 1.500 |
| 645×549 @ 320 (same aspect as ref) | ≈ 0.70–0.75 | 1.00 | ≈ 0.70 |

Two findings drive the migration plan:

1. **The game world is uniformly scaled about the viewport centre.** Three
   separate village patches (centre and off-centre) all matched at a uniform
   scale with `anisotropy = 1.000`, and their matched centres sat exactly at
   `phys_center + (ref_point − ref_center) × k`. A patch near the bottom edge
   (ref y 600–720) only partially matched, consistent with that band falling
   off the shorter viewport: at 1280×720 the visible village band is
   `ref y ∈ [94, 638]` — the reference frame's top and bottom ~94 px are not
   visible.
2. **The HUD is edge-anchored and scales by the same `k`.** Element *sizes*
   and their margins to the nearest screen edge both scale with `k`
   (`Attack!` text 74×20 @ (25,695) → 97×23 @ (35,669); `SHOP` right margin
   63 → 84). The HUD therefore does **not** follow the world transform: a
   bottom-anchored HUD point at ref y 695 maps to y≈672 under the edge model
   but to y≈788 (off-screen) under the centred world model.

`k` tracks the **screen diagonal ratio**: `k ≈ √(W²+H²) / √(860²+732²)`.
That is consistent with the game keeping a constant *physical* UI size: the
reported screen size in inches never changes, so the pixel-per-inch ratio is
the diagonal ratio. It is exact for aspect-preserving geometries and within a
few percent elsewhere — measured vs predicted:

| Geometry | `k` measured | diagonal-ratio prediction | error |
| --- | --- | --- | --- |
| 860×732 (reference) | 1.000 | 1.000 | by construction |
| 1280×720 | 1.325 (world) / 1.36 (HUD icon, conf 0.97) | 1.3006 | +1.9% / +4.6% |
| 1600×900 | 1.68 | 1.626 | +3.3% |
| 1920×1080 | 2.00 | 1.951 | +2.5% |
| 1290×1098 | 1.500 | 1.500 | 0% |
| 645×549 | ≈0.70–0.75 | 0.750 | 0 to −6.7% |

The residual is the whole reason the migration is not a one-line change: a 3%
scale error moves a reference point at x≈800 by ~24 px, and the classifier's
probes are single pixels with a ±25–50 channel distance. The reference
geometry is exact, so its calibrated thresholds are unaffected — but a
*predicted* `k` at a new geometry cannot be trusted for 1-px probes. Two
candidates, both compatible with the measurements: the game renders from a
fixed 960×540 design canvas (`k = diag(screen)/diag(960,540)`: predicts 1.3325
at 1280×720 and exactly 2.0 at 1920×1080), and the HUD may scale slightly more
than the world (1.36 vs 1.325). Measure, don't predict:

* preferred — measure `k` live at boot by template-matching a known HUD
template (e.g. `btn_attack` in the village) over a fine scale sweep around the
prediction, and store the result in `Calibration.K`;
* or probe a small neighbourhood (±2 px) at non-reference geometries instead
of a single pixel, which absorbs the residual — but that weakens rule
discrimination and must be re-validated against the false-positive cases the
anchors were tightened for (`ArmyCamp`, `StateBattleEnd`).

Also measured: **`k` does not depend on density.** 1280×720 with density
240, 320 and 480 (with a full CoC restart in between, so the value is not
just cached at launch) produced identical layouts (`k = 1.325`). Density
overrides are therefore not a lever for keeping the reference layout.

## Why this cannot be a config flip

The bot's geometry layer is a single affine map — `Calibration.ScaleRef`
scales x by `physW/860` and y by `physH/732`. That map is only correct when
`k == physW/860 == physH/732`, i.e. when the new geometry is the reference
aspect scaled uniformly. The measurements above show the real law is
different in three ways:

* the scale is the *diagonal* ratio, not the per-axis ratio;
* positions are edge-anchored for the HUD but centre-anchored for the world;
* the visible world band changes shape with the viewport aspect.

Consequences of flipping 860×732 → 1280×720 with only a config change
(measured, not predicted):

* every classifier pixel anchor is sampled at the wrong place — on a live
  village frame `MainVillage` drops from 2/7 to 1/7 passing anchors and its
  score from 260 to 160 (still wins, on a thinner margin);
* the template set was captured at HUD scale 1.0 while the HUD renders at
  `k = 1.325` (×1.017 from the `ResizeToHeight(732)` normalisation), far
  outside the `0.9–1.1` multi-scale window, so templates stop matching;
* loot/result-panel ROIs, slot-bar geometry and the deploy formulas are all
  authored in the reference frame and would need the new mapping for their
  HUD or world half.

## Migration plan (to a real 720p phone geometry)

Target: **1280×720 @ 320 dpi** — 16:9, 720p, the classic "xhdpi 720p" phone
class (e.g. GT-I9300 / Moto G: 720×1280 @ 320). Pair it with a matching
BlueStacks device profile; spoofing a 1440×3200 S21 Ultra while the display
reports 720p is exactly the kind of inconsistency this change is meant to
remove.

1. **Teach `Calibration` the real law.** Replace the single affine map with
   two explicit ones that share `k = diag(physical)/diag(reference)`:
   * `HUD(p)` — edge-anchored: `x·k` from the left, `W − (860−x)·k` from the
     right, mirrored for the top/bottom edges;
   * `World(p)` — centre-anchored: `phys_center + (p − ref_center)·k`.
   Call sites must be classified as HUD or world: classifier anchors, button
   ROIs, slot bar, troop counts, loot panel = HUD; deploy edges/rings, red
   line, base searches = world.
2. **Make the classifier normalisation scale-aware.** `ResizeToHeight(screen,
   732)` is a no-op at the reference aspect only. Normalise by `1/k` (or
   resize to `W/k × H/k`) so HUD elements land at reference pixel size and the
   existing template PNGs keep matching at 1.0.
3. **Re-measure, don't guess, the anchors that are not pure translations.**
   Every pixel anchor declares an expected RGB; transform them, then verify
   per anchor on live frames (`cmd/screendump` prints ref→live RGB per key
   button, and the per-rule pass counts show which anchors broke).
4. **Re-check the world band.** Any deploy geometry below `ref y 638` or
   above `ref y 94` is off-screen at 720p; clamp or re-author those points
   (the strategies are the place this shows up).
5. **Verify live, state by state.** Village → search → battle (deploy
   geometry, slot bar, troop counts) → result panel (loot OCR + stars) →
   return home. A real attack run is the only check that covers the deploy
   mapping, and the failure mode is silent (taps land next to the intended
   point), so it must be run before the geometry is trusted.

Aspect-preserving geometries are the cheap case: at 1290×1098 and 645×549
`k` is a plain uniform scale (1.5 and ~0.7) and the existing pipeline keeps
working unchanged — verified live for classification. A "cosmetic" resolution
change that keeps the aspect can therefore ship without recalibration, but it
does not move the display onto a real device profile either.

## Migration progress

Landed (reference geometry verified unchanged, live and in the test suite):

* `Calibration.K` (display scale) plus `MapX`/`MapY`/`MapPoint`/`MapRect`
  implementing both anchors, and `NewCalibration` as the single constructor
  (zero-value and 860×732 calibrations are the identity, so every existing
  caller and test is unaffected);
* `StateRule.Anchor` with per-rule semantics — `AnchorEdge` for HUD chrome
  (`StateBattle`, `StateMainVillage`, `StateBuilderBase`, `StateFindMatch`,
  `StateArmySelection`), `AnchorCenter` for centred overlays and world/full-
  screen art (result panel, every dialog, splash chain, search map, army
  overview, chat, shield, settings, loading);
* `ClassifyState` maps each probe through the live calibration and matches
  templates on the **raw** frame with the scaled-template cache, sweeping
  `k×0.9 .. k×1.1` — identical to the old `0.9–1.1` sweep when `k == 1`, and
  it removes the per-frame `ResizeToHeight(732)`;
* `cmd/screendump -anchors` prints every rule anchor under both models with
  the sampled colour and a hit/miss verdict, which is the verification tool
  for the rest of the migration.

Verified live at 1280×720: the mapped anchor positions land within ~3 px of
the elements' measured positions (top-right resource icons matched at
`(1240,46)` and `(1240,127)` against mapped `(1241,46)` and `(1241,124)`, at
confidences 0.92 and 0.97). What that same run also showed is the failure mode
to fix next: with the predicted `k`, the elixir-icon probe sampled 3 px
off and fell outside its ±40 tolerance while every position was structurally
right.

Still to do: measure `k` live at boot; re-validate each `AnchorCenter` verdict
against a live frame of its own state (village, boot splash, search, battle,
result, army selection, chest are all reachable on demand); migrate the
remaining consumers (`internal/bot` button ROIs, `internal/game/loot.go`
result panel + rois, `internal/game/chestdismiss.go`, `internal/game/navigator.go`,
`internal/attack` slot bar / troop counter / hero manager / spell deployer /
red line / deploy geometry); re-check the world band clipping for deploy
points outside `ref y ∈ [94, 638]`; and finish with a real attack run, since
that is the only check that covers the deploy mapping.

## Measuring

`cmd/resprobe` is the instrument for all of the numbers above: it takes a
patch from a reference capture and searches a candidate capture over a grid of
(scaleX, scaleY) pairs, reporting the best confidence, the matched centre and
the anisotropy (`scaleX/scaleY`).

```bash
# capture both geometries, then:
go run ./cmd/resprobe -a ref_860x732.png -b cand_1280x720.png \
    -x 330 -y 300 -w 200 -h 150 -margin 320
```

* `anisotropy ≈ 1.000` with the centre at `phys_center` → uniform, centred
  world scale (what every real geometry so far shows).
* `anisotropy ≫ 1` → resampling noise or a non-uniform stretch; pick a patch
  with more texture before believing it.
* HUD patches (`Attack!` at ref 20,688 100×30; `SHOP` at ref 770,690 55×26)
  measure the edge-anchored scale.

Live experiments must reset the override afterwards — `wm size reset` and
`wm density reset` — and CoC recreates its surface (a few seconds of black
frames) whenever the display geometry changes.
