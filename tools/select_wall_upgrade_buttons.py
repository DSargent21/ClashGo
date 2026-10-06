#!/usr/bin/env python3
"""
select_wall_upgrade_buttons.py — guided picking session for the whole
wall-upgrade flow.

This used to pick exactly two boxes (the gold and elixir upgrade buttons) and
write the drag coordinates straight into assets/wall_upgrade_buttons.json. Two
things were wrong with that:

  1. the loader reads that file as 860x732 REFERENCE coordinates and re-maps them
     through the HUD law, so raw drag pixels were moved on every run — on the live
     1280x720 device the recorded boxes tracked no widget at all, and the bot's
     blind taps landed on builder-menu rows;
  2. the flow needs more than two boxes: the builder-head button that opens the
     menu, the menu ROI to search, the post-upgrade Confirm button and the gem-buy
     popup's X.

So the session now walks every box in the order the bot uses them, and each
accepted drag is converted by `build/bin/refmap` into a `reference` block that the
HUD law maps back onto exactly the box you drew. The tool refuses to invent one:
if a box falls in a band seam the reference frame cannot express, refmap says so
and the session asks you to nudge it.

Prepare the game first, then run:

    make refmap                                  # one-time build of the converter
    python3 tools/select_wall_upgrade_buttons.py

Steps, each on a FRESH screencap so you can move the game in between:

    1  builder-head button      village top bar          -> assets/builder_button.json
    2  builder menu panel       the upgrades list        -> assets/builder_menu_roi.json
    3  gold upgrade button      wall tray, gold cost     -> assets/wall_upgrade_buttons.json
    4  elixir upgrade button    wall tray, elixir cost   -> assets/wall_upgrade_buttons.json
    5  Confirm button           post-upgrade dialog      -> assets/wall_upgrade_confirm.json
    6  gem-buy popup X          "buy with gems" dialog   -> assets/wall_upgrade_x_roi.json
    7  chained popup X          optional, Enter to skip  -> assets/wall_upgrade_x_roi.json

Controls, inside the window:
    left-drag        draw the box
    Enter / Space    accept it and move on
    r                redraw the current box
    s                re-capture the screen (game state moved since this step began)
    q / Esc          quit without writing anything
"""
import argparse
import json
import os
import shutil
import subprocess
import sys

REF_W, REF_H = 860, 732
REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
REFMAP = os.path.join(REPO_ROOT, "build", "bin", "refmap")

# One entry per step. `schema`/`key` say which refmap schema the box belongs to and
# which key inside it; `optional` marks a step the user may skip.
STEPS = [
    dict(step=1, key="builder_button", schema="builder", out="assets/builder_button.json",
         title="builder-head button (village top bar)",
         hint="drag around the builder icon that opens the upgrades menu"),
    dict(step=2, key="physical", schema="menu", out="assets/builder_menu_roi.json",
         title="builder menu panel",
         hint="drag around the whole scrollable upgrades list"),
    dict(step=3, key="gold", schema="buttons", out="assets/wall_upgrade_buttons.json",
         title="GOLD upgrade button (wall tray)",
         hint="drag around the gold-cost upgrade button"),
    dict(step=4, key="elixir", schema="buttons", out="assets/wall_upgrade_buttons.json",
         title="ELIXIR upgrade button (wall tray)",
         hint="drag around the elixir-cost upgrade button"),
    dict(step=5, key="confirm_button", schema="confirm", out="assets/wall_upgrade_confirm.json",
         title="Confirm button (post-upgrade dialog)",
         hint="drag around the Confirm/Yes button of the upgrade dialog"),
    dict(step=6, key="x_popup_roi", schema="x_roi", out="assets/wall_upgrade_x_roi.json",
         title="X of the 'buy with gems' popup",
         hint="drag around the popup's close X"),
    dict(step=7, key="x_popup_roi_alt", schema="x_roi", out="assets/wall_upgrade_x_roi.json",
         title="X of the chained popup (optional)",
         hint="Enter to skip if this client shows only one popup"),
]


def parse_args():
    p = argparse.ArgumentParser(description=__doc__,
                                formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--check", action="store_true",
                   help="print the plan and verify refmap + adb are ready, then exit")
    p.add_argument("--device", help="override the adb device id (else config.json)")
    p.add_argument("--only", help="run a single step by key (e.g. --only gold)")
    p.add_argument("--k", type=float, default=0.0,
                   help="pin the display scale (default: config's device.display_scale)")
    return p.parse_args()


