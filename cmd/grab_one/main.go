package main

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"gocv.io/x/gocv"
)

const (
	refWidth  = 860
	refHeight = 732
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = "emulator-5554"

	fmt.Println("Connecting to ADB...")
	if err := client.Connect(); err != nil {
		return fmt.Errorf("ADB: %w", err)
	}
	defer client.Close()

	fmt.Println("Capturing screen...")
	screen, err := client.CaptureToMat()
	if err != nil {
		return fmt.Errorf("capture: %w", err)
	}
	defer screen.Close()

	phyW, phyH := screen.Cols(), screen.Rows()
	fmt.Printf("Screen: %dx%d\n", phyW, phyH)
	scaleX := float64(phyW) / float64(refWidth)
	scaleY := float64(phyH) / float64(refHeight)

	os.MkdirAll("assets/grab", 0755)
	gocv.IMWrite("assets/grab/full_screen.png", screen)
	fmt.Println("Saved: assets/grab/full_screen.png")

	// Use the original working DE ROI: ref=(15,140,300,162)
	deROI := image.Rect(
		int(float64(15)*scaleX),
		int(float64(140)*scaleY),
		int(float64(300)*scaleX),
		int(float64(162)*scaleY),
	)
	deROI = clamp(deROI, phyW, phyH)
	fmt.Printf("DE ROI: (%d,%d)-(%d,%d)\n", deROI.Min.X, deROI.Min.Y, deROI.Max.X, deROI.Max.Y)

	region := screen.Region(deROI)
	defer region.Close()

	// Standard preprocessing (same as original working LootRecognizer)
	gray := gocv.NewMat()
	gocv.CvtColor(region, &gray, gocv.ColorBGRToGray)

	thresh := gocv.NewMat()
	gocv.Threshold(gray, &thresh, 160, 255, gocv.ThresholdBinary)

	// Use original 2x2 kernel (not 3x3 which bridges too much)
	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(2, 2))
	gocv.MorphologyEx(thresh, &thresh, gocv.MorphClose, kernel)
	kernel.Close()

	gocv.IMWrite("assets/grab/de_region.png", thresh)

	// Segment by vertical projection with reasonable threshold
	charH := deROI.Dy()
	counts := make([]int, thresh.Cols())
	for x := 0; x < thresh.Cols(); x++ {
		for y := 0; y < thresh.Rows(); y++ {
			if thresh.GetUCharAt(y, x) > 128 {
				counts[x]++
			}
		}
	}

	minCount := charH / 5
	if minCount < 3 {
		minCount = 3
	}
	marked := make([]bool, thresh.Cols())
	for x := 0; x < thresh.Cols(); x++ {
		marked[x] = counts[x] >= minCount
	}

	var segRects []image.Rectangle
	i := 0
	for i < thresh.Cols() {
		if !marked[i] {
			i++
			continue
		}
		start := i
		for i < thresh.Cols() && marked[i] {
			i++
		}
		segRects = append(segRects, image.Rect(start, 0, i, thresh.Rows()))
	}

	if len(segRects) == 0 {
		segRects = append(segRects, image.Rect(0, 0, thresh.Cols(), thresh.Rows()))
	}

	// Merge close
	merged := []image.Rectangle{segRects[0]}
	for _, s := range segRects[1:] {
		last := &merged[len(merged)-1]
		if s.Min.X-last.Max.X < 3 {
			last.Max.X = s.Max.X
		} else {
			merged = append(merged, s)
		}
	}

	fmt.Printf("\nFound %d segments in DE value area:\n", len(merged))

	type segInfo struct {
		index   int
		rect    image.Rectangle
		cropped gocv.Mat
	}
	var segments []segInfo

	for idx, s := range merged {
		if s.Dx() < 3 {
			continue
		}
		pad := 2
		x1 := max(0, s.Min.X-pad)
		x2 := min(thresh.Cols(), s.Max.X+pad)
		crop := thresh.Region(image.Rect(x1, 0, x2, thresh.Rows()))
		mat := crop.Clone()
		crop.Close()

		segments = append(segments, segInfo{index: idx, rect: s, cropped: mat})

		fmt.Printf("\n--- Segment %d (cols %d-%d, %dx%d) ---\n", idx, s.Min.X, s.Max.X, mat.Cols(), mat.Rows())
		for y := 0; y < mat.Rows(); y++ {
			for x := 0; x < mat.Cols(); x++ {
				if mat.GetUCharAt(y, x) > 128 {
					fmt.Print("\u2588\u2588")
				} else {
					fmt.Print("  ")
				}
			}
			fmt.Println()
		}
	}

	if len(segments) == 0 {
		return fmt.Errorf("no segments found")
	}

	fmt.Println("\nOne of the segments above IS the digit '1'.")
	fmt.Println("Look at the ASCII art and find which one (by index number).")

	for {
		fmt.Print("\nEnter the index of the '1' digit (or 'q' to quit): ")
		var input string
		fmt.Scanln(&input)
		input = strings.TrimSpace(input)
		if input == "q" {
			return nil
		}
		idx, err := strconv.Atoi(input)
		if err != nil {
			fmt.Println("  Invalid. Enter a number.")
			continue
		}
		var found *segInfo
		for i, s := range segments {
			if s.index == idx {
				found = &segments[i]
				break
			}
		}
		if found == nil {
			fmt.Printf("  No segment with index %d. Valid indices: ", idx)
			for _, s := range segments {
				fmt.Printf("%d ", s.index)
			}
			fmt.Println()
			continue
		}
		fmt.Printf("  Selected segment %d (%dx%d). Confirm this is digit '1'? (y/N): ",
			idx, found.cropped.Cols(), found.cropped.Rows())
		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(strings.TrimSpace(confirm)) == "y" {
			os.MkdirAll("assets/templates", 0755)
			path := filepath.Join("assets/templates", "digit_1.png")
			if gocv.IMWrite(path, found.cropped) {
				fmt.Printf("\nSaved digit_1.png (%dx%d) to assets/templates/\n", found.cropped.Cols(), found.cropped.Rows())
			}
			break
		}
		fmt.Println("  Not the '1'. Try another.")
	}

	for _, s := range segments {
		s.cropped.Close()
	}

	fmt.Println("\nDone. Now run: go run cmd/verify_loot/main.go")
	return nil
}

func clamp(r image.Rectangle, w, h int) image.Rectangle {
	if r.Min.X < 0 { r.Min.X = 0 }
	if r.Min.Y < 0 { r.Min.Y = 0 }
	if r.Max.X > w { r.Max.X = w }
	if r.Max.Y > h { r.Max.Y = h }
	return r
}
