package main

import (
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"gocv.io/x/gocv"
)

// Reference dimensions from the bot's calibration system.
// All loot region coordinates are defined at this resolution
// and scaled to the device's actual resolution at runtime.
const (
	refWidth  = 860
	refHeight = 732
)

// lootTextROIs defines the screen regions containing each loot value
// on the attack search screen, in reference coordinates (860x732).
// These are fixed HUD positions that do not vary between bases.
//
// To recalibrate: open the saved full_screen.png in an image editor,
// note the pixel bounds of each loot value, convert back to 860x732:
//
//	refX = physicalX / (screenWidth / 860)
//	refY = physicalY / (screenHeight / 732)
//
// Then update these constants and re-run.
var lootTextROIs = []struct {
	name          string
	x1, y1, x2, y2 int
}{
	// DE position confirmed working (y=140-162 at ref = y=137-159 at 1280x720).
	// Gold and elixir are relative guesses above DE.
	{"gold",   15, 88, 300, 110},
	{"elixir", 15, 114, 300, 136},
	{"de",     15, 140, 300, 162},
}

type candidate struct {
	image  gocv.Mat
	rect   image.Rectangle
	source string
	index  int
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
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
		return fmt.Errorf("ADB connect failed: %w", err)
	}
	defer client.Close()

	fmt.Println("Capturing screen...")
	screen, err := client.CaptureToMat()
	if err != nil {
		return fmt.Errorf("capture failed: %w", err)
	}
	defer screen.Close()

	phyW, phyH := screen.Cols(), screen.Rows()
	if phyW < 720 || phyH < 600 {
		return fmt.Errorf("unexpected screen dimensions: %dx%d", phyW, phyH)
	}
	fmt.Printf("Screen: %dx%d\n", phyW, phyH)

	debugDir := filepath.Join("assets", "templates_captured")
	os.MkdirAll(debugDir, 0755)

	fullPath := filepath.Join(debugDir, "full_screen.png")
	if ok := gocv.IMWrite(fullPath, screen); !ok {
		return fmt.Errorf("failed to save full-screen debug image")
	}
	fmt.Printf("Saved: %s\n", fullPath)

	scaleX := float64(phyW) / float64(refWidth)
	scaleY := float64(phyH) / float64(refHeight)

	yOffset := flag.Int("y-offset", 0, "Additional vertical offset in ref pixels (positive = down)")
	fresh := flag.Bool("fresh", false, "Start fresh (overwrite existing templates)")
	flag.Parse()

	appliedOffsets := 0
	if *yOffset != 0 {
		appliedOffsets = *yOffset
		fmt.Printf("Applying Y-offset of %d ref pixels\n", appliedOffsets)
	}

	tplDir := filepath.Join("assets", "templates")
	os.MkdirAll(tplDir, 0755)

	collectedDir := filepath.Join(debugDir, "collected")
	os.RemoveAll(collectedDir)
	os.MkdirAll(collectedDir, 0755)

	var candidates []candidate

	for _, roi := range lootTextROIs {
		offY1 := roi.y1 + appliedOffsets
		offY2 := roi.y2 + appliedOffsets
		physRect := image.Rect(
			int(float64(roi.x1)*scaleX),
			int(float64(offY1)*scaleY),
			int(float64(roi.x2)*scaleX),
			int(float64(offY2)*scaleY),
		)
		physRect = clamp(physRect, phyW, phyH)

		fmt.Printf("ROI %-7s ref=(%3d,%3d,%3d,%3d)  phys=(%3d,%3d,%3d,%3d)\n",
			roi.name, roi.x1, offY1, roi.x2, offY2,
			physRect.Min.X, physRect.Min.Y, physRect.Max.X, physRect.Max.Y)

		region := screen.Region(physRect)

		colorPath := filepath.Join(debugDir, roi.name+"_color.png")
		gocv.IMWrite(colorPath, region)

		gray := gocv.NewMat()
		gocv.CvtColor(region, &gray, gocv.ColorBGRToGray)

		thresh := gocv.NewMat()
		gocv.Threshold(gray, &thresh, 140, 255, gocv.ThresholdBinary)

		kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(3, 3))
		gocv.MorphologyEx(thresh, &thresh, gocv.MorphClose, kernel)
		kernel.Close()

		debugPath := filepath.Join(debugDir, roi.name+"_region.png")
		gocv.IMWrite(debugPath, thresh)

		charH := physRect.Dy()

		segs := segmentProjection(thresh, charH)
		sort.Slice(segs, func(i, j int) bool {
			return segs[i].Min.X < segs[j].Min.X
		})

		for _, s := range segs {
			pad := charH / 6
			if pad < 1 {
				pad = 1
			}
			x1 := max(0, s.Min.X-pad)
			x2 := min(thresh.Cols(), s.Max.X+pad)
			padded := image.Rect(x1, 0, x2, thresh.Rows())

			digRoi := thresh.Region(padded)
			digitMat := digRoi.Clone()
			digRoi.Close()

			// Skip segments with too few actual pixels
			if countWhitePixels(digitMat) < charH {
				digitMat.Close()
				continue
			}

			candidates = append(candidates, candidate{
				image:  digitMat,
				rect:   padded,
				source: roi.name,
				index:  len(candidates),
			})

			candPath := filepath.Join(collectedDir,
				fmt.Sprintf("%02d_%s.png", len(candidates)-1, roi.name))
			gocv.IMWrite(candPath, digitMat)
		}

		region.Close()
		gray.Close()
		thresh.Close()
	}

	if len(candidates) == 0 {
		fmt.Printf("\nNo digit candidates found. Inspect debug files in %s/\n", debugDir)
		fmt.Println("  1. Open full_screen.png — verify you're on the scouting/search screen")
		fmt.Println("  2. Open *_color.png — verify the ROI contains a loot value")
		fmt.Println("  3. Open *_region.png — verify the thresholded region has visible text")
		fmt.Println("  4. If wrong, open full_screen.png in an editor to find loot pixel coords")
		fmt.Printf("  5. Convert to ref: refX = physX / %.3f, refY = physY / %.3f\n", scaleX, scaleY)
		fmt.Println("  6. Update lootTextROIs in this file and re-run")
		return fmt.Errorf("no digit candidates found")
	}

	fmt.Printf("\nFound %d digit candidates across %d loot regions.\n",
		len(candidates), len(lootTextROIs))
	fmt.Println("Label each digit shown below. Review images in",
		collectedDir+"/ if ASCII is unclear.")
	fmt.Println()

	labeled := make(map[int]gocv.Mat)

	if !*fresh {
		for d := 0; d <= 9; d++ {
			path := filepath.Join(tplDir, fmt.Sprintf("digit_%d.png", d))
			mat := gocv.IMRead(path, gocv.IMReadGrayScale)
			if !mat.Empty() {
				labeled[d] = mat
				fmt.Printf("Loaded existing digit_%d.png (%dx%d)\n", d, mat.Cols(), mat.Rows())
			}
		}
		if len(labeled) > 0 {
			fmt.Printf("Loaded %d existing templates. New labels add or replace. Use -fresh to start over.\n", len(labeled))
		}
	} else {
		fmt.Println("Fresh mode: existing templates will be overwritten.")
	}

