#!/usr/bin/env python3
"""
select_wall_upgrade_buttons.py — drag-rectangle ROI picker for the wall-upgrade
flow. Two rectangles per session (gold-cost button on the left, elixir-cost
button on the right), saved to assets/wall_upgrade_buttons.json with both
physical and reference (860x732) coordinates so wall_upgrade.go can use them
verbatim regardless of the device's actual screen size.

Pattern modeled after tools/internal/select_builder_roi.py. Centroid of each
rectangle is the tap target the bot uses; rectangle corners bound the area
the bot inspects for cost-text color and confirm-button presence.

Controls:
  - Left-click + drag: draw a rectangle
  - 'g': label current rect as GOLD button
  - 'e': label current rect as ELIXIR button
  - 'r': reset both rectangles
  - 's' / Enter / Space: SAVE both rectangles to assets/wall_upgrade_buttons.json
  - 'q' / Esc: exit without saving
"""
import cv2
import json
import os
import subprocess
import sys

import numpy as np

# Reference resolution the bot's calibration is tuned for.
REF_W, REF_H = 860, 732

# State
img = None
clone = None
gold_rect = None   # (x1, y1, x2, y2) once labelled
elixir_rect = None
current_rect = None
is_drawing = False


def mouse_callback(event, x, y, flags, param):
    global current_rect, is_drawing
    if event == cv2.EVENT_LBUTTONDOWN:
        current_rect = [x, y, x, y]
        is_drawing = True
    elif event == cv2.EVENT_MOUSEMOVE and is_drawing:
        current_rect[2] = x
        current_rect[3] = y
    elif event == cv2.EVENT_LBUTTONUP:
        current_rect[2] = x
        current_rect[3] = y
        is_drawing = False


