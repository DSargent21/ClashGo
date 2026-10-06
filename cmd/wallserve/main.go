// wallserve is the local picking session: a page you drag boxes on, a Save button,
// and the picks written to a file on this machine instead of sitting in a browser
// tab.
//
// Why a server rather than a page you open from disk: the picks are the input to
// a coordinate conversion the browser cannot do (the asset files are 860x732
// reference values, the drag is device pixels, and the law between them is
// piecewise — see internal/game/calibration.go). Something on this side has to
// receive them, and a file:// page cannot send anything anywhere. Serving over
// loopback also lets the same page drive the device: the capture button runs the
// capture session, which is what makes the whole thing one window with no
// terminal prompts.
//
// It shells out to tools/wall_pick.sh for both capture and conversion rather than
// reimplementing either, which keeps one tested path and means this command needs
// no OpenCV:
//
//	go run ./cmd/wallserve          # then open the printed URL
//	./tools/wall_pick.sh serve      # same thing, plus the capture session around it
//
// Routes:
//
//	GET  /            the picking page
//	GET  /frame/{n}   one captured frame as JPEG, for refreshing a card in place
//	POST /capture     {step:N}  run the capture session for that step
//	POST /save        {picks}   write the picks to <dir>/picks.txt
//	POST /apply       {picks}   write picks.txt, convert, and write the asset files
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/pickpage"
)

func main() {
	var (
		addr    = flag.String("addr", "127.0.0.1:8765", "loopback address to serve on")
		dir     = flag.String("dir", "output/wall_pick", "directory holding the captured frames and where picks.txt is written")
		script  = flag.String("script", "./tools/wall_pick.sh", "capture + conversion script to delegate to")
		quality = flag.Int("quality", 78, "JPEG quality for the inlined frames")
		timeout = flag.Duration("timeout", 90*time.Second, "how long one capture may take")
	)
	flag.Parse()

	abs, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatalf("wallserve: %v", err)
	}
	*dir = abs

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		frames, err := pickpage.Frames(*dir, *quality)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		page, err := pickpage.Build(frames, true)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page)
	})

	mux.HandleFunc("GET /frame/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(r.PathValue("n"))
		if err != nil || n < 1 || n > pickpage.Steps {
			http.NotFound(w, r)
			return
		}
		path := stepPath(*dir, n)
		data, err := os.ReadFile(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})

	mux.HandleFunc("POST /capture", func(w http.ResponseWriter, r *http.Request) {
		var req stepRequest
		if err := decode(r, &req); err != nil || req.Step < 1 || req.Step > pickpage.Steps {
			http.Error(w, "want {\"step\":1.."+strconv.Itoa(pickpage.Steps)+"}", http.StatusBadRequest)
			return
		}
		step := strconv.Itoa(req.Step)
		log.Printf("capture: step %s", step)
		out, err := run(*dir, *script, *timeout, nil, "capture", step)
		report(w, "captured step "+step, out, err)
	})

	mux.HandleFunc("POST /save", func(w http.ResponseWriter, r *http.Request) {
		var req picksRequest
		if err := decode(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		path, n, err := writePicks(*dir, req.Picks)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("save: %d boxes -> %s", n, path)
		fmt.Fprintf(w, "wrote %d boxes to %s\n\n%s", n, path, strings.TrimSpace(req.Picks))
	})

	mux.HandleFunc("POST /apply", func(w http.ResponseWriter, r *http.Request) {
		var req picksRequest
		if err := decode(r, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		lines := pickLines(req.Picks)
		path, n, err := writePicks(*dir, req.Picks)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("apply: %d boxes from %s", n, path)
		// The script derives the frame size from the captures and the display
		// scale from the device config, so nothing here has to know either.
		out, err := run(*dir, *script, *timeout, strings.NewReader(strings.Join(lines, "\n")), "apply")
		report(w, "wrote the asset files", out, err)
	})

	log.Printf("wall picking session on http://%s", *addr)
	log.Printf("frames + picks in %s", *dir)
	log.Printf("open the URL above, drag a box per card, then press save")
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("wallserve: %v (is %s already in use? try -addr 127.0.0.1:8899)", err, *addr)
	}
}

func stepPath(dir string, n int) string {
	return filepath.Join(dir, fmt.Sprintf("step%d.png", n))
}

// run executes the capture/conversion script and returns its combined output: the
// report shown next to the Save button is the script's own, so what the operator
// sees here is exactly what the terminal session would have printed.
func run(dir, script string, timeout time.Duration, stdin io.Reader, args ...string) (string, error) {
	// WALL_PICK_DIR keeps a probe server's captures out of the real session's
	// directory, and WALL_PICK_NO_OPEN stops a new browser tab per capture. The
	// script resolves the repo root from its own location, so the caller's working
	// directory does not matter.
	cmd := exec.Command(script, args...)
	cmd.Stdin = stdin
	cmd.Env = append(os.Environ(), "WALL_PICK_NO_OPEN=1", "WALL_PICK_DIR="+dir)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		return string(out), fmt.Errorf("timed out after %s", timeout)
	}
	return string(out), err
}

// report writes the script's output plus a one-line verdict. Both go to the body
// as plain text: the page shows them verbatim in its report pane.
func report(w http.ResponseWriter, okMsg, out string, err error) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "%s failed: %v\n\n%s", okMsg, err, strings.TrimSpace(out))
		return
	}
	fmt.Fprintf(w, "%s\n\n%s", okMsg, strings.TrimSpace(out))
}

type stepRequest struct {
	Step int `json:"step"`
}

type picksRequest struct {
	Picks string `json:"picks"`
}

// decode accepts the page's JSON, and also a bare picks body, so the endpoints
// can be driven with curl while debugging: `curl -d 'gold 1 2 3 4' .../save`.
func decode(r *http.Request, v any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return fmt.Errorf("empty request body")
	}
	if json.Unmarshal(body, v) == nil {
		return nil
	}
	if p, ok := v.(*picksRequest); ok {
		p.Picks = string(body)
		return nil
	}
	return fmt.Errorf("cannot read request: %s", strings.TrimSpace(string(body)))
}

// writePicks stores the picked lines in dir/picks.txt, filtered so a stray paste
// cannot leave a line the converter would later reject. The header is a comment,
// which cmd/refmap skips, so the file can be piped straight into it.
func writePicks(dir, body string) (string, int, error) {
	lines := pickLines(body)
	if len(lines) == 0 {
		return "", 0, fmt.Errorf("no picks to save")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, err
	}
	path := filepath.Join(dir, "picks.txt")
	text := fmt.Sprintf("# wall-flow picks — %s\n# from the picking page (cmd/wallserve)\n%s\n",
		time.Now().Format(time.RFC3339), strings.Join(lines, "\n"))
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", 0, err
	}
	return path, len(lines), nil
}

func pickLines(body string) []string {
	var out []string
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}