labelingLoop:
	for i := range candidates {
		c := &candidates[i]

		fmt.Printf("--- Candidate %d/%d (source: %s, %dx%d px) ---\n",
			i+1, len(candidates), c.source, c.image.Cols(), c.image.Rows())

		for y := 0; y < c.image.Rows(); y++ {
			for x := 0; x < c.image.Cols(); x++ {
				if c.image.GetUCharAt(y, x) > 128 {
					fmt.Print("\u2588\u2588")
				} else {
					fmt.Print("  ")
				}
			}
			fmt.Println()
		}

		fmt.Print("Digit (0-9, s=skip, q=quit): ")
		var input string
		if _, err := fmt.Scanln(&input); err != nil {
			fmt.Println("(empty input, skipping)")
			continue
		}
		input = strings.TrimSpace(input)

		switch input {
		case "q":
			break labelingLoop
		case "s":
			continue
		default:
			d, err := strconv.Atoi(input)
			if err != nil || d < 0 || d > 9 {
				fmt.Println("  Invalid. Enter 0-9, s=skip, q=quit.")
				i--
				continue
			}

			if existing, exists := labeled[d]; exists {
				fmt.Printf("  Already have digit %d (%dx%d). Replace? (y/N): ",
					d, existing.Cols(), existing.Rows())
				var replace string
				fmt.Scanln(&replace)
				if strings.ToLower(strings.TrimSpace(replace)) != "y" {
					continue
				}
				existing.Close()
			}

			labeled[d] = c.image.Clone()
			fmt.Printf("  -> digit %d saved\n", d)
		}
	}

	fmt.Println("\n--- Summary ---")

	var saved int
	for d := 0; d <= 9; d++ {
		tpl, ok := labeled[d]
		path := filepath.Join(tplDir, fmt.Sprintf("digit_%d.png", d))
		if ok {
			if gocv.IMWrite(path, tpl) {
				fmt.Printf("  digit_%d.png  (%dx%d)  OK\n", d, tpl.Cols(), tpl.Rows())
				saved++
			} else {
				fmt.Printf("  digit_%d.png  WRITE FAILED\n", d)
			}
		} else {
			fmt.Printf("  digit_%d.png  MISSING\n", d)
		}
	}

	fmt.Printf("\nSaved %d/10 digit templates to %s/\n", saved, tplDir)
	if saved < 10 {
		fmt.Printf("NOTE: missing %d digits. Run again on a different base with -append.\n", 10-saved)
	}

	for _, m := range labeled {
		m.Close()
	}
	for _, c := range candidates {
		c.image.Close()
	}

	fmt.Println("Done.")
	return nil
}

func segmentProjection(thresh gocv.Mat, charH int) []image.Rectangle {
	// Column projection: count white pixels per column
	counts := make([]int, thresh.Cols())
	for x := 0; x < thresh.Cols(); x++ {
		for y := 0; y < thresh.Rows(); y++ {
			if thresh.GetUCharAt(y, x) > 128 {
				counts[x]++
			}
		}
	}

	// Mark columns that exceed threshold
	minCount := 1
	marked := make([]bool, thresh.Cols())
	for x := 0; x < thresh.Cols(); x++ {
		marked[x] = counts[x] >= minCount
	}

	// Find contiguous runs
	var segs []image.Rectangle
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
		segs = append(segs, image.Rect(start, 0, i, thresh.Rows()))
	}

	// Merge runs closer than 3px apart
	if len(segs) == 0 {
		return segs
	}
	merged := []image.Rectangle{segs[0]}
	for _, s := range segs[1:] {
		last := &merged[len(merged)-1]
		if s.Min.X-last.Max.X < 3 {
			last.Max.X = s.Max.X
		} else {
			merged = append(merged, s)
		}
	}

	// Filter out very narrow runs (noise)
	var filtered []image.Rectangle
	for _, s := range merged {
		if s.Dx() >= 1 {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

func countWhitePixels(mat gocv.Mat) int {
	n := 0
	for y := 0; y < mat.Rows(); y++ {
		for x := 0; x < mat.Cols(); x++ {
			if mat.GetUCharAt(y, x) > 128 {
				n++
			}
		}
	}
	return n
}

func clamp(r image.Rectangle, w, h int) image.Rectangle {
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
	return r
}
