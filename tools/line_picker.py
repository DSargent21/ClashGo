#!/usr/bin/env python3
"""Build a click-to-draw page for a live ClashGO battle screenshot.

This is the drawing tool for the per-side deploy geometry that a strategy's
formula.json pins. It exists next to `cmd/design_attack` (the native OpenCV
picker) because the two suit different moments:

  * `cmd/design_attack` walks every unit in the strategy and wants a desktop
    window with mouse handlers.
  * this one answers "here is the battle screen, draw the four Valkyrie lines
    and click where the Earthquake goes" — a page you click in, with the
    screenshot already in it, no window and no click-through-to-the-game risk.

The output is a SELF-CONTAINED html file: the screenshot is inlined as a data
URI because ClashGO's preview server serves only the single registered file, so
a neighbouring .png would 404.

    python3 tools/line_picker.py                     # capture live, build page
    python3 tools/line_picker.py --shot battle.png   # use a saved screenshot
    python3 tools/line_picker.py --device localhost:5555

Writes tmp/line_picker_live.html and prints its absolute path. The page's
`window.__picker.json()` returns the formula JSON, and the Download button
saves the same document to disk. The frame is inlined as a JPEG at the device's
own pixel dimensions, so the coordinates the page reports are device pixels.

The units other than the ring and the spell (heroes, siege) are carried over
from the existing formula file, so the downloaded document is a complete,
valid replacement rather than a two-key fragment.
"""

from __future__ import annotations

import argparse
import base64
import json
import pathlib
import subprocess
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

ROOT = pathlib.Path(__file__).resolve().parents[1]
TEMPLATE = ROOT / "tools" / "line_picker.html"
OTHER_UNITS_KEY = "__OTHER_UNITS_JSON__"
FRAME_URL = "/frame.png"
SAVE_URL = "/save"


def capture_live(device: str, timeout: int = 25) -> bytes:
    """Grab one frame via `adb exec-out screencap -p`.

    exec-out is used in preference to `adb shell screencap` because the guest
    adbd on BlueStacks/macOS wedges (bare `adb shell <cmd>` answers "error:
    closed" for most commands) while binary exec-out still streams. See
    internal/adb/transport.go's shellWedgeFallback for the same workaround.
    """
    args = ["adb"]
    if device:
        args += ["-s", device]
    args += ["exec-out", "screencap", "-p"]
    try:
        out = subprocess.run(args, capture_output=True, timeout=timeout)
    except FileNotFoundError:
        sys.exit("adb not found on PATH — install the platform-tools, or pass --shot <png>")
    except subprocess.TimeoutExpired:
        sys.exit("adb screencap timed out — is the instance running? (`adb devices`)")
    if out.returncode != 0:
        sys.exit("adb screencap failed: " + out.stderr.decode("utf-8", "replace").strip())
    png = out.stdout
    if not png.startswith(b"\x89PNG"):
        sys.exit("adb returned something that is not a PNG — device offline, or a wedged adbd "
                 "(try `adb kill-server && adb start-server`)")
    return png


def png_size(png: bytes) -> tuple[int, int]:
    """IHDR width/height are fixed offsets in every PNG."""
    if len(png) < 24:
        sys.exit("screenshot is truncated")
    return int.from_bytes(png[16:20], "big"), int.from_bytes(png[20:24], "big")


def shrink_for_page(png: bytes, w: int, h: int) -> tuple[str, bytes]:
    """Return (mime, encoded) for the inlined frame.

    The page is one file with the screenshot inside it, so the frame's byte size
    is the page size: a 1280x720 PNG from the device is ~2 MB, which becomes a
    ~2.8 MB base64 attribute and a sluggish load. A JPEG at the SAME pixel
    dimensions is ~10x smaller and draws on the same coordinate grid, so the
    coordinates the user clicks are still device pixels and nothing needs
    scaling. Falls back to the raw PNG when Pillow is unavailable — never
    resample: a downscaled frame would silently shift every drawn coordinate.
    """
    try:
        import io

        from PIL import Image
    except ImportError:
        return "image/png", png
    try:
        img = Image.open(io.BytesIO(png)).convert("RGB")
    except Exception as e:  # noqa: BLE001 — any decode failure means "use the PNG"
        print(f"could not decode the frame for JPEG conversion ({e}); inlining the PNG", file=sys.stderr)
        return "image/png", png
    if img.size != (w, h):
        print(f"decoder reports {img.size[0]}x{img.size[1]} against a {w}x{h} header; inlining the PNG",
              file=sys.stderr)
        return "image/png", png
    buf = io.BytesIO()
    img.save(buf, format="JPEG", quality=85, optimize=True)
    return "image/jpeg", buf.getvalue()


