#!/usr/bin/env python3
"""
picker.py — unified visual calibration picker for ClashGO.

Replaces the 4 ad-hoc pickers (pinpoint.py, select_builder_roi.py,
select_wall_upgrade_buttons.py, calibrate_battle_loot.py) with a single
data-driven CLI. Each visual selection (point or rectangle) is captured
to a key in the output JSON, with reference-coords auto-computed from
the device's actual screen size vs the bot's reference resolution
(860x732).

MODES
-----
  --point KEY     click a tap target. Saves {x, y} under KEY.
  --rect KEY      drag a rectangle. Saves {x1, y1, x2, y2} under KEY.
  --offset KEY    click center + drag band → 4 relative offsets.
                  The 4 ints (x_min_off, y_min_off, x_max_off, y_max_off)
                  are stored at the JSON top level (offset mode owns the
                  cost-roi shape; the KEY arg here is just a label).
  --sample KEY    click a point AND record its RGB + HSV + grayscale.
                  Saves {x, y, rgb:[r,g,b], hsv:[h,s,v], gray} under KEY.
                  Use this to discover "what color is the cost text?"
                  before writing a matching --scan for it.
  --scan KEY R,G,B,TOL
                  auto-detect connected components of pixels within
                  tolerance of R,G,B in the captured frame. Saves an
                  array of {x1,y1,x2,y2,area} blobs sorted largest-first
                  under KEY. Use this to find ROIs without manual drag —
                  e.g. "where do red-text regions live on this screen?"
                  Override the default size floor via --min-area N.

PRESETS (built-in shortcuts for existing assets)
------------------------------------------------
  --preset menu         drag the builder menu ROI → assets/builder_menu_roi.json
                        (writes both physical and reference frames)
  --preset buttons      drag gold + elixir upgrade buttons → assets/wall_upgrade_buttons.json
  --preset modal-x      click the X-button on the unaffordable modal → assets/wall_upgrade_modal.json
  --preset confirm      click the post-upgrade Confirm button → assets/wall_upgrade_confirm.json
                        (blind-tap target; the simplified wall-upgrade flow
                         taps this with no pre-check, then observes whether
                         the gem-buy modal appears — see CALIBRATION_README.md's
                         "Simplified wall-upgrade flow" section)
  --preset battle-loot  drag 2 search columns → assets/battle_loot_rois.json
  --preset cost-roi     click match-center + drag cost band → assets/wall_upgrade_cost_roi.json
                        (writes 4 offset ints). Legacy — used by the old
                        pre-tap red-pixel check on the cost text. The new
                        blind-tap flow no longer needs it.

CONTROLS
--------
  - Left-click + drag : draw a rectangle (rect/offset modes)
  - Left-click         : place a point (point mode)
  - Enter / Space      : confirm the current task and advance
  - 'r'                : reset the current task's selection
  - 'q' / Esc          : cancel the entire run (no save)

Each task's progress is shown on-screen ("TASK 2/3: rect for 'elixir'")
and in the terminal, so the user always knows what's next.

MERGE BEHAVIOR (re-picks)
-------------------------
If the output JSON already exists when the picker runs, the new
results are MERGED into it (preserves unrelated top-level keys,
overwrites collisions). So this workflow "just works":

  # 1. Pick the primary x_popup_roi (writes {"x_popup_roi": {...}})
  picker.py -o assets/wall_upgrade_x_roi.json --rect x_popup_roi

  # 2. Pick a second x_popup_roi_alt (merges — preserves the primary)
  picker.py -o assets/wall_upgrade_x_roi.json --rect x_popup_roi_alt

The save path prints a "Merge audit" line showing exactly which keys
were preserved/replaced/added so re-pick operations can't silently
regress a multi-rect asset to single-rect. Graceful fallback on
malformed JSON (warns + writes fresh instead of crashing the user
out of a half-written prior file).
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from typing import Any

import cv2
import numpy as np

REF_W, REF_H = 860, 732  # bot's calibration reference

# --- Presets --------------------------------------------------------------

PRESETS: dict[str, dict[str, Any]] = {
    "menu": {
        # Single-rect drag → main() derives `reference` from `physical`.
        # The bot's ROIConfig loader at internal/bot/wall_upgrade.go
        # only consumes the `physical` block; the `reference` block is
        # still written so downstream tools + human eyeballs can verify
        # the pixel-frame normalization is sane.
        "output": "assets/builder_menu_roi.json",
        "tasks": [
            {"type": "rect", "key": "physical"},
        ],
        "auto_derive_reference": True,
    },
    "buttons": {
        # Drag 2 rects labelled gold + elixir, save flat per-key.
        "output": "assets/wall_upgrade_buttons.json",
        "tasks": [
            {"type": "rect", "key": "gold"},
            {"type": "rect", "key": "elixir"},
        ],
    },
    "modal-x": {
        "output": "assets/wall_upgrade_modal.json",
        "tasks": [
            {"type": "point", "key": "x_button"},
        ],
    },
    "confirm": {
        # Single-point coordinate for the "Confirm" / "Yes" button in
        # the post-upgrade dialog. The simplified wall-upgrade flow
        # (see CALIBRATION_README.md "Simplified wall-upgrade flow")
        # taps this with no pre-check, then observes the outcome:
        #   - gem-buy modal present → tap close-X (modal-x asset)
        #   - gem-buy modal absent   → assume upgrade succeeded
        # Same single-point pattern as modal-x so a future Go loader
        # can reuse the (x, y) schema without further changes.
        "output": "assets/wall_upgrade_confirm.json",
        "tasks": [
            {"type": "point", "key": "confirm_button"},
        ],
    },
    "battle-loot": {
        "output": "assets/battle_loot_rois.json",
        "tasks": [
            {"type": "rect", "key": "battleSearch"},
            {"type": "rect", "key": "bonusSearch"},
        ],
    },
    # Note: assets/wall_upgrade_cost_roi.json has 4 numeric offsets.
    # The offset mode captures them: click the match-center, drag a
    # band relative to it, the tool computes (x1-cx, y1-cy, x2-cx, y2-cy)
    # in REFERENCE pixel space (the 860x732 frame that the bot's
    # `offset * match.Scale` math expects). Without the reference-frame
    # normalization, picking on a 1280x720 device would yield offsets
    # ~50% larger than the user actually drew.
    "cost-roi": {
        "output": "assets/wall_upgrade_cost_roi.json",
        "tasks": [
            {"type": "offset", "key": "_self"},
        ],
    },
}


# --- Helpers --------------------------------------------------------------

def read_device_id() -> str:
    cfg_path = "config.json"
    if os.path.exists(cfg_path):
        try:
            with open(cfg_path, "r") as f:
                cfg = json.load(f)
                return cfg.get("device", {}).get("device_id", "localhost:5555")
        except Exception:
            pass
    return "localhost:5555"


def capture_screen(device_id: str) -> np.ndarray:
    """adb exec-out screencap -p → numpy BGR image. No temp file."""
    cmd = f"adb -s {device_id} exec-out screencap -p"
    proc = subprocess.Popen(cmd.split(), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    png_data, err = proc.communicate()
    if err:
        raise RuntimeError(f"adb screencap failed: {err.decode(errors='replace')}")
    if not png_data:
        raise RuntimeError("adb screencap returned empty payload")
    nparr = np.frombuffer(png_data, np.uint8)
    img = cv2.imdecode(nparr, cv2.IMREAD_COLOR)
    if img is None:
        raise RuntimeError("cv2.imdecode returned None — screencap parse failed")
    return img


def normalize_rect(r: tuple[int, int, int, int]) -> tuple[int, int, int, int]:
    return (min(r[0], r[2]), min(r[1], r[3]), max(r[0], r[2]), max(r[1], r[3]))


def to_ref(physical_px: int, scale: float) -> int:
    return int(round(physical_px / scale))


def rgb_to_hsv(rgb: tuple[int, int, int]) -> tuple[int, int, int]:
    """Convert an (R, G, B) 0-255 tuple to OpenCV's HSV triple.
    OpenCV reports H ∈ [0, 179] and S, V ∈ [0, 255], which is different
    from the textbook H ∈ [0, 360]. The values are still directly
    comparable to what `cv2.cvtColor(BGR, COLOR_BGR2HSV)` would emit
    on a real BGR image — both come from the same LUT chain."""
    arr = np.uint8([[[rgb[2], rgb[1], rgb[0]]]])  # pack as BGR for cv2
    hsv = cv2.cvtColor(arr, cv2.COLOR_BGR2HSV)[0, 0]
    return (int(hsv[0]), int(hsv[1]), int(hsv[2]))


def find_color_blobs(img_bgr: np.ndarray, target_rgb: tuple[int, int, int],
                     tol: int, min_area: int = 100
                     ) -> list[tuple[int, int, int, int, int]]:
    """Scan img_bgr for pixels within Euclidean distance `tol` of target_rgb
    in RGB space (mathematically: sqrt(ΔR² + ΔG² + ΔB²) ≤ tol). Tolerates a
    range of about ±tol on any single channel and tighter in the worst-case
    diagonal where all three channels differ. Runs connected-components on
    the in-tolerance mask after a 3×3 dilation (CoC text renderings
    produce a 1–2px AA halo around glyphs; without dilation "the cost
    number" comes back as several centroids per letter). Returns
    (x1, y1, x2, y2, area) tuples, sorted largest area first so the
    biggest match is `KEY[0]` — that's almost always "the prominent text
    blob" the user is hunting."""
    r, g, b = target_rgb
    diff = img_bgr.astype(np.int32) - np.array([b, g, r], dtype=np.int32)
    dist2 = diff[:, :, 0] ** 2 + diff[:, :, 1] ** 2 + diff[:, :, 2] ** 2
    mask = (dist2 <= tol * tol).astype(np.uint8) * 255
    kernel = cv2.getStructuringElement(cv2.MORPH_RECT, (3, 3))
    mask = cv2.dilate(mask, kernel, iterations=1)
    num_labels, labels, stats, _ = cv2.connectedComponentsWithStats(mask, connectivity=8)
    blobs: list[tuple[int, int, int, int, int]] = []
    # Skip label 0 (background)
    for i in range(1, num_labels):
        x, y, w, h, area = stats[i]
        if area < min_area:
            continue
        blobs.append((int(x), int(y), int(x + w), int(y + h), int(area)))
    blobs.sort(key=lambda b: -b[4])
    return blobs


# --- Snapshot picker ------------------------------------------------------

class Picker:
    """Drives the OpenCV window for one or more selection tasks.

    State machine:
      - Each task has a `type` (point|rect|offset|sample|scan).
      - Mouse interactions advance per-type callbacks; `scan` does its
        work via _auto_fill_task when the task becomes current (no
        mouse interaction required).
      - Enter/Space confirm and advance; 'r' resets; Esc/'q' cancel.
        'r' on a `scan` task re-runs the scan (re-counts blobs).
      - On-screen overlay shows the in-progress rect, prompt, and
        (for `scan`) the detected blob boxes.
    """

    def __init__(self, image: np.ndarray, tasks: list[dict], on_done,
                 min_area: int = 100,
                 scan_params: dict[str, tuple[tuple[int, int, int], int]] | None = None):
        self.image = image
        self.h, self.w = image.shape[:2]
        self.sx = self.w / REF_W
        self.sy = self.h / REF_H
        self.tasks = tasks
        self.idx = 0
        # Per-task state: point=click; rect=(x1,y1,x2,y2); offset=center+(rect)
        self.point: tuple[int, int] | None = None
        self.rect: tuple[int, int, int, int] | None = None
        self.center: tuple[int, int] | None = None
        self.is_drawing = False
        self.start: tuple[int, int] | None = None
        self.results: dict[str, Any] = {}
        # scan_results: keyed by task key. Each value is the latest
        # blob list for that task, kept here so the overlay can draw
        # the boxes every frame and a re-scan via 'r' updates in place
        # without the user thinking about it.
        self.scan_results: dict[str, list[tuple[int, int, int, int, int]]] = {}
        # scan_params: target RGB + tolerance for each scan task.
        # Stashed at construction time so _auto_fill_task can find it
        # by task key without re-deparsing CLI args.
        self.scan_params: dict[str, tuple[tuple[int, int, int], int]] = (
            scan_params if scan_params is not None else {}
        )
        self.on_done = on_done
        self.window = "ClashGO Picker"
        self.cancelled = False
        self.min_area = min_area

    # -- Mouse --
    def _on_mouse(self, event, x, y, flags, _param):
        # Bounds guard. After _commit_task() advances self.idx past
        # the last task (and the break in run() exits the main loop),
        # OpenCV can still have a queued mouse event to dispatch —
        # mouse-button-release events from the click that triggered
        # the final commit get delivered by waitKey *after* self.idx
        # has already moved past len(self.tasks). Without this guard
        # the next `self.tasks[self.idx]["type"]` raises IndexError
        # (the crash the user hit on `picker.py --preset modal-x --tap`).
        # Guard at the top so every branch below is safe.
        if self.idx >= len(self.tasks):
            return
        t = self.tasks[self.idx]["type"]
        if t == "point":
            if event == cv2.EVENT_LBUTTONDOWN:
                self.point = (x, y)
        elif t == "rect":
            if event == cv2.EVENT_LBUTTONDOWN:
                self.start = (x, y)
                self.rect = (x, y, x, y)
                self.is_drawing = True
            elif event == cv2.EVENT_MOUSEMOVE and self.is_drawing:
                self.rect = (self.start[0], self.start[1], x, y)
            elif event == cv2.EVENT_LBUTTONUP:
                self.rect = (self.start[0], self.start[1], x, y)
                self.is_drawing = False
                self.start = None
        elif t == "offset":
            # stage 0: click center. stage 1: drag bound.
            if self.center is None:
                if event == cv2.EVENT_LBUTTONDOWN:
                    self.center = (x, y)
            elif event == cv2.EVENT_LBUTTONDOWN:
                self.start = (x, y)
                self.rect = (x, y, x, y)
                self.is_drawing = True
            elif event == cv2.EVENT_MOUSEMOVE and self.is_drawing:
                self.rect = (self.start[0], self.start[1], x, y)
            elif event == cv2.EVENT_LBUTTONUP:
                self.rect = (self.start[0], self.start[1], x, y)
                self.is_drawing = False
                self.start = None
        elif t == "sample":
            # Same mouse interaction as `point` (single LBUTTONDOWN
            # sets the point). The added work — grabbing the pixel's
            # RGB/HSV/gray — happens at commit time so the color we
            # save always reflects the click position. `scan` needs
            # no mouse interaction at all; it's precomputed in
            # _auto_fill_task when the task becomes current.
            if event == cv2.EVENT_LBUTTONDOWN:
                self.point = (x, y)
        # `scan` is intentionally not handled here. It's a non-
        # interactive auto-fill task; blobs are drawn in the overlay
        # and the user just presses Enter to commit or 'r' to refresh.

    # -- Rendering --
    def _draw_overlay(self, canvas: np.ndarray) -> np.ndarray:
        # Prompt banner
        n = len(self.tasks)
        if self.idx < n:
            t = self.tasks[self.idx]
            kind = t["type"].upper()
            prompt = f"TASK {self.idx + 1}/{n}: {kind} for '{t['key']}' - press Enter when done, 'r' to reset, 'q' to cancel"
        else:
            prompt = "All tasks done. Press Enter to save & exit."
        # Black-out top bar for readability on bright backgrounds
        cv2.rectangle(canvas, (0, 0), (canvas.shape[1], 50), (0, 0, 0), -1)
        cv2.putText(canvas, prompt, (12, 32), cv2.FONT_HERSHEY_SIMPLEX, 0.55,
                    (255, 255, 255), 1, cv2.LINE_AA)

        t = self.tasks[self.idx]["type"] if self.idx < len(self.tasks) else None
        # In-progress visuals
        if t == "rect" and self.rect:
            r = self.rect
            cv2.rectangle(canvas, (r[0], r[1]), (r[2], r[3]), (0, 255, 0), 2)
            cv2.putText(canvas, f"{abs(r[2]-r[0])}x{abs(r[3]-r[1])}",
                        (r[0], max(r[1] - 6, 12)), cv2.FONT_HERSHEY_SIMPLEX, 0.5,
                        (0, 255, 0), 1)
        elif t == "offset":
            if self.center:
                cv2.circle(canvas, self.center, 8, (255, 0, 255), -1)
                cv2.putText(canvas, "center", (self.center[0] + 12, self.center[1] - 6),
                            cv2.FONT_HERSHEY_SIMPLEX, 0.5, (255, 0, 255), 1)
            if self.rect and self.center:
                r = self.rect
                cv2.rectangle(canvas, (r[0], r[1]), (r[2], r[3]), (0, 255, 255), 2)
                # offsets preview
                cx, cy = self.center
                offs = (r[0] - cx, r[1] - cy, r[2] - cx, r[3] - cy)
                cv2.putText(canvas,
                            f"offs={offs[0]},{offs[1]}|{offs[2]},{offs[3]}",
                            (r[0], min(r[3] + 18, canvas.shape[0] - 6)),
                            cv2.FONT_HERSHEY_SIMPLEX, 0.5, (0, 255, 255), 1)
        elif t == "point" and self.point:
            cv2.circle(canvas, self.point, 8, (0, 255, 255), -1)
            cv2.putText(canvas, f"({self.point[0]}, {self.point[1]})  ref=({to_ref(self.point[0], self.sx)}, {to_ref(self.point[1], self.sy)})",
                        (self.point[0] + 12, self.point[1] - 6),
                        cv2.FONT_HERSHEY_SIMPLEX, 0.5, (0, 255, 255), 1)
        elif t == "sample" and self.point:
            # Show a colored swatch at the click with an RGB/HSV/gray
            # readout so the user can decide live whether the picked
            # pixel matches what they expected (e.g. "did I just click
            # a red letter or a shadow?"). The swatch uses OpenCV's
            # BGR ordering internally but the readout uses RGB to
            # match the human mental model.
            x, y = self.point
            b, g, rr = (int(c) for c in self.image[y, x])
            rgb = (rr, g, b)
            h, s, v = rgb_to_hsv(rgb)
            gray = int(0.299 * rr + 0.587 * g + 0.114 * b)
            cv2.circle(canvas, self.point, 10, (0, 255, 255), -1)
            cv2.circle(canvas, self.point, 10, (255, 255, 255), 1)
            # Color swatch (40x40 in BGR)
            sw_x = min(x + 18, canvas.shape[1] - 44)
            sw_y = max(y - 36, 56)
            swatch_bgr = (b, g, rr)
            cv2.rectangle(canvas, (sw_x, sw_y), (sw_x + 40, sw_y + 40),
                          swatch_bgr, -1)
            cv2.rectangle(canvas, (sw_x, sw_y), (sw_x + 40, sw_y + 40),
                          (255, 255, 255), 1)
            cv2.putText(canvas, f"RGB({rr},{g},{b})", (sw_x, sw_y + 56),
                        cv2.FONT_HERSHEY_SIMPLEX, 0.45, (255, 255, 255), 1,
                        cv2.LINE_AA)
            cv2.putText(canvas, f"HSV({h},{s},{v}) gray={gray}",
                        (sw_x, sw_y + 72), cv2.FONT_HERSHEY_SIMPLEX, 0.45,
                        (255, 255, 255), 1, cv2.LINE_AA)
        elif t == "scan":
            # Draw the detected blobs as numbered red boxes (numbered
            # largest first, matching saved JSON order). The user can
            # see at a glance how many blobs the scan found and
            # whether the largest box matches their mental target.
            # The blobs overlay is refreshed on every loop iteration
            # when self.idx matches the current scan task (see run()),
            # so a re-scan via 'r' updates the boxes within a frame.
            tkey = self.tasks[self.idx]["key"]
            blobs = self.scan_results.get(tkey, [])
            for i, b in enumerate(blobs[:8]):  # cap overlay at top 8
                x1, y1, x2, y2, area = b
                color = (0, 0, 255) if i == 0 else (60, 60, 220)
                cv2.rectangle(canvas, (x1, y1), (x2, y2), color, 2)
                cv2.putText(canvas, f"#{i} area={area}",
                            (x1, max(y1 - 6, 56)),
                            cv2.FONT_HERSHEY_SIMPLEX, 0.5, color, 1,
                            cv2.LINE_AA)
            if len(blobs) > 8:
                cv2.putText(canvas,
                            f"... +{len(blobs) - 8} more",
                            (12, canvas.shape[0] - 12),
                            cv2.FONT_HERSHEY_SIMPLEX, 0.5, (0, 0, 255),
                            1, cv2.LINE_AA)
            if not blobs:
                cv2.putText(canvas,
                            "NO BLOBS FOUND — adjust R,G,B or tolerance",
                            (12, 64), cv2.FONT_HERSHEY_SIMPLEX, 0.55,
                            (0, 0, 255), 2, cv2.LINE_AA)
        return canvas

    # -- Advance / finalize --
    def _commit_task(self) -> bool:
        """Commit the current task's selection to self.results. Returns
        False if the current task has no valid selection."""
        t = self.tasks[self.idx]
        key = t["key"]
        if t["type"] == "point":
            if not self.point:
                print("  No point placed — click first.")
                return False
            x, y = self.point
            # loadWallUpgradeXButton treats (0,0) as a "no-override"
            # sentinel — saving exactly (0,0) would be silently
            # discarded by the bot. If the user genuinely wants to tap
            # the top-left corner, nudge to (1,1) and warn loudly so
            # the divergence is visible in the picker transcript.
            if x == 0 and y == 0:
                print("  WARNING: picked (0,0) — bot treats this as the "
                      "no-override sentinel and will fall back to its "
                      "default. Saving as (1,1); drag a few pixels off "
                      "the corner if you actually mean the top-left.")
                x, y = 1, 1
            self.results[key] = {"x": x, "y": y}
        elif t["type"] == "rect":
            if not self.rect:
                print("  No rectangle drawn — drag first.")
                return False
            r = normalize_rect(self.rect)
            self.results[key] = {"x1": r[0], "y1": r[1], "x2": r[2], "y2": r[3]}
        elif t["type"] == "offset":
            if not self.center or not self.rect:
                print("  Need both a center click AND a drag — try again.")
                return False
            r = normalize_rect(self.rect)
            # IMPORTANT: internal/bot/wall_upgrade.go multiplies each
            # offset by match.Scale before applying it to the match
            # center. That means the offsets live in REFERENCE pixel
            # space (the 860x732 frame), not the captured physical
            # space. Normalize center + rect → ref first, then subtract.
            # Without this normalization on a non-reference-sized screen
            # (e.g. 1280x720) the picker would emit offsets in *device*
            # pixels that the bot would then re-scale, producing a band
            # ~50% larger than the user actually drew.
            cx_r = to_ref(self.center[0], self.sx)
            cy_r = to_ref(self.center[1], self.sy)
            r_ref = (
                to_ref(r[0], self.sx),
                to_ref(r[1], self.sy),
                to_ref(r[2], self.sx),
                to_ref(r[3], self.sy),
            )
            self.results["x_min_off"] = r_ref[0] - cx_r
            self.results["y_min_off"] = r_ref[1] - cy_r
            self.results["x_max_off"] = r_ref[2] - cx_r
            self.results["y_max_off"] = r_ref[3] - cy_r
        elif t["type"] == "sample":
            if not self.point:
                print("  No point placed — click first.")
                return False
            x, y = self.point
            # Compute the pixel's color at commit time so the saved
            # values always reflect the picked coord (the user could
            # have moved the mouse between click and Enter). RGB order
            # in the JSON matches the human mental model; HSV is
            # OpenCV-scale (H 0-179, S/V 0-255). gray is the BT.601
            # luminance — matches what the bot's grayscale templates
            # use downstream.
            b, g, rr = (int(c) for c in self.image[y, x])
            rgb = (rr, g, b)
            h, s, v = rgb_to_hsv(rgb)
            gray = int(0.299 * rr + 0.587 * g + 0.114 * b)
            self.results[key] = {
                "x": x, "y": y,
                "rgb": [rr, g, b],
                "hsv": [h, s, v],
                "gray": gray,
            }
            print(f"  sampled at ({x},{y}): RGB{rgb} HSV({h},{s},{v}) gray={gray}")
        elif t["type"] == "scan":
            # Result was precomputed in _auto_fill_task; copy blobs
            # to self.results as a sorted rect array under KEY.
            blobs = self.scan_results.get(key, [])
            self.results[key] = [
                {"x1": b[0], "y1": b[1], "x2": b[2], "y2": b[3], "area": b[4]}
                for b in blobs
            ]
            print(f"  scan '{key}': {len(blobs)} blob(s) saved")
        # Reset for next task
        self.point = None
        self.rect = None
        self.center = None
        self.start = None
        self.is_drawing = False
        self.idx += 1
        return True

    # -- Auto-fill (no mouse interaction) --
    def _auto_fill_task(self, force: bool = False) -> None:
        """Pre-compute the current task if it is `scan` (no mouse
        interaction required). Called by run() on every loop iteration;
        the `force=True` path is used by 'r' to re-run on user demand.

        For non-scan task types this is a no-op so the existing flow
        is unaffected. Stores the result under self.scan_results[key]
        keyed by the task's KEY (not self.idx), so re-entering this
        task later keeps its blob list."""
        if self.idx >= len(self.tasks):
            return
        t = self.tasks[self.idx]
        if t["type"] != "scan":
            return
        key = t["key"]
        if (not force) and key in self.scan_results:
            return
        # Defensive guard: if a caller wires a `scan` task without
        # supplying the matching RGB + tolerance (e.g. a unit test or
        # a future caller hand-building a Picker), skip the task
        # rather than crashing with KeyError. The CLI validates
        # args.scan in main(), so this only fires for bypass paths.
        if key not in self.scan_params:
            print(f"  WARNING: scan task '{key}' has no params wired "
                  f"(missing entry in scan_params). Skipping — save "
                  f"will record empty blobs.")
            self.scan_results[key] = []
            return
        target_rgb, tol = self.scan_params[key]
        blobs = find_color_blobs(self.image, target_rgb, tol,
                                 min_area=self.min_area)
        self.scan_results[key] = blobs

    def run(self) -> dict[str, Any] | None:
        cv2.namedWindow(self.window, cv2.WINDOW_NORMAL)
        cv2.resizeWindow(self.window, min(self.w, 1280), min(self.h, 1024))
        cv2.setMouseCallback(self.window, self._on_mouse)

        # Initial instructions
        print("-" * 60)
        print(f"Picker opened on {self.w}x{self.h} screen {self.sx=:.3f} {self.sy=:.3f}")
        print(f"Reference: {REF_W}x{REF_H} (bot's calibration baseline)")
        print("  - Drag rectangles; click for points; scan = auto.")
        print("  - Enter/Space: confirm task. 'r': reset/rescan. 'q'/Esc: cancel.")
        print("-" * 60)

        while True:
            # Drive scan tasks through their pre-computed result.
            # For point/rect/offset/sample tasks this is a no-op.
            self._auto_fill_task()
            canvas = self.image.copy()
            canvas = self._draw_overlay(canvas)
            cv2.imshow(self.window, canvas)
            key = cv2.waitKey(30) & 0xFF
            if key in (ord("q"), ord("Q"), 27):
                self.cancelled = True
                break
            if key in (ord("r"), ord("R")):
                self.point = None
                self.rect = None
                self.center = None
                self.start = None
                self.is_drawing = False
                # Re-run the scan for the current task if it's auto-fill.
                self._auto_fill_task(force=True)
                if self.idx < len(self.tasks) and self.tasks[self.idx]["type"] == "scan":
                    tkey = self.tasks[self.idx]["key"]
                    n = len(self.scan_results.get(tkey, []))
                    print(f"  Rescanned '{tkey}': {n} blob(s)")
                else:
                    print("  Reset current task.")
                continue
            if key in (13, 10, 32):  # Enter / Space
                if self.idx >= len(self.tasks):
                    # All done —> finalize
                    break
                if self._commit_task():
                    if self.idx < len(self.tasks):
                        nt = self.tasks[self.idx]
                        print(f"  -> task {self.idx + 1}/{len(self.tasks)}: {nt['type']} for '{nt['key']}'")
            try:
                if cv2.getWindowProperty(self.window, cv2.WND_PROP_VISIBLE) < 1:
                    self.cancelled = True
                    break
            except cv2.error:
                self.cancelled = True
                break

        # Belt-and-suspenders: unbind the mouse callback BEFORE
        # destroying the window. macOS-level mouse events can still
        # be in the OS queue and may reach the Python callback after
        # the main loop has exited; the bounds guard in _on_mouse
        # covers this, but unregistering here is a cleaner shutdown
        # than relying on the bounds guard alone.
        cv2.setMouseCallback(self.window, lambda *_: None)
        cv2.destroyAllWindows()
        if self.cancelled:
            return None
        return self.results


# --- CLI ------------------------------------------------------------------

def _build_argparser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="picker.py",
        description="Unified visual calibration picker for ClashGO. "
                    "Capture points, rects, and offset-ROIs from the live "
                    "device and save JSON asset files.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "examples:\n"
            "  picker.py --preset menu           # builder menu ROI (phys+ref)\n"
            "  picker.py --preset buttons        # gold + elixir upgrade buttons\n"
            "  picker.py --preset modal-x        # modal X-button (tap target)\n"
            "  picker.py --preset confirm        # post-upgrade Confirm button (blind tap)\n"
            "  picker.py --preset battle-loot    # 2 search columns\n"
            "  picker.py --preset cost-roi       # 4 relative offsets\n"
            "  picker.py -o tmp.json --rect gold --rect elixir --point x_button\n"
        ),
    )
    p.add_argument("--preset", choices=list(PRESETS.keys()),
                   help="Built-in shortcut for an existing asset JSON.")
    p.add_argument("-o", "--output", help="Output JSON file (overrides preset default).")
    p.add_argument("--rect", action="append", default=[],
                   metavar="KEY", help="Add a '--rect KEY' for each rect to capture.")
    p.add_argument("--point", action="append", default=[],
                   metavar="KEY", help="Add a '--point KEY' for each point to capture.")
    p.add_argument("--offset", action="append", default=[],
                   metavar="KEY", help="Add an '--offset KEY' for an offset-band capture.")
    p.add_argument("--sample", action="append", default=[],
                   metavar="KEY",
                   help="Add a '--sample KEY' that clicks a pixel and records its "
                        "RGB + HSV + gray values at that point. Use this to discover "
                        "the exact color of a UI element before writing a matching "
                        "--scan for it. Saves {x, y, rgb, hsv, gray} under KEY.")
    p.add_argument("--scan", action="append", nargs=2, default=[],
                   metavar=("KEY", "R,G,B,TOL"),
                   help="Add a '--scan KEY R,G,B,TOL' task that auto-detects color "
                        "blobs matching R,G,B within Euclidean tolerance TOL in RGB "
                        "space. Saves an array of {x1,y1,x2,y2,area} rects sorted "
                        "largest-first under KEY. Use to find ROIs that match a "
                        "known color (red text, white text, gold-elixir icon, etc.) "
                        "without manually dragging.")
    p.add_argument("--min-area", type=int, default=100,
                   help="Minimum blob area (pixels) for --scan tasks. Default 100 "
                        "drops anti-aliased noise; raise to ignore small/texture "
                        "blobs, lower to capture fine detail. Only affects --scan.")
    p.add_argument("--device", help="Override ADB device id (else reads config.json).")
    p.add_argument("--tap", action="store_true",
                   help="After committing any task that produced an "
                        "{x,y} coord (--point or --sample), send "
                        "`adb shell input tap x y` to the device so the "
                        "user can verify the chosen target lands where "
                        "intended. Replaces tools/pinpoint.py's left-"
                        "click-and-instant-tap behavior. Has no effect "
                        "on --rect/--offset/--scan tasks.")
    return p


