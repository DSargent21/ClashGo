// cmd/capture_template is a manual template-recapture tool. It pulls a
// single screenshot from the live BlueStacks instance over adb, asks the
// user (on the terminal) for a rect of pixel coordinates describing the
// target template region, crops, writes it to assets/templates/<name>.png,
// backs up the previous version to <name>.png.bak, then runs
// MatchMultiScaleROI against the source frame so the user gets immediate
// feedback ("✓ conf=0.91 at (442,285)" or "✗ no match — try again").
//
// Typical workflow to recapture text_wall.png from a clean session:
//
//  1. In BlueStacks, navigate to MainVillage → tap builder head →
//     scroll the upgrades menu to bring the "Wall" row to a stable
//     position on screen. (This tool does NOT drive the menu — see
//     run_test_wall_upgrade.sh for that.)
//  2. Run `run_capture_template.sh text_wall`. The tool snaps a preview
//     to output/template_captures/<ts>/captured_screen.png — open that
//     PNG in Preview / your image viewer.
//  3. Inspect the PNG and enter the bounding rect of the "Wall" label
//     in physical (screen) coords when prompted (e.g. "350 270 450 300").
//     Coords can be either corner-format (4 numbers) or center-format
//     (prefix the line with `c`, e.g. `c 400 285 100 30`).
//  4. The tool writes assets/templates/text_wall.png, archives the
//     previous one to .bak, prints the verification confidence.
//
// Coordinate space: always the PHYSICAL pixel size of the captured
// frame (e.g. 860x732 for 860x732 devices). The tool warns otherwise
// and never rescales — keeping the trace from capture to crop
// single-step avoids off-by-one errors at non-ref resolutions.
//
// SAFETY: the template is written to `<name>.png.tmp` first, then a
// verify pass runs, and only on success does the tmp file rename
// over the live file. This means if conf is below `--min-conf` or no
// matches are found, the previous live template is preserved
// (unchanged on disk) and the broken capture lands in
// `<name>.png.failed-<ts>` for inspection. The .bak backup is still
// written so the user has belt-and-braces recovery options.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"image"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gocv.io/x/gocv"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
)