def read_config():
    for path in (os.path.join(REPO_ROOT, "config.json"),
                 os.path.expanduser("~/Library/Application Support/ClashGO/config.json")):
        if os.path.exists(path):
            try:
                with open(path) as f:
                    return json.load(f)
            except (OSError, ValueError):
                continue
    return {}


def resolve_device(cfg, override):
    return override or cfg.get("device", {}).get("device_id", "localhost:5555")


def resolve_scale(cfg, override):
    if override > 0:
        return override
    return float(cfg.get("device", {}).get("display_scale", 0) or 0)


def capture_screen(device_id):
    """Capture the live device screen via adb exec-out (no temp file)."""
    import numpy as np
    import cv2
    cmd = ["adb", "-s", device_id, "exec-out", "screencap", "-p"]
    proc = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    png_data, err = proc.communicate()
    if not png_data:
        raise RuntimeError(f"adb screencap produced nothing: {err.decode(errors='replace')}")
    img = cv2.imdecode(np.frombuffer(png_data, np.uint8), cv2.IMREAD_COLOR)
    if img is None:
        raise RuntimeError("cv2.imdecode returned None — screencap parse failed")
    return img


def refmap_convert(device_w, device_h, scale, schema, drags, optional=()):
    """Hand the drags to cmd/refmap and return the asset JSON it produced.

    Raises RuntimeError with refmap's own message on any refusal, so the session
    can tell the user to nudge a box instead of writing an asset that does not fit.
    """
    if not os.path.exists(REFMAP):
        raise RuntimeError(f"{REFMAP} is missing — build it first:  make refmap")
    cmd = [REFMAP, "-w", str(device_w), "-h", str(device_h), "-schema", schema]
    if scale > 0:
        cmd += ["-k", str(scale)]
    if optional:
        cmd += ["-optional", ",".join(optional)]
    stdin = "".join(f"{key} {r[0]} {r[1]} {r[2]} {r[3]}\n" for key, r in drags.items())
    proc = subprocess.run(cmd, input=stdin, capture_output=True, text=True)
    for line in proc.stderr.splitlines():
        print("    " + line)
    if proc.returncode != 0:
        raise RuntimeError(proc.stderr.strip() or f"refmap exited {proc.returncode}")
    return json.loads(proc.stdout)


def write_asset(path, payload):
    """Write the asset, merging into whatever is already there.

    Merging matters for the x-popup pair: picking the primary must not erase an alt
    that was picked in an earlier session (the loop reads both).
    """
    full = os.path.join(REPO_ROOT, path)
    os.makedirs(os.path.dirname(full), exist_ok=True)
    existing = {}
    if os.path.exists(full):
        try:
            with open(full) as f:
                loaded = json.load(f)
            if isinstance(loaded, dict):
                existing = loaded
        except (OSError, ValueError):
            print(f"    WARNING: {path} was unreadable; writing it fresh")
    existing.update(payload)
    with open(full, "w") as f:
        json.dump(existing, f, indent=2)
        f.write("\n")
    print(f"    wrote {path}: {', '.join(sorted(payload))}")


def check():
    cfg = read_config()
    device_id = resolve_device(cfg, None)
    scale = resolve_scale(cfg, 0.0)
    ok = True
    print(f"repo root      : {REPO_ROOT}")
    print(f"adb device     : {device_id}")
    print(f"display scale  : {scale if scale > 0 else 'derived from the frame diagonal'}")
    print(f"refmap binary  : {REFMAP} {'(present)' if os.path.exists(REFMAP) else '(MISSING — run: make refmap)'}")
    if not os.path.exists(REFMAP):
        ok = False
    for mod in ("cv2", "numpy"):
        try:
            __import__(mod)
            print(f"python module  : {mod} present")
        except ImportError:
            print(f"python module  : {mod} MISSING (needed for the drag window)")
            ok = False
    if shutil.which("adb") is None:
        print("adb            : MISSING from PATH")
        ok = False
    print("\nsteps:")
    for s in STEPS:
        tail = "  (optional)" if s["key"] == "x_popup_roi_alt" else ""
        print(f"  {s['step']}. {s['title']:<42} -> {s['out']}{tail}")
    print("\nready" if ok else "\nnot ready")
    return 0 if ok else 1


