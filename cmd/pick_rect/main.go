// Command pick_rect is a generic drag-to-pick CLI. It captures a
// screenshot from ADB (or a PNG file via -source), opens a browser
// drag UI labeled with -label, and saves the chosen rect to the
// JSON file at -out.
//
// Use cases:
//   - capture a "Continue" button after chest dismissal
//   - capture any single UI element bounding box
//   - quickly sketch a ref-space rect without writing a new picker
//
// The saved JSON shape is the bare Rectangle:
//
//	{
//	  "x1": 100, "y1": 200, "x2": 300, "y2": 400
//	}
//
// which matches `game.Rectangle` so callers can json.Unmarshal it
// directly into a *game.Rectangle without a wrapper struct.
//
// Coords are stored in REFERENCE space (game.RefWidth × RefHeight)
// regardless of capture resolution; the runtime ScaleRef completes
// the conversion at tap-time.
//
// Typical workflow:
//
//	# 1. Stage the screen you want to capture.
//	# 2. Drag a rect around the target.
//	go run cmd/pick_rect/main.go -label "Continue button" -out assets/continue_button.json
//	# 3. Restart the consumer (or wait for next bot-startup) to pick up the new geometry.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"image"
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
	"gocv.io/x/gocv"
)

// dragHTML is a stripped-down version of cmd/pick_chest_roi::dragHTML.
// It only renders an <img> overlay with a draggable rect; no save->
// coords handler integration, no per-label UI scaffolding beyond
// the header text.
const dragHTML = `<!doctype html><html><head>
<meta charset="utf-8"><title>pick_rect — drag crop</title>
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
		sourcePath = flag.String("source", "", "load PNG from this path instead of capturing live from ADB")
		label      = flag.String("label", "target element", "text shown to the user describing what to drag")
		outPath    = flag.String("out", "", "output JSON path for the chosen rect (REQUIRED — no default to avoid cwd pollution)")
		refW       = flag.Int("ref-w", game.RefWidth, "reference width (default 860)")
		refH       = flag.Int("ref-h", game.RefHeight, "reference height (default 732)")
		timeout    = flag.Duration("timeout", 5*time.Minute, "browser-drag timeout")
		adbHost    = flag.String("adb-host", "127.0.0.1", "ADB host (capture source)")
		adbPort    = flag.Int("adb-port", 5037, "ADB port")
		deviceID   = flag.String("device", "emulator-5554", "ADB device id")
		adbTimeout = flag.Duration("adb-timeout", 15*time.Second, "ADB capture timeout")
	)
	flag.Parse()

	// Require explicit -out. Writing to cwd would silently pollute the
	// project tree (and trigger wails dev rebuilds on every save); the
	// chest picker defaults to paths.Resolve(...) instead, but pick_rect
	// is generic and shouldn't assume a project layout.
	if *outPath == "" {
		log.Fatalf("-out is required (e.g. -out assets/continue_button.json)")
	}

	// 1. Acquire the source screenshot.
	pngPath, physW, physH, err := acquireSource(*sourcePath, *adbHost, *adbPort, *deviceID, *adbTimeout)
	if err != nil {
		log.Fatalf("acquire source: %v", err)
	}
	defer func() { _ = os.Remove(pngPath) }()

	scaleX := safeDiv(float64(physW), float64(*refW))
	scaleY := safeDiv(float64(physH), float64(*refH))

	fmt.Println("────────────────────────────────────────────")
	fmt.Printf("Label:    %s\n", *label)
	fmt.Printf("Output:   %s\n", *outPath)
	fmt.Printf("Ref:      %dx%d     Capture: %dx%d     scale: (%.3f, %.3f)\n",
		*refW, *refH, physW, physH, scaleX, scaleY)
	if !nearlyEqual(scaleX, 1.0) || !nearlyEqual(scaleY, 1.0) {
		fmt.Println("⚠ capture-to-ref is not 1:1; selected rect will be rescaled to REF coordinates before saving.")
	}

	// 2. Open browser drag UI and wait for the user to drop a rect.
	box, err := dragForRect(pngPath, *label, physW, physH, *timeout)
	if err != nil {
		log.Fatalf("drag: %v", err)
	}

	// 3. Convert to ref space, validate, write JSON.
	refRect := boxToRef(*box, scaleX, scaleY)
	if !rectInBounds(refRect) {
		log.Fatalf("selected rect failed ref-frame validation: %+v", refRect)
	}

	payload := map[string]int{
		"x1": refRect.X1,
		"y1": refRect.Y1,
		"x2": refRect.X2,
		"y2": refRect.Y2,
	}
	blob, _ := json.MarshalIndent(payload, "", "  ")
	if err := os.WriteFile(*outPath, append(blob, '\n'), 0o644); err != nil {
		log.Fatalf("write %s: %v", *outPath, err)
	}

	fmt.Printf("✓ %s (phys) → %s (ref)\n", box, refRect)
	fmt.Printf("✓ wrote %s\n", *outPath)
	fmt.Println("Restart the consumer (or wait for next bot-startup) to pick up the new geometry.")
}

// acquireSource returns (pngFilePath, physW, physH, err). If sourcePath
// is non-empty it IMRead's that PNG; otherwise it captures live from
// ADB. In both branches the result is IMWrite'd into a temp file
// the HTTP server can host.
func acquireSource(sourcePath, adbHost string, adbPort int, deviceID string, adbTimeout time.Duration) (string, int, int, error) {
	tmp, err := os.CreateTemp("", "clashgo-pick-rect-*.png")
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
		var capErr error
		mat, capErr = client.CaptureToMat()
		if capErr != nil {
			_ = os.Remove(tmpPath)
			return "", 0, 0, fmt.Errorf("adb capture: %w", capErr)
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
// PHYSICAL pixels of the captured frame.
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
// (game.RefWidth × game.RefHeight), clamped to the valid range so a
// tiny drag past the edge can't produce an out-of-range config.
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

// rectInBounds mirrors game.Rectangle.isValid() — duplicated here
// because that method is unexported and cmd/pick_rect is in package
// main. Keep both definitions in sync if the rules ever change.
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
