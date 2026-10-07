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
	"strconv"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/paths"
	"gocv.io/x/gocv"
)

type slotItem struct {
	X    int    `json:"x"`
	Y    int    `json:"y"`
	W    int    `json:"w"`
	H    int    `json:"h"`
	Name string `json:"name"`
}

type armySlotsData struct {
	Width         int                 `json:"width"`
	Height        int                 `json:"height"`
	DropdownArrow struct {
		X int `json:"x"`
		Y int `json:"y"`
	} `json:"dropdown_arrow"`
	Slots map[string]slotItem `json:"slots"`
}

type server struct {
	device     string
	configPath string
	data       armySlotsData
	liveW      int
	liveH      int
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8793", "address to serve the picker on")
	device := flag.String("device", "localhost:5555", "ADB device")
	configPath := flag.String("config", "", "path to army_slots.json")
	open := flag.Bool("open", true, "open browser automatically")
	flag.Parse()

	resolvedPath := *configPath
	if resolvedPath == "" {
		resolvedPath = paths.Resolve("army_slots.json")
	}

	s := &server{
		device:     *device,
		configPath: resolvedPath,
	}
	s.loadConfig()

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/state", s.handleState)
	mux.HandleFunc("/frame.png", s.handleFrame)
	mux.HandleFunc("/save", s.handleSave)
	mux.HandleFunc("/tap", s.handleTap)
	mux.HandleFunc("/open_dropdown", s.handleOpenDropdown)

	url := "http://" + *addr + "/"
	fmt.Printf("pick_army_slots: config=%s\n", s.configPath)
	fmt.Printf("pick_army_slots: serving on %s\n", url)
	if *open {
		openBrowser(url)
	}
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatalf("pick_army_slots: %v", err)
	}
}

func (s *server) loadConfig() {
	s.data = armySlotsData{
		Width:  1280,
		Height: 720,
		DropdownArrow: struct {
			X int `json:"x"`
			Y int `json:"y"`
		}{X: 742, Y: 71},
		Slots: map[string]slotItem{
			"1": {X: 740, Y: 150, W: 417, H: 55, Name: "Slot 1 (Imported Army 3)"},
			"2": {X: 740, Y: 222, W: 417, H: 55, Name: "Slot 2 (Army 2)"},
			"3": {X: 740, Y: 293, W: 417, H: 55, Name: "Slot 3 (Imported Army 3)"},
			"4": {X: 740, Y: 366, W: 417, H: 55, Name: "Slot 4 (Imported Army 4)"},
		},
	}
	raw, err := os.ReadFile(s.configPath)
	if err == nil {
		_ = json.Unmarshal(raw, &s.data)
	}
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, indexHTML)
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	s.loadConfig()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.data)
}

func (s *server) handleFrame(w http.ResponseWriter, r *http.Request) {
	client := adb.NewClient(adb.WithHost("127.0.0.1"), adb.WithPort(5037), adb.WithTimeout(15*time.Second))
	client.DeviceID = s.device
	defer client.Close()

	mat, err := client.CaptureToMat()
	if err != nil || mat.Empty() {
		http.Error(w, "capture failed: "+fmt.Sprint(err), http.StatusBadGateway)
		return
	}
	defer mat.Close()

	s.liveW, s.liveH = mat.Cols(), mat.Rows()
	buf, err := gocv.IMEncode(gocv.PNGFileExt, mat)
	if err != nil {
		http.Error(w, "encode failed: "+fmt.Sprint(err), http.StatusInternalServerError)
		return
	}
	defer buf.Close()

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf.GetBytes())
}

func (s *server) handleSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var in armySlotsData
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.Width <= 0 {
		in.Width = 1280
	}
	if in.Height <= 0 {
		in.Height = 720
	}
	s.data = in

	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		http.Error(w, "marshal failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_ = os.MkdirAll(filepath.Dir(s.configPath), 0755)
	if err := os.WriteFile(s.configPath, append(raw, '\n'), 0644); err != nil {
		http.Error(w, "write failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (s *server) handleTap(w http.ResponseWriter, r *http.Request) {
	xStr := r.URL.Query().Get("x")
	yStr := r.URL.Query().Get("y")
	x, _ := strconv.Atoi(xStr)
	y, _ := strconv.Atoi(yStr)

	client := adb.NewClient(adb.WithHost("127.0.0.1"), adb.WithPort(5037), adb.WithTimeout(15*time.Second))
	client.DeviceID = s.device
	defer client.Close()

	if err := client.Tap(x, y); err != nil {
		http.Error(w, "tap failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

func (s *server) handleOpenDropdown(w http.ResponseWriter, r *http.Request) {
	client := adb.NewClient(adb.WithHost("127.0.0.1"), adb.WithPort(5037), adb.WithTimeout(30*time.Second))
	client.DeviceID = s.device
	defer client.Close()

	// Tap attack
	_ = client.Tap(83, 634)
	time.Sleep(1200 * time.Millisecond)
	// Tap find match
	_ = client.Tap(205, 534)
	time.Sleep(1200 * time.Millisecond)
	// Tap dropdown arrow
	arrowX := s.data.DropdownArrow.X
	arrowY := s.data.DropdownArrow.Y
	if arrowX == 0 {
		arrowX, arrowY = 742, 71
	}
	_ = client.Tap(arrowX, arrowY)
	time.Sleep(1000 * time.Millisecond)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
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
