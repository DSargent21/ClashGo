// Command pick_coords is the deploy-point picker: it shows the live emulator
// frame in the Preview tab and lets you click the exact points the bot should
// use, per side, then writes them into precision_config.json.
//
// Why it exists: the deploy geometry was being argued about in code (red-line
// detection, band clamps, formula substitution, radial push-out) while the
// ground truth is simply "these are the tiles the game accepts on this side".
// A human looking at the battle screen knows that instantly; the bot could not
// work it out, and every automatic correction it tried produced a new failure
// mode. This tool moves that decision back to the human, once per side, and
// makes the result authoritative.
//
// The coordinate convention is the trap this tool exists to hide. The pins in
// precision_config.json are authored in the file's own reference geometry
// (width/height, currently 860x732) and the bot scales them by
// live/reference per axis on load. You click on a 1280x720 frame; the tool
// converts, so the numbers you see on screen are the taps you get.
//
// Usage:
//
//	./build/bin/pick_coords                 # serve on 127.0.0.1:8791
//	./build/bin/pick_coords -addr 127.0.0.1:9000 -device localhost:5555
//	make pick-coords                        # same, via the Makefile
//
// It never writes frames to disk and only captures when the page asks for a
// frame, so it cannot fill the disk the way the ad-hoc capture loops did.
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
	"runtime"
	"sort"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/paths"
	"gocv.io/x/gocv"
)

const configName = "precision_config.json"

// startScale is what a fresh config (or a missing width/height) is authored at.
const (
	defaultRefW = 860
	defaultRefH = 732
)

type ptJSON struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type edgeJSON struct {
	P1 ptJSON `json:"p1"`
	P2 ptJSON `json:"p2"`
}

// itemsJSON is the five things the user pins, all in LIVE screen pixels.
type itemsJSON struct {
	TroopLine  map[string]edgeJSON `json:"troop_line"`
	SpellA     map[string]edgeJSON `json:"spell_a"`
	SpellB     map[string]edgeJSON `json:"spell_b"`
	Hero       map[string]ptJSON   `json:"hero"`
	SpellPoint map[string]ptJSON   `json:"spell_point"`
}

type dims struct {
	W int `json:"w"`
	H int `json:"h"`
}

type stateJSON struct {
	ConfigPath string    `json:"config_path"`
	Live       dims      `json:"live"`
	Ref        dims      `json:"ref"`
	Items      itemsJSON `json:"items"`
	Note       string    `json:"note,omitempty"`
}

// server holds the picker's state: the frame the user is clicking on and the
// device/config it writes to.
type server struct {
	device     string
	configPath string
	ref        dims
	live       dims
	framePNG   []byte
	capturedAt time.Time
	captureErr string
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8791", "address to serve the picker on")
	device := flag.String("device", "localhost:5555", "ADB device to capture")
	configPath := flag.String("config", "", "path to precision_config.json (default: the one the bot reads)")
	open := flag.Bool("open", false, "open the picker in your browser")
	flag.Parse()

	s := &server{device: *device, configPath: *configPath}
	if s.configPath == "" {
		s.configPath = paths.Resolve(configName)
	}
	s.loadRef()

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/state", s.handleState)
	mux.HandleFunc("/frame.png", s.handleFrame)
	mux.HandleFunc("/save", s.handleSave)

	url := "http://" + *addr + "/"
	fmt.Printf("pick_coords: config=%s\n", s.configPath)
	fmt.Printf("pick_coords: reference geometry %dx%d, live frame will be measured on first capture\n", s.ref.W, s.ref.H)
	fmt.Printf("pick_coords: serving %s\n", url)
	fmt.Printf("pick_coords: put the game on the side you are pinning, then click the frame in the Preview tab\n")
	if *open {
		openBrowser(url)
	}
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("pick_coords: %v", err)
	}
}

// loadRef reads the reference geometry the config file declares.
func (s *server) loadRef() {
	s.ref = dims{W: defaultRefW, H: defaultRefH}
	raw, err := os.ReadFile(s.configPath)
	if err != nil {
		return
	}
	var cfg struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return
	}
	if cfg.Width > 0 && cfg.Height > 0 {
		s.ref = dims{W: cfg.Width, H: cfg.Height}
	}
}

// ---------------------------------------------------------------- HTTP

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, indexHTML)
}

