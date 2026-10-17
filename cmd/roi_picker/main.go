package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type ROIItem struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Label    string `json:"label"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	W        int    `json:"w"`
	H        int    `json:"h"`
	CenterX  int    `json:"cx"`
	CenterY  int    `json:"cy"`
}

type TaskItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	TargetFile  string `json:"target_file,omitempty"`
}

type server struct {
	mu         sync.Mutex
	device     string
	lastPNG    []byte
	frameW     int
	frameH     int
	savedROIs  []ROIItem
	outputPath string
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8795", "address to serve the ROI picker on")
	device := flag.String("device", "localhost:5555", "ADB device")
	open := flag.Bool("open", true, "open browser automatically")
	flag.Parse()

	s := &server{
		device:     *device,
		outputPath: "output/user_rois.json",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/frame.png", s.handleFrame)
	mux.HandleFunc("/crop.png", s.handleCrop)
	mux.HandleFunc("/tap", s.handleTap)
	mux.HandleFunc("/save", s.handleSave)
	mux.HandleFunc("/save-template", s.handleSaveTemplate)
	mux.HandleFunc("/tasks", s.handleTasks)

	url := "http://" + *addr + "/"
	fmt.Printf("\n=======================================================\n")
	fmt.Printf(" ClashGO Visual ROI & Coordinate Selector\n")
	fmt.Printf(" Server running at: %s\n", url)
	fmt.Printf(" Device: %s\n", *device)
	fmt.Printf("=======================================================\n\n")

	if *open {
		go func() {
			time.Sleep(200 * time.Millisecond)
			openBrowser(url)
		}()
	}

	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "Server failed: %v\n", err)
		os.Exit(1)
	}
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	htmlPath := filepath.Join("cmd", "roi_picker", "index.html")
	http.ServeFile(w, r, htmlPath)
}

func (s *server) captureFrame() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Capture frame directly from emulator via compound adb command
	cmd := exec.Command("adb", "-s", s.device, "shell", "getprop ro.build.type; screencap -p")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("screencap failed: %w", err)
	}

	raw := out.Bytes()
	// Strip any leading text from compound getprop command before PNG magic bytes
	idx := bytes.Index(raw, []byte("\x89PNG\r\n\x1a\n"))
	if idx < 0 {
		return nil, fmt.Errorf("no PNG header found in screencap output")
	}
	pngBytes := raw[idx:]

	cfg, err := png.DecodeConfig(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("invalid PNG header: %w", err)
	}

	s.frameW = cfg.Width
	s.frameH = cfg.Height
	s.lastPNG = make([]byte, len(pngBytes))
	copy(s.lastPNG, pngBytes)

	return s.lastPNG, nil
}

func (s *server) handleFrame(w http.ResponseWriter, r *http.Request) {
	pngBytes, err := s.captureFrame()
	if err != nil {
		http.Error(w, fmt.Sprintf("Capture failed: %v", err), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Width", fmt.Sprint(s.frameW))
	w.Header().Set("X-Frame-Height", fmt.Sprint(s.frameH))
	w.Write(pngBytes)
}

func (s *server) handleCrop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		X int `json:"x"`
		Y int `json:"y"`
		W int `json:"w"`
		H int `json:"h"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	rawBytes := s.lastPNG
	s.mu.Unlock()

	if len(rawBytes) == 0 {
		http.Error(w, "no frame captured yet", http.StatusNotFound)
		return
	}

	img, err := png.Decode(bytes.NewReader(rawBytes))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	b := img.Bounds()
	x1 := req.X
	y1 := req.Y
	x2 := req.X + req.W
	y2 := req.Y + req.H

	if x1 < 0 {
		x1 = 0
	}
	if y1 < 0 {
		y1 = 0
	}
	if x2 > b.Max.X {
		x2 = b.Max.X
	}
	if y2 > b.Max.Y {
		y2 = b.Max.Y
	}

	if x2 <= x1 || y2 <= y1 {
		http.Error(w, "invalid crop bounds", http.StatusBadRequest)
		return
	}

	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}

	if si, ok := img.(subImager); ok {
		sub := si.SubImage(image.Rect(x1, y1, x2, y2))
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		png.Encode(w, sub)
		return
	}

	http.Error(w, "crop unsupported", http.StatusInternalServerError)
}