def run_session(args):
    import cv2

    cfg = read_config()
    device_id = resolve_device(cfg, args.device)
    scale = resolve_scale(cfg, args.k)
    steps = [s for s in STEPS if args.only is None or s["key"] == args.only]
    if not steps:
        print(f"error: --only {args.only} matches no step", file=sys.stderr)
        return 2

    state = {"img": None, "rect": None, "drawing": False}

    def on_mouse(event, x, y, flags, param):
        if event == cv2.EVENT_LBUTTONDOWN:
            state["rect"] = [x, y, x, y]
            state["drawing"] = True
        elif event == cv2.EVENT_MOUSEMOVE and state["drawing"]:
            state["rect"][2], state["rect"][3] = x, y
        elif event == cv2.EVENT_LBUTTONUP:
            state["rect"][2], state["rect"][3] = x, y
            state["drawing"] = False

    window = "wall-flow picking session"
    cv2.namedWindow(window, cv2.WINDOW_NORMAL)
    cv2.setMouseCallback(window, on_mouse)

    picked = {}  # (schema, out) -> {key: rect}
    try:
        for s in steps:
            while True:  # re-capture loop for `s`
                state["img"] = capture_screen(device_id)
                h, w = state["img"].shape[:2]
                cv2.resizeWindow(window, min(w, 1280), min(h, 900))
                print(f"\nstep {s['step']}: {s['title']}")
                print(f"  {s['hint']}")
                again = True
                while again:
                    canvas = state["img"].copy()
                    r = state["rect"]
                    if r is not None:
                        cv2.rectangle(canvas, (r[0], r[1]), (r[2], r[3]), (0, 255, 255), 2)
                        label = f"{min(r[0], r[2])},{min(r[1], r[3])} {abs(r[2]-r[0])}x{abs(r[3]-r[1])}"
                        cv2.putText(canvas, label, (min(r[0], r[2]), max(0, min(r[1], r[3]) - 8)),
                                    cv2.FONT_HERSHEY_SIMPLEX, 0.6, (0, 255, 255), 2)
                    cv2.putText(canvas, f"{s['step']}/7  {s['title']}", (16, 32),
                                cv2.FONT_HERSHEY_SIMPLEX, 0.8, (0, 255, 0), 2)
                    cv2.putText(canvas, "Enter=accept  r=redraw  s=recapture  q=quit",
                                (16, 60), cv2.FONT_HERSHEY_SIMPLEX, 0.6, (0, 255, 0), 2)
                    cv2.imshow(window, canvas)
                    key = cv2.waitKey(30) & 0xFF
                    if key in (27, ord("q"), ord("Q")):
                        print("\ncancelled — nothing more written")
                        return 1
                    if key in (ord("s"), ord("S")):
                        again = False  # outer loop re-captures
                        continue
                    if key in (ord("r"), ord("R")):
                        state["rect"] = None
                        continue
                    if key in (13, 10, 32):
                        r = state["rect"]
                        if r is None or (r[0] == r[2] and r[1] == r[3]):
                            print("    no box drawn yet — drag one first")
                            continue
                        box = (min(r[0], r[2]), min(r[1], r[3]), max(r[0], r[2]), max(r[1], r[3]))
                        if s["key"] == "x_popup_roi_alt" and (box[2] - box[0]) < 4:
                            print("    box too small to be an X — drag again, or press q to stop")
                            continue
                        ret = ""
                        while ret != "ok":
                            try:
                                payload = refmap_convert(w, h, scale, s["schema"], {s["key"]: box},
                                                         optional=("x_popup_roi_alt",)
                                                         if s["schema"] == "x_roi" else ())
                                picked.setdefault((s["schema"], s["out"]), {}).update(payload)
                                print("    accepted")
                                ret = "ok"
                                again = False
                            except RuntimeError as e:
                                print(f"    refmap refused this box:\n      {e}")
                                print("    drag a new box (r), recapture (s), or quit (q)")
                                break
                        continue
                break
    finally:
        cv2.destroyAllWindows()

    for (_schema, out), payload in picked.items():
        write_asset(out, payload)
    print("\ndone — re-run the bot's wall flow with: go run ./cmd/test_wall_upgrade -mode=run -yes")
    return 0


def main():
    args = parse_args()
    if args.check:
        return check()
    return run_session(args)


if __name__ == "__main__":
    sys.exit(main())