// handleState returns the saved pins converted into live pixels.
func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	s.loadRef()
	// Measure the live frame here rather than relying on the page to have
	// fetched one: the conversion below is meaningless (and silently shows
	// reference coordinates as if they were live) until the live size is known.
	if s.live.W <= 0 || s.live.H <= 0 {
		if png, w0, h0, err := s.capture(); err == nil {
			s.framePNG, s.live, s.capturedAt = png, dims{W: w0, H: h0}, time.Now()
			s.captureErr = ""
		} else {
			s.captureErr = fmt.Sprintf("capture failed (%v): is the emulator up and CoC on screen?", err)
		}
	}
	st := stateJSON{
		ConfigPath: s.configPath,
		Live:       s.live,
		Ref:        s.ref,
		Items: itemsJSON{
			TroopLine:  map[string]edgeJSON{},
			SpellA:     map[string]edgeJSON{},
			SpellB:     map[string]edgeJSON{},
			Hero:       map[string]ptJSON{},
			SpellPoint: map[string]ptJSON{},
		},
	}
	if s.captureErr != "" {
		st.Note = s.captureErr
	}
	raw, err := os.ReadFile(s.configPath)
	if err == nil {
		var cfg struct {
			Leading  string                 `json:"-"` // unused; keeps the shape obvious
			Edges    map[string]edgeJSON    `json:"edges"`
			Sides    map[string]edgeJSON    `json:"sides"`
			SpellA   map[string]edgeJSON    `json:"spell_edges_a"`
			SpellB   map[string]edgeJSON    `json:"spell_edges_b"`
			Hero     map[string]ptJSON      `json:"hero_targets"`
			Spell    map[string]ptJSON      `json:"spell_targets"`
			ExtraRaw map[string]interface{} `json:"-"`
		}
		if json.Unmarshal(raw, &cfg) == nil {
			for k, v := range cfg.Edges {
				st.Items.TroopLine[k] = s.refToLiveEdge(v)
			}
			for k, v := range cfg.Sides {
				// Sides is a legacy alias; surface it as a troop line so the user
				// sees what the bot would use for that side.
				if _, ok := st.Items.TroopLine[k]; !ok {
					st.Items.TroopLine[k] = s.refToLiveEdge(v)
				}
			}
			for k, v := range cfg.SpellA {
				st.Items.SpellA[k] = s.refToLiveEdge(v)
			}
			for k, v := range cfg.SpellB {
				st.Items.SpellB[k] = s.refToLiveEdge(v)
			}
			for k, v := range cfg.Hero {
				st.Items.Hero[k] = s.refToLivePt(v)
			}
			for k, v := range cfg.Spell {
				st.Items.SpellPoint[k] = s.refToLivePt(v)
			}
		} else {
			st.Note = "config file is not valid JSON: " + err.Error()
		}
	} else {
		st.Note = "config file not found yet; saving will create it"
	}
	writeJSON(w, st)
}

// handleFrame captures a fresh frame on demand and hands back its size.
func (s *server) handleFrame(w http.ResponseWriter, r *http.Request) {
	png, w0, h0, err := s.capture()
	if err != nil {
		s.captureErr = fmt.Sprintf("capture failed (%v): is the emulator up and CoC on screen?", err)
		http.Error(w, s.captureErr, http.StatusBadGateway)
		return
	}
	s.captureErr = ""
	s.framePNG, s.live = png, dims{W: w0, H: h0}
	s.capturedAt = time.Now()
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Width", fmt.Sprint(w0))
	w.Header().Set("X-Frame-Height", fmt.Sprint(h0))
	w.Write(png)
}

