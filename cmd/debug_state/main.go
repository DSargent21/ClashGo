package main

import (
	"fmt"
	"math"
	"os"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/vision"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	zerolog.TimeFieldFormat = time.RFC3339
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).With().Timestamp().Logger()

	cfg := config.LoadOrDefault("config.json")
	
	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
		adb.WithTimeout(15*time.Second),
	)
	client.DeviceID = cfg.Device.DeviceID

	fmt.Printf("🔍 Diagnosing Game State Detection (Device: %s)...\n", client.DeviceID)

	if err := client.Connect(); err != nil {
		fmt.Printf("❌ Connection failed: %v\n", err)
		os.Exit(1)
	}

	var screen gocv.Mat
	var err error
	for i := 0; i < 3; i++ {
		screen, err = client.CaptureToMat()
		if err == nil {
			break
		}
		fmt.Printf("⚠️  Capture attempt %d failed: %v, retrying...\n", i+1, err)
		time.Sleep(1 * time.Second)
	}

	if err != nil {
		fmt.Printf("❌ Capture failed after 3 attempts: %v\n", err)
		os.Exit(1)
	}
	defer screen.Close()

	w, h := screen.Cols(), screen.Rows()
	fmt.Printf("📸 Captured screen: %dx%d (Aspect Ratio: %.2f)\n", w, h, float64(w)/float64(h))

	// Normalization check
	norm := vision.ResizeToHeight(screen, 732)
	defer norm.Close()
	fmt.Printf("📏 Normalized screen: %dx%d\n", norm.Cols(), norm.Rows())

	cal := &game.Calibration{
		PhysicalW: w, PhysicalH: h,
		ScaleX: float64(w) / 860.0,
		ScaleY: float64(h) / 732.0,
	}

	classifier := game.NewClassifier(cal, game.DefaultClassifierConfig(), logger)
	rules := classifier.GetRules() // Need to add this method

	fmt.Println("\n--- Rule Breakdown ---")
	for _, rule := range rules {
		passed := 0
		fmt.Printf("[%s] %s\n", rule.State.String(), rule.Desc)
		for i, chk := range rule.Checks {
			sx, sy := chk.X, chk.Y
			if sx >= norm.Cols() || sy >= norm.Rows() {
				fmt.Printf("  %d: Out of bounds (%d, %d)\n", i, sx, sy)
				continue
			}

			b := norm.GetUCharAt(sy, sx*3)
			g := norm.GetUCharAt(sy, sx*3+1)
			r := norm.GetUCharAt(sy, sx*3+2)

			dr := absDiff(int(r), int(chk.R))
			dg := absDiff(int(g), int(chk.G))
			db := absDiff(int(b), int(chk.B))
			dist := math.Sqrt(float64(dr*dr + dg*dg + db*db))

			status := "❌"
			if dist <= float64(chk.Tolerance) {
				status = "✅"
				passed++
			}

			fmt.Printf("  %d: %s Pos(%d, %d) Found RGB(%d, %d, %d) Want RGB(%d, %d, %d) Dist=%.1f Tol=%d\n",
				i, status, sx, sy, r, g, b, chk.R, chk.G, chk.B, dist, chk.Tolerance)
		}
		
		totalStatus := "FAIL"
		if passed >= rule.MinPass {
			totalStatus = "PASS"
		}
		fmt.Printf("  RESULT: %s (%d/%d passed, min=%d)\n\n", totalStatus, passed, len(rule.Checks), rule.MinPass)
	}

	debugPath := "debug_screen.png"
	gocv.IMWrite(debugPath, norm)
	fmt.Printf("💾 Saved normalized screen to %s for inspection.\n", debugPath)
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