func main() {
	var (
		name     string
		device   string
		minConf  float64
		outDir   string
		noBackup bool
		verbose  bool
		drag     bool
		timeout  time.Duration
		source   string
	)
	flag.StringVar(&name, "name", "", "template name (e.g. text_wall, btn_upgrade_wall, btn_confirm_upgrade) — REQUIRED")
	flag.StringVar(&device, "device", "", "override device id (else uses config)")
	flag.Float64Var(&minConf, "min-conf", 0.85, "minimum MatchMultiScaleROI confidence for the verify pass to count as success")
	flag.StringVar(&outDir, "out", "", "preview dir (default ./output/template_captures/<ts>)")
	flag.BoolVar(&noBackup, "no-backup", false, "skip writing <name>.png.bak before overwriting")
	flag.StringVar(&source, "source", "", "load this PNG instead of capturing live from ADB (offline crop — e.g. a previously saved captured_screen.png)")
	flag.BoolVar(&verbose, "verbose", false, "print extra matching details")
	flag.BoolVar(&drag, "drag", false, "use a browser-based drag-crop UI instead of a terminal prompt (requires a modern browser)")
	flag.DurationVar(&timeout, "drag-timeout", 5*time.Minute, "drag-mode timeout — how long to wait for the user to save coords before giving up")
	flag.Usage = usage
	flag.Parse()

	if name == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --name is required (e.g. text_wall)")
		usage()
		os.Exit(2)
	}

	// Path-traversal guard: --name is interpolated into a path; reject
	// any separators so a stray `-name=../foo` can't escape assets/templates.
	if strings.ContainsAny(name, `/\`) {
		fmt.Fprintf(os.Stderr, "ERROR: --name=%q contains path separators (use a bare name like 'text_wall')\n", name)
		os.Exit(2)
	}

	logger := newLogger()
	if verbose {
		logger = logger.Level(zerolog.DebugLevel)
	}

	// 1. Resolve device + connect — SKIPPED in offline mode (-source).
	var client *adb.Client
	if source == "" {
		cfgPath := paths.ResolveConfig("config.json")
		cfg, err := config.Load(cfgPath)
		if err != nil {
			logger.Warn().Err(err).Str("path", cfgPath).Msg("config load failed; using defaults")
			cfg = config.DefaultConfig()
		}
		zl := adbLogAdapter{log: logger}
		client = adb.NewClient(
			adb.WithHost(cfg.Device.ADBHost),
			adb.WithPort(cfg.Device.ADBPort),
			adb.WithTimeout(30*time.Second),
			adb.WithLogger(&zl),
			adb.WithZoomKeys(cfg.Device.ZoomOutKey, cfg.Device.ZoomInKey),
		)
		if device != "" {
			client.DeviceID = device
		} else {
			client.DeviceID = cfg.Device.DeviceID
		}
		if err := client.AutoDetectDevice(); err != nil {
			logger.Warn().Err(err).Msg("AutoDetectDevice failed; using configured device id verbatim")
		}
		if err := client.EnsureConnected(); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: connect to %s: %v\n", client.DeviceID, err)
			os.Exit(1)
		}
		fmt.Printf("✓ adb connected: %s\n", client.DeviceID)
	}

	// 2. Set up the output dir for the preview screenshot.
	if outDir == "" {
		outDir = filepath.Join("output", "template_captures", time.Now().Format("20060102_150405")+"_"+name)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: mkdir %s: %v\n", outDir, err)
		os.Exit(1)
	}
	fmt.Printf("✓ preview dir: %s\n", outDir)

	// 3. Capture current screen (live) or load from -source (offline).
	var screen gocv.Mat
	if source != "" {
		fmt.Printf("✓ offline mode: loading %s\n", source)
		screen = gocv.IMRead(source, gocv.IMReadColor)
		if screen.Empty() {
			fmt.Fprintf(os.Stderr, "ERROR: read source PNG %s\n", source)
			os.Exit(1)
		}
	} else {
		s, err := client.CaptureToMat()
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: capture screen: %v\n", err)
			_ = client.Close()
			os.Exit(1)
		}
		screen = s
	}
	defer screen.Close()
	if source == "" {
		defer func() { _ = client.Close() }()
	}
	w, h := screen.Cols(), screen.Rows()
	fmt.Printf("✓ captured: %dx%d  (ref 860x732, scale %.2fx%.2f)\n",
		w, h, float64(w)/860.0, float64(h)/732.0)

	// Always save the preview so the user can inspect it in an external
	// viewer BEFORE entering coords. This is essential — the user's
	// mental model is "I'll look at the image, then type coords".
	previewPath := filepath.Join(outDir, "captured_screen.png")
	if ok := gocv.IMWrite(previewPath, screen); !ok {
		fmt.Fprintf(os.Stderr, "ERROR: write preview to %s\n", previewPath)
		os.Exit(1)
	}
	fmt.Printf("✓ preview saved: %s\n", previewPath)

	// 4. Prompt the user for coords. Two paths:
	//    a) default: terminal stdin via promptForRect
	//    b) -drag:   browser-based drag UI via dragForRect
	// Both return *image.Rectangle in PHYSICAL pixels of the captured frame.
	// dragForRect needs the template name so it can render the header
	// text in the browser UI; captured_main()'s local `name` is the
	// only validated source of truth, so we pass it through.
	var r *image.Rectangle
	var err error
	if drag {
		r, err = dragForRect(previewPath, name, w, h, timeout)
	} else {
		r, err = promptForRect(screen)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
	if r == nil {
		fmt.Println("aborted by user — no template written")
		return
	}

	// 5. Backup existing template (if any).
	tplDir := paths.Resolve("templates")
	dstPath := filepath.Join(tplDir, name+".png")
	backupPath := dstPath + ".bak"
	if !noBackup {
		if existing, err := os.ReadFile(dstPath); err == nil {
			if err := os.WriteFile(backupPath, existing, 0o644); err != nil {
				fmt.Fprintf(os.Stderr, "WARN: backup to %s failed: %v (continuing)\n", backupPath, err)
			} else {
				fmt.Printf("✓ backed up old template to %s (%d bytes)\n", backupPath, len(existing))
			}
		}
	}

	// 6. Crop + write to a `<name>.png.tmp` file. We will only rename
	// over the live `<name>.png` AFTER the verify pass confirms the
	// new template is a good fit. This is the safety-vs-reviewer
	// "destructive backup race" fix — if the verify fails, the live
	// template is preserved and the broken capture lands in
	// `<name>.png.failed-<ts>` for inspection.
	tmpPath := filepath.Join(tplDir, name+".tmp.png")
	cropped := screen.Region(*r)
	defer cropped.Close()
	if cropped.Empty() {
		fmt.Fprintf(os.Stderr, "ERROR: cropped region is empty (rect %v out of bounds %dx%d)\n", *r, w, h)
		os.Exit(1)
	}
	if ok := gocv.IMWrite(tmpPath, cropped); !ok {
		fmt.Fprintf(os.Stderr, "ERROR: write tmp template to %s\n", tmpPath)
		os.Exit(1)
	}
	fmt.Printf("✓ wrote cropped png to %s (%dx%d) — pending verify\n", tmpPath, cropped.Cols(), cropped.Rows())

	// 7. Verify by re-reading the just-written crop and matching it
	//    against the source frame. We match the NEW crop (not the
	//    on-disk store, which is absent on a first-time capture) so
	//    the verify actually validates the crop we're about to save.
	tpl := gocv.IMRead(tmpPath, gocv.IMReadColor)
	if tpl.Empty() {
		fmt.Fprintf(os.Stderr, "ERROR: cannot re-read cropped template from %s\n", tmpPath)
		_ = moveAside(tmpPath, failedTemplatePath(tplDir, name))
		os.Exit(1)
	}
	defer tpl.Close()
	fullROI := image.Rect(0, 0, w, h)
	matches, _ := vision.MatchMultiScaleROICached(screen, tpl, name, 0.3, 1.5, 60, 0.7, fullROI)
	if len(matches) == 0 {
		fmt.Printf("\n✗ VERIFICATION FAILED: template does not match any region of the captured frame.\n")
		fmt.Printf("  Common causes: rect captures surrounding padding, another visually-similar\n")
		fmt.Printf("  element, or a slightly off rendering of the target. Re-run and try a\n")
		fmt.Printf("  tighter rect.\n")
		preserve := failedTemplatePath(tplDir, name)
		_ = moveAside(tmpPath, preserve)
		fmt.Printf("  Captured-but-broken crop is preserved at %s for inspection.\n", preserve)
		fmt.Printf("  Live template %s is UNCHANGED.\n", dstPath)
		os.Exit(1)
	}
	best := matches[0]
	fmt.Printf("\n=== VERIFY ===\n")
	fmt.Printf("best match: conf=%.2f  at (%d,%d)  scale=%.2f\n",
		best.Confidence, best.Point.X, best.Point.Y, best.Scale)
	if best.Confidence < minConf {
		fmt.Printf("✗ conf=%.2f is BELOW the %s threshold (%.2f). Tmp file preserved at %s for inspection.\n",
			best.Confidence, name, minConf, tmpPath)
		preserve := failedTemplatePath(tplDir, name)
		_ = moveAside(tmpPath, preserve)
		fmt.Printf("  Captured-but-below-threshold crop is preserved at %s.\n", preserve)
		fmt.Printf("  Live template %s is UNCHANGED.\n", dstPath)
		os.Exit(1)
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: rename %s -> %s: %v (the tmp file is still there)\n", tmpPath, dstPath, err)
		os.Exit(1)
	}
	fmt.Printf("✓ conf=%.2f clears the %.2f threshold — template written to %s\n",
		best.Confidence, minConf, dstPath)
	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  ./run_test_wall_upgrade.sh dry-run    # verify the new template matches on a fresh capture")
	fmt.Println("  ./run_test_wall_upgrade.sh run -yes  # full live loop")
}

// moveAside renames src → dst inside the same parent directory. Used
// to preserve broken captures rather than deleting them. Best-effort
// (caller ignores the error in non-fatal paths; passthrough-fatal
// callers log and continue).
func moveAside(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// failedTemplatePath builds a unique full preservation path under
// tplDir for a captured-but-failed template crop. The path is always
// fully-qualified so os.Rename lands in assets/templates/ rather than
// the cwd (the original bug was passing a bare name to os.Rename
// which silently interpreted it relative to the process working
// directory). UnixNano suffix guarantees uniqueness so two fails in
// the same wall-clock second never overwrite each other's preserved
// crop.
//
//	failedTemplatePath("assets/templates", "text_wall")
//	-> assets/templates/text_wall.png.failed-1752695123456789012
func failedTemplatePath(tplDir, name string) string {
	return filepath.Join(tplDir,
		name+".png.failed-"+strconv.FormatInt(time.Now().UnixNano(), 10))
}

// promptForRect reads a rect from stdin in either {corners} or {center+size}
// format, validates it lies inside the screen, and returns it. Allows
// 'q' to abort (returns (nil, nil)).
//
// Accepted shapes:
//   - 4 numbers: corners format x1 y1 x2 y2
//   - 5 numbers starting with "c": center format  c cx cy w h
func promptForRect(screen gocv.Mat) (*image.Rectangle, error) {
	w, h := screen.Cols(), screen.Rows()
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("────────────────────────────────────────────────────────")
	fmt.Printf("Coor space: PHYSICAL pixels of the captured frame (%dx%d).\n", w, h)
	fmt.Println("Open the captured preview to eyeball the target region:")
	fmt.Println("  open output/template_captures/<ts>_<name>/captured_screen.png")
	fmt.Println()
	fmt.Println("Enter the rect that bounds the target (e.g. the 'Wall' label):")
	fmt.Println("  corners format: x1 y1 x2 y2         (e.g. 350 270 450 300)")
	fmt.Println("  center format:  c cx cy w h         (e.g. c 400 285 100 30)")
	fmt.Println("  q to abort without writing")
	fmt.Println("────────────────────────────────────────────────────────")
	fmt.Printf("> ")

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "q" || line == "quit" {
			return nil, nil
		}
		parts := strings.Fields(line)
		if len(parts) != 4 && len(parts) != 5 {
			fmt.Printf("need 4 numbers (corners) or 5 numbers prefixed by 'c' (center+wh); got %d. try again.\n> ", len(parts))
			continue
		}

		mode := "corners"
		if len(parts) == 5 {
			if parts[0] != "c" && parts[0] != "C" {
				fmt.Printf("5-value input must start with 'c' for center+wh; got %q. try again.\n> ", parts[0])
				continue
			}
			mode = "center"
			parts = parts[1:]
		}

		nums := make([]int, 4)
		parsed := true
		for i, p := range parts {
			n, perr := strconv.Atoi(p)
			if perr != nil {
				fmt.Printf("non-integer %q: %v. try again.\n> ", p, perr)
				parsed = false
				break
			}
			nums[i] = n
		}
		if !parsed {
			continue
		}

		var r image.Rectangle
		switch mode {
		case "corners":
			r = image.Rect(nums[0], nums[1], nums[2], nums[3])
		case "center":
			cx, cy, w2, h2 := nums[0], nums[1], nums[2], nums[3]
			r = image.Rect(cx-w2/2, cy-h2/2, cx+w2/2, cy+h2/2)
		}

		// Canonicalize so Min/Max are correct regardless of input order.
		r = r.Canon()
		// Validate bounds.
		if r.Min.X < 0 {
			r.Min.X = 0
		}
		if r.Min.Y < 0 {
			r.Min.Y = 0
		}
		if r.Max.X > w {
			r.Max.X = w
		}
		if r.Max.Y > h {
			r.Max.Y = h
		}
		if r.Dx() < 4 || r.Dy() < 4 {
			fmt.Printf("rect too small (%dx%d); need at least 4x4. try again.\n> ", r.Dx(), r.Dy())
			continue
		}
		fmt.Printf("OK: cropped rect %v  (%dx%d). Proceeding...\n", r, r.Dx(), r.Dy())
		return &r, nil
	}
}

// usage prints the -h output.
func usage() {
	fmt.Fprintf(os.Stderr, `Usage: capture_template -name=NAME [flags]

Required:
  -name=NAME           template name (e.g. text_wall, btn_upgrade_wall,
                       btn_confirm_upgrade, btn_attack, btn_battle).
                       Must NOT contain path separators.

Flags:
  -device=ID          override device id (else uses config.json)
  -source=PATH        load this PNG instead of capturing live from ADB
                       (offline crop of a previously saved frame)
  -min-conf=F         minimum MatchMultiScaleROI confidence to count as
                       success on the verify pass (default 0.85)
  -out=DIR            preview screenshot directory
                       (default ./output/template_captures/<ts>_<name>)
  -no-backup          skip writing <name>.png.bak before overwriting
  -verbose            extra gocv matching detail
  -drag               use a browser-based drag-crop UI instead of a
                       terminal prompt (recommended for precision crops)
  -drag-timeout=DUR   drag-mode timeout (default 5m). After this the
                       tool gives up and exits non-zero.

UX modes:
  default     terminal prompt (corners x1 y1 x2 y2 or center 'c cx cy w h')
  -drag       opens a browser window showing the captured preview; drag a
              rect over the target, click "Save coords". The tool reads
              the rect, applies the same crop+verify+rename flow as the
              terminal mode. Faster and more precise for textured targets.

SAFETY: writes to <name>.png.tmp first; only renames to <name>.png
after the verify pass clears --min-conf. If the new template fails
verify, the live <name>.png is UNCHANGED and the broken capture is
preserved as <name>.png.failed-<UnixNano> for inspection.
`)
}

// dragHTML is the single-page UI served at '/' by dragForRect. The browser
// loads /preview.png as the source image and POSTs the chosen rect to
// /coords when the user clicks "Save coords ✓".
//
// Coords: the image is served at its natural pixel size; CSS-pixel mouse
// positions are scaled by (naturalWidth / clientWidth, naturalHeight /
// clientHeight) before sending so the resulting "x1 y1 x2 y2" are in the
// physical pixels of the captured frame. The user can also just read the
// "corners format" text from the corner info box and paste it into any
// other tool.
const dragHTML = `<!doctype html><html><head>
<meta charset="utf-8"><title>capture_template — drag crop</title>
<style>
body{margin:0;font-family:-apple-system,system-ui,sans-serif;color:#222;background:#111}
header{padding:10px 14px;background:#222;color:#fff;font-size:14px}
header b{color:#0ff;font-family:Menlo,monospace}
.toolbar{padding:8px 12px;background:#f0f0f0;border-bottom:1px solid #ccc;display:flex;gap:10px;align-items:center}
button{padding:6px 14px;border:1px solid #888;background:#fff;border-radius:4px;cursor:pointer;font-size:13px}
button:disabled{opacity:.4;cursor:not-allowed}
button:hover:not(:disabled){background:#eef}
#frame{display:inline-block;position:relative;margin:12px;box-shadow:0 4px 18px rgba(0,0,0,.5);background:#000}
#preview{display:block;user-select:none;-webkit-user-drag:none;cursor:crosshair}
#rect{position:absolute;border:2px solid #0f0;background:rgba(0,255,0,.18);pointer-events:none;display:none}
#info{position:fixed;bottom:12px;right:12px;background:rgba(34,34,34,.95);color:#fff;padding:10px 14px;font-family:Menlo,monospace;font-size:13px;line-height:1.5;border-radius:6px;z-index:10;min-width:280px}
#info .coord{color:#0f0;font-weight:bold;font-size:15px;display:block;margin-top:4px}
#info .ctx{color:#aaa;font-size:11px}
#saved{font-weight:bold}
</style></head>
<body>
<header>Drag a rect <b>around the TARGET_TEMPLATE_NAME label</b> on the preview below, then click Save coords ✓. ESC to reset.</header>
<div class="toolbar">
  <button id="reset" disabled>Reset</button>
  <button id="save" disabled>Save coords ✓</button>
  <span id="saved"></span>
</div>
<div id="frame">
  <img id="preview" src="/preview.png" draggable="false">
  <div id="rect"></div>
</div>
<div id="info" class="ctx">loading…</div>
<script>
const preview=document.getElementById('preview'),rect=document.getElementById('rect'),
      info=document.getElementById('info'),reset=document.getElementById('reset'),
      save=document.getElementById('save'),saved=document.getElementById('saved'),
      frame=document.getElementById('frame');
let startX=0,startY=0,dragging=false,finalRect=null;
function updateInfo(initial){
  const ns={w:preview.naturalWidth,h:preview.naturalHeight};
  info.innerHTML='<span class="ctx">image: '+ns.w+' x '+ns.h+' physical px</span>'+(initial?'<br><br><span class="ctx">drag a rect on the preview above</span>':'');
}
preview.addEventListener('load',()=>updateInfo(true));
preview.addEventListener('mousedown',e=>{
  const r=frame.getBoundingClientRect();
  startX=e.clientX-r.left;startY=e.clientY-r.top;
  dragging=true;
  rect.style.display='block';
  rect.style.left=startX+'px';rect.style.top=startY+'px';
  rect.style.width='0';rect.style.height='0';
  reset.disabled=false;
});
window.addEventListener('mousemove',e=>{
  if(!dragging)return;
  const r=frame.getBoundingClientRect();
  const x=e.clientX-r.left,y=e.clientY-r.top;
  const minX=Math.min(startX,x),maxX=Math.max(startX,x);
  const minY=Math.min(startY,y),maxY=Math.max(startY,y);
  rect.style.left=minX+'px';rect.style.top=minY+'px';
  rect.style.width=(maxX-minX)+'px';rect.style.height=(maxY-minY)+'px';
  const ns={w:preview.naturalWidth,h:preview.naturalHeight};
  const sx=ns.w/preview.clientWidth,sy=ns.h/preview.clientHeight;
  const px1=Math.round(minX*sx),py1=Math.round(minY*sy);
  const px2=Math.round(maxX*sx),py2=Math.round(maxY*sy);
  info.innerHTML='<span class="ctx">corners format (paste into terminal):</span><span class="coord">'
    +px1+' '+py1+' '+px2+' '+py2+'</span><span class="ctx">size: '+(px2-px1)+' x '+(py2-py1)+'</span>';
  finalRect={x1:px1,y1:py1,x2:px2,y2:py2};
  save.disabled=false;
});
window.addEventListener('mouseup',()=>{dragging=false;});
reset.addEventListener('click',()=>{
  rect.style.display='none';rect.style.width='0';rect.style.height='0';
  save.disabled=true;reset.disabled=true;finalRect=null;
  updateInfo(true);
});
save.addEventListener('click',async()=>{
  save.disabled=true;reset.disabled=true;saved.textContent='saving…';
  try{
    const r=await fetch('/coords',{method:'POST',body:JSON.stringify(finalRect),headers:{'Content-Type':'application/json'}});
    if(r.ok){
      saved.innerHTML='<span style="color:#0a0">✓ saved!</span> tool verifying… you can close this tab.';
    }else{
      saved.textContent='✗ '+r.statusText;
      save.disabled=false;
    }
  }catch(e){
    saved.textContent='✗ '+e.message;save.disabled=false;
  }
});
window.addEventListener('keydown',e=>{if(e.key==='Escape'&&!reset.disabled){reset.click();}});
updateInfo(true);
</script></body></html>`

// dragForRect starts a local HTTP server on 127.0.0.1:0 (random port),
// serves the dragHTML UI + the captured preview, and waits for the user
// to drag a rect and POST it back. Returns the rect in physical pixels
// of the captured frame, valid to pass to gocv.Mat.Region.
//
// Behavior notes:
//   - Server binds to loopback ONLY — no external network exposure.
//   - Returns after first valid /coords POST, then srv.Shutdown()s.
//   - default 5min timeout (configurable via -drag-timeout) prevents
//     the tool from hanging indefinitely when a user closes the browser
//     tab without saving. Executed via context.WithTimeout so the
//     underlying timer is freed when the rect arrives early — the
//     earlier time.After-based implementation left the timer primed
//     until expiry, wasting goroutine cycles for up to 5min.
//   - `templateName` is html-escaped before substitution into the
//     dragHTML header so a future user passing a name with '<', '&',
//     or '>' can't inject HTML. The earlier "TARGET_TEMPLATE_NAME"
//     placeholder mechanism is unchanged.
func dragForRect(previewPath, templateName string, screenW, screenH int, timeout time.Duration) (*image.Rectangle, error) {
	mux := http.NewServeMux()
	tplName := html.EscapeString(templateName)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, strings.Replace(dragHTML, "TARGET_TEMPLATE_NAME", tplName, 1))
	})
	mux.HandleFunc("/preview.png", func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(previewPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.Copy(w, f)
	})

	coordsCh := make(chan image.Rectangle, 1)
	errCh := make(chan error, 1)
	mux.HandleFunc("/coords", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			X1, Y1, X2, Y2 int
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			errCh <- fmt.Errorf("decode coords: %w", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		coordsCh <- image.Rect(body.X1, body.Y1, body.X2, body.Y2).Canon()
		_, _ = w.Write([]byte("ok"))
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(listener) }()
	// Shut the server down on any exit path so future invocations don't
	// see TIME_WAIT-stuck ports and resources hold for the OS GC.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	fmt.Println()
	fmt.Println("────────────────────────────────────────────────────────")
	fmt.Printf("Drag mode — opening %s …\n", url)
	if err := exec.Command("open", url).Run(); err != nil {
		fmt.Printf("  (auto-open failed: %v)\n", err)
		fmt.Printf("  Paste this URL into your browser manually if no window opened:\n  %s\n", url)
	}
	fmt.Println("Drag a rect around the target on the preview, then click 'Save coords ✓'.")
	fmt.Println("ESC resets the rect. Timeout: ", timeout)
	fmt.Println("────────────────────────────────────────────────────────")

	// Use context.WithTimeout instead of time.After so the timer is
	// released the moment we get a successful /coords POST, rather
	// than holding it armed for the full timeout duration.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	select {
	case coords := <-coordsCh:
		if coords.Dx() < 4 || coords.Dy() < 4 {
			return nil, fmt.Errorf("rect too small (%dx%d); need >= 4x4 minimum", coords.Dx(), coords.Dy())
		}
		if coords.Min.X < 0 {
			coords.Min.X = 0
		}
		if coords.Min.Y < 0 {
			coords.Min.Y = 0
		}
		if coords.Max.X > screenW {
			coords.Max.X = screenW
		}
		if coords.Max.Y > screenH {
			coords.Max.Y = screenH
		}
		return &coords, nil
	case err := <-errCh:
		return nil, fmt.Errorf("coords POST failed: %w", err)
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("drag mode timed out after %s — close the browser tab and re-run", timeout)
		}
		return nil, fmt.Errorf("drag mode interrupted: %w", ctx.Err())
	}
}

// adbLogAdapter adapts zerolog to adb.Logger.
type adbLogAdapter struct {
	log zerolog.Logger
}

func (a *adbLogAdapter) Debug() bool { return a.log.GetLevel() <= zerolog.DebugLevel }
func (a *adbLogAdapter) Debugf(format string, v ...any) {
	a.log.Debug().Msgf(format, v...)
}
func (a *adbLogAdapter) Info(msg string)  { a.log.Info().Msg(msg) }
func (a *adbLogAdapter) Warn(msg string)  { a.log.Warn().Msg(msg) }
func (a *adbLogAdapter) Error(msg string) { a.log.Error().Msg(msg) }
func (a *adbLogAdapter) WithFields(fields map[string]any) adb.Logger {
	return &adbLogAdapter{log: a.log.With().Fields(fields).Logger()}
}

// newLogger returns a zerolog logger writing to stderr at Info level.
func newLogger() zerolog.Logger {
	zerolog.TimeFieldFormat = time.RFC3339Nano
	return zerolog.New(os.Stderr).With().Timestamp().Logger()
}