// handleSave converts the live-pixel pins back into the file's reference
// geometry and merges them into precision_config.json, leaving every other key
// (width, height, bar_y, future additions) untouched.
func (s *server) handleSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in itemsJSON
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.live.W <= 0 || s.live.H <= 0 {
		// Measure it rather than refusing: the conversion is the whole point of
		// this endpoint, and a save arriving before any frame was fetched is a
		// normal thing for a script or a reloaded page to do.
		if _, w0, h0, err := s.capture(); err != nil {
			http.Error(w, "no live frame measured yet and capture failed: "+err.Error(), http.StatusConflict)
			return
		} else {
			s.live = dims{W: w0, H: h0}
		}
	}

	fields := map[string]interface{}{}
	if raw, err := os.ReadFile(s.configPath); err == nil {
		_ = json.Unmarshal(raw, &fields)
	}
	if fields == nil {
		fields = map[string]interface{}{}
	}
	if _, ok := fields["width"]; !ok {
		fields["width"] = s.live.W
		fields["height"] = s.live.H
		s.ref = s.live
	}

	// Per-side merge, never a section replace: saving one side must not drop
	// the other three (they are picked in separate sessions, one battle each).
	setEdges := func(key string, m map[string]edgeJSON) {
		if len(m) == 0 {
			return
		}
		out := map[string]interface{}{}
		if prev, ok := fields[key].(map[string]interface{}); ok {
			for k, v := range prev {
				out[k] = v
			}
		}
		for side, e := range m {
			a, b := s.liveToRefEdge(e)
			out[side] = map[string]interface{}{"p1": map[string]int{"X": a.X, "Y": a.Y}, "p2": map[string]int{"X": b.X, "Y": b.Y}}
		}
		fields[key] = out
	}
	setPts := func(key string, m map[string]ptJSON) {
		if len(m) == 0 {
			return
		}
		out := map[string]interface{}{}
		if prev, ok := fields[key].(map[string]interface{}); ok {
			for k, v := range prev {
				out[k] = v
			}
		}
		for side, p := range m {
			q := s.liveToRefPt(p)
			out[side] = map[string]int{"X": q.X, "Y": q.Y}
		}
		fields[key] = out
	}
	setEdges("edges", in.TroopLine)
	setEdges("spell_edges_a", in.SpellA)
	setEdges("spell_edges_b", in.SpellB)
	setPts("hero_targets", in.Hero)
	setPts("spell_targets", in.SpellPoint)

	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		http.Error(w, "marshal: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.configPath), 0o755); err != nil {
		http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(s.configPath, append(data, '\n'), 0o644); err != nil {
		http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"ok":            true,
		"written":       s.configPath,
		"sides":         sortedSides(in),
		"live":          s.live,
		"ref":           s.ref,
		"live_pixels":   true,
		"stored_pixels": fmt.Sprintf("%dx%d reference", s.ref.W, s.ref.H),
	})
}

func sortedSides(in itemsJSON) []string {
	seen := map[string]bool{}
	for _, m := range []map[string]edgeJSON{in.TroopLine, in.SpellA, in.SpellB} {
		for k := range m {
			seen[k] = true
		}
	}
	for _, m := range []map[string]ptJSON{in.Hero, in.SpellPoint} {
		for k := range m {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- capture

func (s *server) capture() ([]byte, int, int, error) {
	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = s.device
	defer client.Close()

	mat, err := client.CaptureToMat()
	if err != nil {
		return nil, 0, 0, err
	}
	defer mat.Close()
	if mat.Empty() {
		return nil, 0, 0, fmt.Errorf("empty capture")
	}
	buf, err := gocv.IMEncode(gocv.PNGFileExt, mat)
	if err != nil {
		return nil, 0, 0, err
	}
	defer buf.Close()
	return buf.GetBytes(), mat.Cols(), mat.Rows(), nil
}

// ---------------------------------------------------------------- geometry

func (s *server) refToLiveEdge(e edgeJSON) edgeJSON {
	return edgeJSON{P1: s.refToLivePt(e.P1), P2: s.refToLivePt(e.P2)}
}

func (s *server) refToLivePt(p ptJSON) ptJSON {
	if s.ref.W <= 0 || s.ref.H <= 0 || s.live.W <= 0 || s.live.H <= 0 {
		return p
	}
	return ptJSON{
		X: int(float64(p.X) * float64(s.live.W) / float64(s.ref.W)),
		Y: int(float64(p.Y) * float64(s.live.H) / float64(s.ref.H)),
	}
}

func (s *server) liveToRefEdge(e edgeJSON) (ptJSON, ptJSON) {
	return s.liveToRefPt(e.P1), s.liveToRefPt(e.P2)
}

func (s *server) liveToRefPt(p ptJSON) ptJSON {
	if s.ref.W <= 0 || s.ref.H <= 0 || s.live.W <= 0 || s.live.H <= 0 {
		return p
	}
	return ptJSON{
		X: int(float64(p.X)*float64(s.ref.W)/float64(s.live.W) + 0.5),
		Y: int(float64(p.Y)*float64(s.ref.H)/float64(s.live.H) + 0.5),
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
