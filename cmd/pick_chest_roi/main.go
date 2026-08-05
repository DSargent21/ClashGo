// Command pick_chest_roi is a one-off GUI tool for authoring the
// assets/chest_dismiss_roi.json geometry. The browser-drag UI is
// adapted from cmd/capture_template's dragForRect. Loopback-only HTTP
// server on a random port — no external exposure.
//
// Two-step workflow for the Skip-and-Confirm fast path:
//
//  1. drag the chest-screen rects (tap_roi, optional tap_roi_alt,
//     skip_button):
//
//     go run cmd/pick_chest_roi/main.go -also-buttons
//
//  2. manually tap Skip on the chest screen in-game, then drag
//     confirm_yes_button off the dialog:
//
//     go run cmd/pick_chest_roi/main.go -confirm-only
//
// Why the split: the Confirm Yes dialog isn't visible during the
// initial chest-screen capture (you'd only be guessing where it'll
// land), so capturing it in a separate run off the dialog screenshot
// is the only accurate way. The two steps land in the SAME JSON so
// the runtime engine sees both buttons together.
//
// Flags:
//
//	(no flag)              drag tap_roi only
//	-also-alt              drag tap_roi + tap_roi_alt
//	-also-buttons          drag tap_roi + skip_button (no confirm)
//	-also-alt -also-buttons drag all three of the above
//	-confirm-only          drag ONLY confirm_yes_button (no other flow)
//
// On success the tool prints each chosen rect (phys → ref) + 5 RGB
// hex samples inside it so you can paste them straight into the
// StateChestReward classifier rule in internal/game/classifier.go.
//
// Coords are saved in REFERENCE space (game.RefWidth × RefHeight)
// regardless of capture resolution — the runtime ScaleRef completes
// the conversion at tap-time.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"image"
	"image/png"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"gocv.io/x/gocv"
)