def render_page(args, template: str, img_src: str, other: dict, seed, server_mode: bool) -> str:
    """Fill the template. `img_src` is either a data URI (file mode) or a URL the
    page can re-fetch (server mode)."""
    return (template
            .replace("__SCREENSHOT_DATA_URI__", img_src)
            .replace(OTHER_UNITS_KEY, json.dumps(other, indent=2))
            .replace("__SEED_JSON__", json.dumps(seed, indent=2) if seed else "null")
            .replace("__SERVER_MODE__", "true" if server_mode else "false")
            .replace("__RING_UNIT__", args.ring_unit)
            .replace("__SPELL_UNIT__", args.spell_unit)
            .replace("__FORMULA_NAME__",
                     f"authored with tools/line_picker.html ({args._frame_size[0]}x{args._frame_size[1]})"))


def serve(args, template: str, other: dict, seed) -> int:
    """Serve the picker on loopback, re-capturing the frame on demand.

    The one-shot page is a snapshot: the game moves and the frame is stale, and
    the only way to refresh was to re-run this script. In serve mode the page
    asks for /frame.png and gets a fresh capture, so Re-capture pulls the
    emulator's screen right now while the drawing stays put.

    Bound to 127.0.0.1 only: the page writes into the repo (/save) and talks to
    the device through adb, neither of which belongs on a network interface.
    """
    formula_path = pathlib.Path(args.formula)

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_a):  # keep the terminal readable
            pass

        def _send(self, code: int, ctype: str, body: bytes):
            self.send_response(code)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):  # noqa: N802 — BaseHTTPRequestHandler's API
            path = urlsplit(self.path).path
            if path in ("/", "/index.html"):
                page = render_page(args, template, FRAME_URL, other, seed, server_mode=True)
                self._send(200, "text/html; charset=utf-8", page.encode())
                return
            if path == FRAME_URL:
                try:
                    png = capture_live(args.device)
                except SystemExit as e:
                    self._send(503, "text/plain; charset=utf-8", str(e).encode())
                    return
                mime, frame = shrink_for_page(png, *png_size(png))
                args._frame_size = png_size(png)
                print(f"re-captured {args._frame_size[0]}x{args._frame_size[1]} ({len(frame) / 1e3:.0f} KB)",
                      file=sys.stderr)
                self._send(200, mime, frame)
                return
            self._send(404, "text/plain; charset=utf-8", b"not found")

        def do_POST(self):  # noqa: N802
            if urlsplit(self.path).path != SAVE_URL:
                self._send(404, "text/plain; charset=utf-8", b"not found")
                return
            body = self.rfile.read(int(self.headers.get("Content-Length") or 0))
            try:
                doc = json.loads(body)
            except json.JSONDecodeError as e:
                self._send(400, "text/plain; charset=utf-8", f"not JSON: {e}".encode())
                return
            units = doc.get("units")
            if not isinstance(units, dict) or not units:
                self._send(400, "text/plain; charset=utf-8", b"refusing to save a document with no units")
                return
            formula_path.write_text(json.dumps(doc, indent=2) + "\n")
            msg = f"saved {len(units)} units to {formula_path.name}"
            print(msg, file=sys.stderr)
            self._send(200, "text/plain; charset=utf-8", msg.encode())

    srv = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"serving http://127.0.0.1:{args.port}/  (Ctrl-C to stop)", file=sys.stderr)
    print(f"  frame:  GET {FRAME_URL} re-captures from {args.device or 'the connected device'}", file=sys.stderr)
    print(f"  save:   POST {SAVE_URL} writes {formula_path}", file=sys.stderr)
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        srv.server_close()
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--shot", help="use a saved PNG instead of a live capture")
    ap.add_argument("--device", default="", help="adb serial (empty = the only connected device)")
    ap.add_argument("--formula", default=str(ROOT / "assets/strategies/valk_spam_formula.json"),
                    help="existing formula whose other units are carried over "
                         "(and, in --serve mode, where Save to repo writes)")
    ap.add_argument("--ring-unit", default="valkyrie",
                    help="formula key the drawn lines are saved under")
    ap.add_argument("--spell-unit", default="earthquake spell",
                    help="formula key the clicked point is saved under")
    ap.add_argument("--out", default=str(ROOT / "tmp/line_picker_live.html"),
                    help="where to write the self-contained page")
    ap.add_argument("--seed", help="JSON document to preload the drawing from "
                                   "(default: the existing formula, so a fresh page keeps the drawing)")
    ap.add_argument("--blank", action="store_true",
                    help="start with an empty drawing instead of seeding from --formula")
    ap.add_argument("--serve", action="store_true",
                    help="serve the page on loopback instead of writing one file, so "
                         "Re-capture / Save to repo work against the live emulator")
    ap.add_argument("--port", type=int, default=8765,
                    help="port for --serve (default 8765; the bot's own web UI uses 8080)")
    args = ap.parse_args()
    # The frame size the page reports in its title; --serve re-captures, so it is
    # updated on every frame request rather than fixed at build time.
    args._frame_size = (0, 0)

    if args.shot:
        shot_path = pathlib.Path(args.shot)
        if not shot_path.is_file():
            sys.exit(f"no such screenshot: {shot_path}")
        png = shot_path.read_bytes()
    else:
        print(f"capturing {args.device or 'the connected device'} ...", file=sys.stderr)
        png = capture_live(args.device)

    w, h = png_size(png)
    mime, frame = shrink_for_page(png, w, h)
    print(f"screenshot {w}x{h}, {len(png) / 1e6:.1f} MB → inlined as {mime} {len(frame) / 1e3:.0f} KB",
          file=sys.stderr)

    other: dict = {}
    existing: dict | None = None
    formula_path = pathlib.Path(args.formula)
    if formula_path.is_file():
        try:
            existing = json.loads(formula_path.read_text())
        except json.JSONDecodeError as e:
            sys.exit(f"{formula_path} is not valid JSON: {e}")
        other = {
            k: v for k, v in existing.get("units", {}).items()
            if k not in (args.ring_unit, args.spell_unit)
        }
        # A ring/spell authored for a different frame would be projected onto
        # this one, which moves every tap. Say so rather than letting it drift.
        screen = existing.get("screen") or {}
        if screen and (screen.get("w"), screen.get("h")) != (w, h):
            print(f"WARNING: {formula_path.name} declares {screen.get('w')}x{screen.get('h')} "
                  f"but this frame is {w}x{h}; the bot projects between them", file=sys.stderr)
        print(f"carrying over {len(other)} other unit(s) from {formula_path.name}", file=sys.stderr)
    else:
        print(f"no existing formula at {formula_path} — only the ring and the spell will be written",
              file=sys.stderr)

    # Seed the page with the drawing that is already on disk, so re-running this
    # against a fresh screenshot does not throw away the sides the user drew.
    seed = None
    if args.seed:
        seed_path = pathlib.Path(args.seed)
        if not seed_path.is_file():
            sys.exit(f"no such seed: {seed_path}")
        try:
            seed = json.loads(seed_path.read_text())
        except json.JSONDecodeError as e:
            sys.exit(f"{seed_path} is not valid JSON: {e}")
    elif not args.blank:
        seed = existing
    if seed:
        print("seeding the page with "
              f"{len(((seed.get('units') or {}).get(args.ring_unit) or {}).get('lines') or [])} existing side(s)",
              file=sys.stderr)

    template = TEMPLATE.read_text()
    for needed in (OTHER_UNITS_KEY, "__SCREENSHOT_DATA_URI__", "__SEED_JSON__", "__SERVER_MODE__"):
        if needed not in template:
            sys.exit(f"{TEMPLATE} is missing the {needed} placeholder; re-check the file")

    args._frame_size = (w, h)
    if args.serve:
        return serve(args, template, other, seed)

    # json.dumps of a plain dict cannot contain "</script>", so embedding it in a
    # script block is safe without escaping.
    page = render_page(args, template,
                       f"data:{mime};base64," + base64.b64encode(frame).decode("ascii"),
                       other, seed, server_mode=False)

    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(page)
    print(f"{out.resolve()}  ({len(page) / 1e6:.1f} MB)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