func (s *server) handleTap(w http.ResponseWriter, r *http.Request) {
	var req struct {
		X int `json:"x"`
		Y int `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Use compound prefix to guarantee execution on BlueStacks macOS
	cmdStr := fmt.Sprintf("getprop ro.build.type; input tap %d %d", req.X, req.Y)
	cmd := exec.Command("adb", "-s", s.device, "shell", cmdStr)
	out, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf("tap failed: %v: %s", err, string(out)), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true,
		"x":  req.X,
		"y":  req.Y,
	})
}

func (s *server) handleSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ROIs []ROIItem `json:"rois"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.savedROIs = req.ROIs
	s.mu.Unlock()

	data, err := json.MarshalIndent(map[string]interface{}{
		"width":    s.frameW,
		"height":   s.frameH,
		"saved_at": time.Now().Format(time.RFC3339),
		"rois":     req.ROIs,
	}, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_ = os.MkdirAll(filepath.Dir(s.outputPath), 0o755)
	if err := os.WriteFile(s.outputPath, data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":   true,
		"path": s.outputPath,
	})
}

func (s *server) handleSaveTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		X    int    `json:"x"`
		Y    int    `json:"y"`
		W    int    `json:"w"`
		H    int    `json:"h"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	rawPNG := s.lastPNG
	s.mu.Unlock()

	if len(rawPNG) == 0 {
		http.Error(w, "no frame captured", http.StatusNotFound)
		return
	}

	img, err := png.Decode(bytes.NewReader(rawPNG))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	si, ok := img.(subImager)
	if !ok {
		http.Error(w, "cannot crop", http.StatusInternalServerError)
		return
	}

	crop := si.SubImage(image.Rect(req.X, req.Y, req.X+req.W, req.Y+req.H))
	destPath := filepath.Join("assets", "templates", req.Name+".png")
	_ = os.MkdirAll(filepath.Dir(destPath), 0o755)

	f, err := os.Create(destPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	if err := png.Encode(f, crop); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":   true,
		"path": destPath,
	})
}

func (s *server) handleTasks(w http.ResponseWriter, r *http.Request) {
	tasks := []TaskItem{
		{
			ID:          "troop_bar_slots",
			Title:       "Troop Bar Card Slots (0 to 8)",
			Description: "During an attack or replay, drag over each card in the bottom troop bar from left to right. We need the exact slot X centers.",
			Category:    "Slots",
			TargetFile:  "assets/manual_slots.json",
		},
		{
			ID:          "siege_slot",
			Title:       "Stone Slammer / Siege Slot",
			Description: "Drag over the Clan Castle / Siege Machine card on the troop bar (usually slot 2 or 3).",
			Category:    "Siege",
		},
		{
			ID:          "hero_slots",
			Title:       "Hero Cards & Ability Icons",
			Description: "Drag over Queen, Warden, Prince, and Duke hero slots on the bar.",
			Category:    "Heroes",
		},
		{
			ID:          "spell_slots",
			Title:       "Rage & Freeze Spell Cards",
			Description: "Drag over the Rage and Freeze spell slots at the right end of the bar.",
			Category:    "Spells",
		},
		{
			ID:          "continue_button",
			Title:       "Post-Battle 'Continue' or 'Return Home' Button",
			Description: "Drag over the green 'Continue' or 'Return Home' button on the victory/defeat report screen.",
			Category:    "Buttons",
			TargetFile:  "assets/continue_button.json",
		},
		{
			ID:          "attack_edge_line",
			Title:       "Preferred Deploy Line (P1 to P2)",
			Description: "Select two points or the bounding box along your preferred deployment edge outside the red line.",
			Category:    "Deploy",
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tasks)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	}
	if cmd != nil {
		_ = cmd.Start()
	}
}