const dragHTML = `<!doctype html><html><head>
<meta charset="utf-8"><title>pick_chest_roi — drag crop</title>
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
<header>Drag a rect <b>around the TARGET_LABEL</b> on the preview below, then click Save coords ✓. ESC to reset.</header>
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
      saved.innerHTML='<span style="color:#0a0">✓ saved!</span> — close this tab when done.';
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

func main() {
	var (
		sourcePath  = flag.String("source", "", "load PNG from this path instead of capturing live from ADB")
		refW        = flag.Int("ref-w", game.RefWidth, "reference width (default 860)")
		refH        = flag.Int("ref-h", game.RefHeight, "reference height (default 732)")
		alsoAlt     = flag.Bool("also-alt", false, "capture a SECOND rectangle for tap_roi_alt after the primary")
		alsoBtns    = flag.Bool("also-buttons", false, "capture skip_button (separately from confirm_yes_button)")
		confirmOnly = flag.Bool("confirm-only", false, "capture ONLY confirm_yes_button. Use this AFTER manually tapping Skip on the chest so the dialog is visible.")
		timeout     = flag.Duration("timeout", 5*time.Minute, "browser-drag timeout")
		adbHost     = flag.String("adb-host", "127.0.0.1", "ADB host (capture source)")
		adbPort     = flag.Int("adb-port", 5037, "ADB port")
		deviceID    = flag.String("device", "emulator-5554", "ADB device id")
		outPath     = flag.String("out", "", "output JSON path (default assets/chest_dismiss_roi.json)")
		adbTimeout  = flag.Duration("adb-timeout", 15*time.Second, "ADB capture timeout")
	)
	flag.Parse()

	if *outPath == "" {
		*outPath = paths.Resolve("chest_dismiss_roi.json")
	}

	pngPath, physW, physH, err := acquireSource(*sourcePath, *adbHost, *adbPort, *deviceID, *adbTimeout)
	if err != nil {
		log.Fatalf("acquire source: %v", err)
	}
	defer func() {
		_ = os.Remove(pngPath)
	}()

	scaleX := safeDiv(float64(physW), float64(*refW))
	scaleY := safeDiv(float64(physH), float64(*refH))

	saved := &game.ChestROISchema{}
	if existing, lerr := game.LoadChestDismissConfig(); lerr == nil && existing != nil {
		saved = existing
	}

	fmt.Println("────────────────────────────────────────────")
	fmt.Printf("Ref frame: %dx%d     Capture: %dx%d     scale: (%.3f, %.3f)\n",
		*refW, *refH, physW, physH, scaleX, scaleY)
	if !nearlyEqual(scaleX, 1.0) || !nearlyEqual(scaleY, 1.0) {
		fmt.Println("⚠ capture-to-ref is not 1:1; selected rect will be")
		fmt.Println("  rescaled to REF coordinates before saving.")
	}

	// Two high-level modes:
	//   - normal: drag primary (tap_roi), optional alt, optional skip.
	//   - -confirm-only: skip the above entirely; just drag
	//     confirm_yes_button off a confirmation dialog the user has
	//     already opened. Loads the existing JSON (which already
	//     has tap_roi/skip_button from the previous run) and only
	//     updates confirm_yes_button, preserving everything else.

	if !*confirmOnly {
		// 1. primary tap_roi.
		box, err := dragForRect(pngPath, "primary chest tap zone", physW, physH, *timeout)
		if err != nil {
			log.Fatalf("primary drag: %v", err)
		}
		saved.TapROI = boxToRef(*box, scaleX, scaleY)
		fmt.Printf("✓ primary: %s (phys) → %+v (ref)\n", box, saved.TapROI)
		printPixelsPng(pngPath, *box)

		// 2. optional tap_roi_alt.
		if *alsoAlt {
			box2, err := dragForRect(pngPath, "alternate chest tap zone", physW, physH, *timeout)
			if err != nil {
				fmt.Printf("⚠ alt drag skipped: %v\n", err)
				fmt.Printf("  primary rect already saved; re-run with -also-alt to retry.\n")
			} else {
				saved.TapROIAlt = boxToRef(*box2, scaleX, scaleY)
				fmt.Printf("✓ alt: %s (phys) → %+v (ref)\n", box2, saved.TapROIAlt)
				printPixelsPng(pngPath, *box2)
			}
		}

		// 3. optional skip button (fast-path recovery, part 1).
		//    Confirm Yes is captured separately via -confirm-only.
		if *alsoBtns {
			boxSkip, err := dragForRect(pngPath, "skip button on the chest screen", physW, physH, *timeout)
			if err != nil {
				fmt.Printf("⚠ skip drag skipped: %v\n", err)
			} else {
				saved.SkipButton = boxToRef(*boxSkip, scaleX, scaleY)
				fmt.Printf("✓ skip button: %s (phys) → %+v (ref)\n", boxSkip, saved.SkipButton)
				printPixelsPng(pngPath, *boxSkip)
				fmt.Println()
				fmt.Println(">>> NEXT STEP <<<")
				fmt.Println("  1. Tap Skip on the chest screen in-game.")
				fmt.Println("  2. Wait for the Confirm Yes dialog to appear.")
				fmt.Println("  3. Run:  go run cmd/pick_chest_roi/main.go -confirm-only")
				fmt.Println("to capture the Confirm Yes button off the dialog.")
			}
		}
	}

	if *confirmOnly {
		boxConfirm, err := dragForRect(pngPath, "confirm yes button on the post-skip dialog", physW, physH, *timeout)
		if err != nil {
			fmt.Printf("⚠ confirm drag skipped: %v\n", err)
			fmt.Printf("  existing chest_dismiss_roi.json preserved; re-run to retry.\n")
		} else {
			saved.ConfirmYesButton = boxToRef(*boxConfirm, scaleX, scaleY)
			fmt.Printf("✓ confirm yes: %s (phys) → %+v (ref)\n", boxConfirm, saved.ConfirmYesButton)
			printPixelsPng(pngPath, *boxConfirm)
		}
	}

	// 4. validate + write JSON. Invalid optional rects are silently
	//    nil'd so a partial run still produces a valid config that
	//    gracefully degrades to the tap-scan fallback.
	if saved.TapROI == nil || !rectInBounds(saved.TapROI) {
		log.Fatalf("post-selection primary rect failed validation: %+v", saved)
	}
	if saved.TapROIAlt != nil && !rectInBounds(saved.TapROIAlt) {
		saved.TapROIAlt = nil
	}
	if saved.SkipButton != nil && !rectInBounds(saved.SkipButton) {
		saved.SkipButton = nil
	}
	if saved.ConfirmYesButton != nil && !rectInBounds(saved.ConfirmYesButton) {
		saved.ConfirmYesButton = nil
	}
	blob, _ := json.MarshalIndent(saved, "", "  ")
	if err := os.WriteFile(*outPath, append(blob, '\n'), 0o644); err != nil {
		log.Fatalf("write %s: %v", *outPath, err)
	}
	fmt.Printf("✓ wrote %s\n", *outPath)
	fmt.Println("Restart the bot (or wait for next bot-startup) to pick up")
	fmt.Println("the new geometry.")
}

// acquireSource returns (pngFilePath, physW, physH, err). If sourcePath
// is non-empty it IMRead's that PNG; otherwise it captures live from
// ADB. In both branches the result is IMWrite'd into a temp file
// the HTTP server can host.
func acquireSource(sourcePath, adbHost string, adbPort int, deviceID string, adbTimeout time.Duration) (string, int, int, error) {
	tmp, err := os.CreateTemp("", "clashgo-pick-chest-roi-*.png")
	if err != nil {
		return "", 0, 0, err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()

	var mat gocv.Mat
	if sourcePath != "" {
		mat = gocv.IMRead(sourcePath, gocv.IMReadColor)
		if mat.Empty() {
			_ = os.Remove(tmpPath)
			return "", 0, 0, fmt.Errorf("IMRead(%q) returned empty", sourcePath)
		}
		fmt.Printf("Loaded %s (%dx%d)\n", sourcePath, mat.Cols(), mat.Rows())
	} else {
		client := adb.NewClient(
			adb.WithHost(adbHost),
			adb.WithPort(adbPort),
			adb.WithTimeout(adbTimeout),
		)
		client.DeviceID = deviceID
		if err := client.Connect(); err != nil {
			_ = os.Remove(tmpPath)
			return "", 0, 0, fmt.Errorf("adb connect: %w", err)
		}
		defer client.Close()
		mat, err = client.CaptureToMat()
		if err != nil {
			_ = os.Remove(tmpPath)
			return "", 0, 0, fmt.Errorf("adb capture: %w", err)
		}
		fmt.Printf("Captured %dx%d from %s:%d/%s\n", mat.Cols(), mat.Rows(), adbHost, adbPort, deviceID)
	}
	if !gocv.IMWrite(tmpPath, mat) {
		_ = os.Remove(tmpPath)
		mat.Close()
		return "", 0, 0, fmt.Errorf("IMWrite(%s) failed", tmpPath)
	}
	physW := mat.Cols()
	physH := mat.Rows()
	mat.Close()
	return tmpPath, physW, physH, nil
}

// dragForRect serves the screenshot at /preview.png and the drag UI
// at /. Browser POSTs x1 y1 x2 y2 to /coords; return value is in
// PHYSICAL pixels of the captured frame. Adapted from
// cmd/capture_template/main.go::dragForRect.
func dragForRect(previewPath, targetLabel string, screenW, screenH int, timeout time.Duration) (*image.Rectangle, error) {
	mux := http.NewServeMux()
	tplLabel := html.EscapeString(targetLabel)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, strings.Replace(dragHTML, "TARGET_LABEL", tplLabel, 1))
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
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
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
		fmt.Printf("  Paste this URL into your browser manually:\n  %s\n", url)
	}
	fmt.Printf("Drag a rect around: %s\n", targetLabel)
	fmt.Println("Then click 'Save coords ✓'. ESC resets. Timeout:", timeout)
	fmt.Println("────────────────────────────────────────────────────────")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	select {
	case coords := <-coordsCh:
		if coords.Dx() < 4 || coords.Dy() < 4 {
			return nil, fmt.Errorf("rect too small (%dx%d); need >= 4x4", coords.Dx(), coords.Dy())
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

// boxToRef converts a physical-pixel rectangle into Reference space
// (game RefWidth × RefHeight), clamped to the valid range so a tiny
// drag past the edge can't produce an out-of-range config.
func boxToRef(box image.Rectangle, scaleX, scaleY float64) *game.Rectangle {
	if box.Empty() {
		return nil
	}
	return &game.Rectangle{
		X1: clamp(int(float64(box.Min.X)/scaleX), 0, game.RefWidth),
		Y1: clamp(int(float64(box.Min.Y)/scaleY), 0, game.RefHeight),
		X2: clamp(int(float64(box.Max.X)/scaleX), 0, game.RefWidth),
		Y2: clamp(int(float64(box.Max.Y)/scaleY), 0, game.RefHeight),
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func safeDiv(a, b float64) float64 {
	if b <= 0 {
		return 1.0
	}
	return a / b
}

func nearlyEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-6
}

// rectInBounds reports whether the rectangle has positive area AND
// lies fully inside the reference frame (game.RefWidth × RefHeight).
// Mirrors game.Rectangle.isValid() — kept duplicated here because
// that method is unexported and `cmd/pick_chest_roi` is in package
// `main`. Keep both definitions in sync if the rules ever change.
func rectInBounds(r *game.Rectangle) bool {
	if r == nil {
		return false
	}
	if r.X2 <= r.X1 || r.Y2 <= r.Y1 {
		return false
	}
	if r.X1 < 0 || r.Y1 < 0 || r.X2 > game.RefWidth || r.Y2 > game.RefHeight {
		return false
	}
	return true
}

// printPixelsPng re-reads the PNG from disk and samples 5 evenly-
// spaced points inside `box`. Decoded here (rather than retained as
// gocv.Mat) so we release the capture handle between drags.
func printPixelsPng(pngPath string, box image.Rectangle) {
	f, err := os.Open(pngPath)
	if err != nil {
		fmt.Printf("(pixel sample skipped: open %s: %v)\n", pngPath, err)
		return
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		fmt.Printf("(pixel sample skipped: png.Decode: %v)\n", err)
		return
	}
	pixSamples(img, box)
}

func pixSamples(img image.Image, box image.Rectangle) {
	r := box
	if img == nil || r.Empty() {
		fmt.Println("no rect or no image")
		return
	}
	pts := []image.Point{
		{X: (r.Min.X + r.Max.X) / 2, Y: (r.Min.Y + r.Max.Y) / 2},
		{X: r.Min.X + r.Dx()/4, Y: r.Min.Y + r.Dy()/4},
		{X: r.Min.X + 3*r.Dx()/4, Y: r.Min.Y + r.Dy()/4},
		{X: r.Min.X + r.Dx()/4, Y: r.Min.Y + 3*r.Dy()/4},
		{X: r.Min.X + 3*r.Dx()/4, Y: r.Min.Y + 3*r.Dy()/4},
	}
	b := img.Bounds()
	fmt.Println("Pixel sample inside rect (RGB hex — paste into classifier.go):")
	for i, pt := range pts {
		if !pt.In(b) {
			continue
		}
		c := img.At(pt.X, pt.Y)
		r16, g16, b16, _ := c.RGBA()
		r8, g8, b8 := uint8(r16>>8), uint8(g16>>8), uint8(b16>>8)
		fmt.Printf("  [%d] (%d,%d) hex=0x%02X,0x%02X,0x%02X\n",
			i, pt.X, pt.Y, r8, g8, b8)
	}
}
