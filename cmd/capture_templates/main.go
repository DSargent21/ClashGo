package main

import (
	"fmt"
	"image"
	"os"
	"time"

	"github.com/diegosargent/coc-bot/internal/adb"
	"gocv.io/x/gocv"
)

func main() {
	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = "emulator-5554"

	if err := client.Connect(); err != nil {
		fmt.Println("ADB connect failed:", err)
		os.Exit(1)
	}

	mat, err := client.CaptureToMat()
	if err != nil {
		fmt.Println("Capture failed:", err)
		os.Exit(1)
	}
	defer mat.Close()

	w, h := mat.Cols(), mat.Rows()
	fmt.Printf("Screen: %dx%d\n", w, h)

	// Reference: 860x732
	refW, refH := 860, 732
	scaleX := float64(w) / float64(refW)
	scaleY := float64(h) / float64(refH)
	fmt.Printf("Scale: %.3fx %.3fy\n", scaleX, scaleY)

	// Button regions in reference coordinates (x1,y1,x2,y2)
	// These are approximate — we capture generous areas
	buttons := []struct {
		name   string
		x1, y1 int // reference coords, top-left
		x2, y2 int // reference coords, bottom-right
	}{
		{"btn_attack", 0, 500, 200, 732},       // bottom-left: orange attack button
		{"btn_find_match", 100, 480, 500, 732}, // bottom: yellow find match
		{"btn_army_arrow", 20, 570, 130, 680},  // bottom-left: army selector arrow
		{"btn_army_1", 130, 570, 350, 680},     // bottom: army 1 button
		{"btn_battle", 350, 480, 700, 732},     // bottom-right: green battle
		{"btn_next", 600, 480, 860, 732},       // bottom-right: next button
	}

	outDir := "assets/templates_captured"
	os.MkdirAll(outDir, 0755)

	for _, btn := range buttons {
		// Scale to physical coords
		r := image.Rect(
			int(float64(btn.x1)*scaleX),
			int(float64(btn.y1)*scaleY),
			int(float64(btn.x2)*scaleX),
			int(float64(btn.y2)*scaleY),
		)

		// Clamp to screen bounds
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

		region := mat.Region(r)
		path := fmt.Sprintf("%s/%s.png", outDir, btn.name)
		if ok := gocv.IMWrite(path, region); !ok {
			fmt.Printf("FAILED to save %s\n", path)
		} else {
			fmt.Printf("Saved %s  (region %d,%d %dx%d)\n", path, r.Min.X, r.Min.Y, r.Dx(), r.Dy())
		}
		region.Close()
	}

	fmt.Printf("\nTemplates saved to %s/\n", outDir)
	fmt.Println("Review each file:")
	fmt.Println("  If a region looks like the correct button -> copy to assets/templates/")
	fmt.Println("  If it's wrong -> adjust coordinates in this script and re-run")
}