def capture_screen():
    """Capture the live device screen via adb exec-out (no temp file)."""
    device_id = "localhost:5555"
    cfg_path = "config.json"
    if os.path.exists(cfg_path):
        try:
            with open(cfg_path, "r") as f:
                cfg = json.load(f)
                device_id = cfg.get("device", {}).get("device_id", "localhost:5555")
        except Exception:
            pass
    cmd = f"adb -s {device_id} exec-out screencap -p"
    proc = subprocess.Popen(cmd.split(), stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    png_data, err = proc.communicate()
    if err:
        raise RuntimeError(f"adb screencap failed: {err.decode()}")
    nparr = np.frombuffer(png_data, np.uint8)
    img = cv2.imdecode(nparr, cv2.IMREAD_COLOR)
    if img is None:
        raise RuntimeError("cv2.imdecode returned None — screencap parse failed")
    return img


def draw_overlays(canvas):
    """Draw labelled RECTs in green (gold) and purple (elixir), plus the
    in-progress rect in yellow."""
    if gold_rect is not None:
        cv2.rectangle(canvas, (gold_rect[0], gold_rect[1]),
                      (gold_rect[2], gold_rect[3]), (0, 255, 0), 2)
        cx, cy = (gold_rect[0] + gold_rect[2]) // 2, (gold_rect[1] + gold_rect[3]) // 2
        cv2.putText(canvas, "GOLD", (gold_rect[0], gold_rect[1] - 8),
                    cv2.FONT_HERSHEY_SIMPLEX, 0.6, (0, 255, 0), 2)
        cv2.circle(canvas, (cx, cy), 6, (0, 255, 0), -1)
    if elixir_rect is not None:
        cv2.rectangle(canvas, (elixir_rect[0], elixir_rect[1]),
                      (elixir_rect[2], elixir_rect[3]), (255, 0, 255), 2)
        cx, cy = (elixir_rect[0] + elixir_rect[2]) // 2, (elixir_rect[1] + elixir_rect[3]) // 2
        cv2.putText(canvas, "ELIXIR", (elixir_rect[0], elixir_rect[1] - 8),
                    cv2.FONT_HERSHEY_SIMPLEX, 0.6, (255, 0, 255), 2)
        cv2.circle(canvas, (cx, cy), 6, (255, 0, 255), -1)
    if is_drawing and current_rect is not None and not (
        current_rect[0] == current_rect[2] and current_rect[1] == current_rect[3]
    ):
        cx0, cy0 = current_rect[0], current_rect[1]
        cx1, cy1 = current_rect[2], current_rect[3]
        cv2.rectangle(canvas, (cx0, cy0), (cx1, cy1), (0, 255, 255), 2)
        cv2.putText(canvas, "PRESS g or e", (cx0, cy0 - 8),
                    cv2.FONT_HERSHEY_SIMPLEX, 0.5, (0, 255, 255), 1)


def main():
    global img, clone, gold_rect, elixir_rect, current_rect, is_drawing

    print("Capturing live screen via ADB...")
    try:
        img = capture_screen()
    except Exception as e:
        print(f"FATAL: {e}")
        sys.exit(1)

    h, w = img.shape[:2]
    sx, sy = w / REF_W, h / REF_H
    print(f"Screen: {w}x{h} | scale x={sx:.3f} y={sy:.3f} (ref {REF_W}x{REF_H})")

    clone = img.copy()
    window_title = "Select Wall Upgrade Buttons - Drag, then g/e/s"
    cv2.namedWindow(window_title, cv2.WINDOW_NORMAL)
    cv2.resizeWindow(window_title, min(w, 1200), min(h, 1000))
    cv2.setMouseCallback(window_title, mouse_callback)

    print("=" * 64)
    print("INSTRUCTIONS:")
    print("  1. Wait for the wall-upgrade tray to be on screen (run the bot's")
    print("     wall-upgrade flow up to the tray before launching this tool).")
    print("  2. Drag a rectangle around the LEFT (gold) upgrade button.")
    print("     Press 'g' to label it as GOLD.")
    print("  3. Drag a rectangle around the RIGHT (elixir) upgrade button.")
    print("     Press 'e' to label it as ELIXIR.")
    print("  4. Press 's' / Enter / Space to SAVE -> assets/wall_upgrade_buttons.json")
    print("     Or 'r' to reset, 'q' / Esc to cancel.")
    print("=" * 64)

    while True:
        canvas = clone.copy()
        draw_overlays(canvas)
        cv2.imshow(window_title, canvas)
        key = cv2.waitKey(30) & 0xFF

        if key in (ord("g"), ord("G")):
            if current_rect is None or (current_rect[0] == current_rect[2] and current_rect[1] == current_rect[3]):
                print("No rectangle drawn yet — drag first.")
                continue
            gold_rect = normalize_rect(current_rect)
            print(f"GOLD rect set: {gold_rect}")
            current_rect = None
            is_drawing = False
        elif key in (ord("e"), ord("E")):
            if current_rect is None or (current_rect[0] == current_rect[2] and current_rect[1] == current_rect[3]):
                print("No rectangle drawn yet — drag first.")
                continue
            elixir_rect = normalize_rect(current_rect)
            print(f"ELIXIR rect set: {elixir_rect}")
            current_rect = None
            is_drawing = False
        elif key in (ord("r"), ord("R")):
            gold_rect = None
            elixir_rect = None
            current_rect = None
            is_drawing = False
            print("Reset both rectangles.")
        elif key in (ord("s"), ord("S"), 13, 10, 32):
            if gold_rect is None or elixir_rect is None:
                print("Need BOTH gold ('g') and elixir ('e') rectangles before saving.")
                continue
            save_buttons_json(gold_rect, elixir_rect, w, h, sx, sy)
            break
        elif key in (27, ord("q"), ord("Q")):
            print("Cancelled — no save.")
            break

        try:
            if cv2.getWindowProperty(window_title, cv2.WND_PROP_VISIBLE) < 1:
                break
        except cv2.error:
            break

    cv2.destroyAllWindows()


def normalize_rect(r):
    return (min(r[0], r[2]), min(r[1], r[3]), max(r[0], r[2]), max(r[1], r[3]))


def to_ref(x, s):
    return int(round(x / s))


def save_buttons_json(gold, elixir, w, h, sx, sy):
    # Flat schema (parses cleanly into Go's map[string]int and then
    # image.Rect). The previous nested physical/reference split couldn't
    # unmarshal correctly because image.Rectangle's Go fields are
    # Min/Max Point, not a flat x1/y1/x2/y2 dict.
    out = {
        "gold": {"x1": gold[0], "y1": gold[1], "x2": gold[2], "y2": gold[3]},
        "elixir": {"x1": elixir[0], "y1": elixir[1], "x2": elixir[2], "y2": elixir[3]},
    }
    # Ensure the assets dir exists
    os.makedirs("assets", exist_ok=True)
    out_path = "assets/wall_upgrade_buttons.json"
    with open(out_path, "w") as f:
        json.dump(out, f, indent=2)
    print("-" * 64)
    print("SAVED to", out_path)
    print(f"  GOLD rect:   x1={gold[0]} y1={gold[1]} x2={gold[2]} y2={gold[3]} -> centroid ({to_ref((gold[0] + gold[2]) // 2, sx)}, {to_ref((gold[1] + gold[3]) // 2, sy)})  [ref coords]")
    print(f"  ELIXIR rect: x1={elixir[0]} y1={elixir[1]} x2={elixir[2]} y2={elixir[3]} -> centroid ({to_ref((elixir[0] + elixir[2]) // 2, sx)}, {to_ref((elixir[1] + elixir[3]) // 2, sy)})  [ref coords]")
    print("Centroid is the tap target wall_upgrade.go will use.")
    print("-" * 64)


if __name__ == "__main__":
    main()
