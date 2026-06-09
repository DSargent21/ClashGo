package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/vision"
	"gocv.io/x/gocv"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	cmd := os.Args[1]
	cfg := config.DefaultConfig()

	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
	)
	client.DeviceID = cfg.Device.DeviceID

	if err := client.Connect(); err != nil {
		fmt.Printf("ADB Connection Error: %v\n", err)
		return
	}
	defer client.Close()

	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		fmt.Printf("Calibration failed: %v\n", err)
		return
	}

	screen, err := client.CaptureToMat()
	if err != nil {
		fmt.Printf("Capture failed: %v\n", err)
		return
	}
	defer screen.Close()

	switch cmd {
	case "match":
		if len(os.Args) < 3 {
			fmt.Println("Usage: go run scripts/pinpoint_helper.go match <template_name>")
			return
		}
		tplName := os.Args[2]
		ts, err := game.NewTemplateStore("assets/templates")
		if err != nil {
			fmt.Printf("Failed to create template store: %v\n", err)
			return
		}
		ts.LoadTemplates()

		tpl, ok := ts.Get(tplName)
		if !ok {
			fmt.Printf("Template '%s' not found in assets/templates.\n", tplName)
			return
		}

		fmt.Printf("Matching template '%s' (size: %dx%d) on screen...\n", tplName, tpl.Cols(), tpl.Rows())
		matches, _ := vision.MatchMultiScale(screen, tpl, 0.5, 1.5, 20, 0.40)

		if len(matches) == 0 {
			fmt.Println("No matches found above threshold 0.40.")
			return
		}

		for idx, m := range matches {
			refX, refY := cal.Unscale(m.Point.X, m.Point.Y)
			fmt.Printf("[%d] Confidence: %.3f\n", idx, m.Confidence)
			fmt.Printf("    Physical:  (%d, %d)\n", m.Point.X, m.Point.Y)
			fmt.Printf("    Reference: (%d, %d)\n", refX, refY)
		}

	case "color":
		if len(os.Args) < 4 {
			fmt.Println("Usage: go run scripts/pinpoint_helper.go color <x> <y> [is_ref_coord: true/false]")
			return
		}
		x, _ := strconv.Atoi(os.Args[2])
		y, _ := strconv.Atoi(os.Args[3])
		isRef := true
		if len(os.Args) >= 5 {
			isRef, _ = strconv.ParseBool(os.Args[4])
		}

		px, py := x, y
		if isRef {
			px, py = cal.ScaleRef(x, y)
		}

		if px < 0 || px >= screen.Cols() || py < 0 || py >= screen.Rows() {
			fmt.Printf("Coordinates (%d, %d) out of screen bounds (%dx%d).\n", px, py, screen.Cols(), screen.Rows())
			return
		}

		// Sample a 5x5 region around the point to print average colors
		bgr := screen.GetVecbAt(py, px)
		fmt.Printf("Pixel Color at Physical (%d, %d) [Ref: (%d, %d)]:\n", px, py, x, y)
		fmt.Printf("  BGR: (%d, %d, %d)\n", bgr[0], bgr[1], bgr[2])
		fmt.Printf("  RGB: (%d, %d, %d)\n", bgr[2], bgr[1], bgr[0])

	case "tap":
		if len(os.Args) < 4 {
			fmt.Println("Usage: go run scripts/pinpoint_helper.go tap <x> <y> [is_ref_coord: true/false]")
			return
		}
		x, _ := strconv.Atoi(os.Args[2])
		y, _ := strconv.Atoi(os.Args[3])
		isRef := true
		if len(os.Args) >= 5 {
			isRef, _ = strconv.ParseBool(os.Args[4])
		}

		px, py := x, y
		if isRef {
			px, py = cal.ScaleRef(x, y)
		}

		fmt.Printf("Tapping screen at physical coordinates (%d, %d) [Ref: (%d, %d)]...\n", px, py, x, y)
		if err := client.Tap(px, py); err != nil {
			fmt.Printf("Tap failed: %v\n", err)
			return
		}
		time.Sleep(1000 * time.Millisecond)

		afterScreen, err := client.CaptureToMat()
		if err == nil {
			gocv.IMWrite("after_tap.png", afterScreen)
			afterScreen.Close()
			fmt.Println("Screenshot after tap saved to after_tap.png")
		}

	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Println("Vanguard Pinpoint & Diagnostic Helper")
	fmt.Println("Commands:")
	fmt.Println("  go run scripts/pinpoint_helper.go match <template_name>")
	fmt.Println("  go run scripts/pinpoint_helper.go color <x> <y> [is_ref_coord: true/false]")
	fmt.Println("  go run scripts/pinpoint_helper.go tap <x> <y> [is_ref_coord: true/false]")
}