def main() -> int:
    args = _build_argparser().parse_args()

    # Resolve tasks: preset first, then explicit --rect/--point/--offset
    # additively (power users can mix preset + extras). --sample and
    # --scan are *not* task-list constructs that the picker loop
    # walks: --sample flows through the picker as another interactive
    # task (one click per task), but --scan is special — its target
    # color + tolerance live in args.scan and need to be threaded
    # into the Picker instance so _auto_fill_task can fire them. We
    # validate the syntax of each --scan here and stash the params
    # for that path.
    scan_params: dict[str, tuple[tuple[int, int, int], int]] = {}
    invalid_scans: list[str] = []
    for key, spec_str in args.scan:
        parts = spec_str.split(",")
        if len(parts) != 4:
            invalid_scans.append(f"{key}: expected R,G,B,TOL (got '{spec_str}')")
            continue
        try:
            r, g, b, tol = (int(p) for p in parts)
        except ValueError:
            invalid_scans.append(f"{key}: non-integer component in '{spec_str}'")
            continue
        if tol < 0 or tol > 255:
            invalid_scans.append(f"{key}: tol={tol} out of [0,255] range")
            continue
        scan_params[key] = ((r, g, b), tol)

    if invalid_scans:
        for msg in invalid_scans:
            print(f"error: --scan: {msg}", file=sys.stderr)
        return 2

    if args.preset:
        spec = PRESETS[args.preset]
        tasks = list(spec["tasks"])
        output = spec["output"] if args.output is None else args.output
    else:
        tasks = []
        for k in args.rect:
            tasks.append({"type": "rect", "key": k})
        for k in args.point:
            tasks.append({"type": "point", "key": k})
        for k in args.offset:
            tasks.append({"type": "offset", "key": k})
        for k in args.sample:
            tasks.append({"type": "sample", "key": k})
        for key, _ in args.scan:
            tasks.append({"type": "scan", "key": key})
        output = args.output

    if not tasks:
        print("error: no tasks specified. Use --preset or --rect/--point/--offset.",
              file=sys.stderr)
        return 2
    if not output:
        print("error: --output (or a preset that supplies one) is required.", file=sys.stderr)
        return 2

    device_id = args.device or read_device_id()
    print(f"Using ADB device: {device_id}")
    try:
        img = capture_screen(device_id)
    except Exception as e:
        print(f"FATAL: adb screencap failed: {e}", file=sys.stderr)
        return 1

    picker = Picker(img, tasks, on_done=None,
                    min_area=args.min_area,
                    scan_params=scan_params)
    results = picker.run()
    if results is None:
        print("Cancelled. No save.")
        return 1

    # Preset-specific post-processing.
    # --preset menu: derive the `reference` block from the captured
    # `physical` rect by dividing each coord by the device's scale
    # factor. The selector only consumed the `physical` half of
    # builder_menu_roi.json historically — the user dragged the same
    # rect twice. Here we collapse that into one drag and produce the
    # reference block deterministically.
    if args.preset == "menu" and "physical" in results:
        p = results["physical"]
        sx, sy = picker.sx, picker.sy
        results["reference"] = {
            "x1": int(round(p["x1"] / sx)),
            "y1": int(round(p["y1"] / sy)),
            "x2": int(round(p["x2"] / sx)),
            "y2": int(round(p["y2"] / sy)),
        }
        print(f"  -> derived reference block from physical (sx={sx:.3f}, sy={sy:.3f})")

    # --tap: replay the captured point on the device so the user can
    # verify it lands on-target. Keeps backward compat with
    # tools/pinpoint.py's "click = instant-tap" affordance for the
    # most common case (modal X-button, single tap targets).
    if args.tap:
        for k, v in results.items():
            if isinstance(v, dict) and "x" in v and "y" in v:
                tx, ty = v["x"], v["y"]
                print(f"  -> tap device at ({tx}, {ty}) for key '{k}'")
                cmd = f"adb -s {device_id} shell input tap {tx} {ty}"
                subprocess.run(cmd.split(), check=False)

    # Merge into output JSON. If a preset supplies a sorted-key shape
    # (e.g. cost-roi emits fixed keys), the dict is already in the right
    # order. For nested presets, the user picked `dict[key]` so the
    # nesting is correct.
    #
    # SUBTLE BUT IMPORTANT: we read the existing JSON (if any) and
    # patch-merge with the freshly-picked results. This lets the user
    # pick a SECOND rect (e.g. --rect x_popup_roi_alt) without nuking
    # the primary x_popup_roi that was calibrated in a prior run.
    # Without this merge, the picker would silently regress multi-rect
    # assets to single-rect on every re-pick — the failure mode the
    # wall-upgrade flow's X-retry chain relies on (it reads
    # x_popup_roi + x_popup_roi_alt in a single 10-candidate tap
    # sequence; losing the primary would halve coverage).
    #
    # Fallback to empty dict on parse error / non-dict root: a Ctrl-C'd
    # previous run might have left a half-written file; we want the
    # new run to recover gracefully rather than crash on the partial
    # payload. The parse-failure warning is loud so the user notices
    # and investigates if it happens repeatedly.
    os.makedirs(os.path.dirname(output) or ".", exist_ok=True)
    existing: dict[str, Any] = {}
    if os.path.exists(output):
        try:
            with open(output, "r") as f:
                loaded = json.load(f)
            if isinstance(loaded, dict):
                existing = loaded
            else:
                print(f"  WARNING: {output} contains a non-dict root "
                      f"({type(loaded).__name__}); ignoring existing "
                      f"contents and writing fresh results.")
        except (json.JSONDecodeError, OSError, ValueError) as e:
            print(f"  WARNING: {output} could not be parsed ({e!r}); "
                  f"ignoring existing contents and writing fresh results.")

    # dict.update preserves insertion order on collision (overwrites
    # value at the existing key's position, appends new keys at the
    # end). So the merged JSON has: existing keys in their original
    # order, freshly-picked keys appended (or replaced in place). The
    # diff vs the prior file is therefore "the new rects only" in git.
    merged: dict[str, Any] = dict(existing)
    merged.update(results)

    with open(output, "w") as f:
        json.dump(merged, f, indent=2)
    print("-" * 60)
    print(f"SAVED to {output}")
    print(json.dumps(merged, indent=2))

    # Merge audit: surface exactly what was preserved vs replaced so
    # re-pick operations don't silently lose data. Only prints when an
    # existing file was already present (first-time picks would just
    # produce "preserved 0 keys" — noise).
    if existing:
        merge_keys = set(results.keys())
        preserved = sorted(set(existing.keys()) - merge_keys)
        replaced = sorted(merge_keys & set(existing.keys()))
        new_keys = sorted(merge_keys - set(existing.keys()))
        msgs = []
        if preserved:
            msgs.append(f"preserved {len(preserved)} unrelated key(s): "
                        f"{', '.join(preserved)}")
        if replaced:
            msgs.append(f"replaced {len(replaced)} existing key(s): "
                        f"{', '.join(replaced)}")
        if new_keys:
            msgs.append(f"added {len(new_keys)} new key(s): "
                        f"{', '.join(new_keys)}")
        if msgs:
            print("  Merge audit: " + "; ".join(msgs) + ".")
    print("Reference coords (860x732) are computed alongside physical in the picker.")
    print("Open the asset JSON file the bot reads and verify the keys match what wall_upgrade.go expects.")
    print("-" * 60)
    return 0


if __name__ == "__main__":
    sys.exit(main())
