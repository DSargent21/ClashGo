package main

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"sort"
	"time"

	"github.com/diegosargent/coc-bot/internal/adb"
	"gocv.io/x/gocv"
)

func main() {
	client := adb.NewClient(adb.WithHost("127.0.0.1"), adb.WithPort(5037), adb.WithTimeout(30*time.Second))
	client.DeviceID = "emulator-5554"
	if err := client.Connect(); err != nil {
		fmt.Println("ADB fail:", err)
		os.Exit(1)
	}
	mat, err := client.CaptureToMat()
	if err != nil {
		fmt.Println("Capture fail:", err)
		os.Exit(1)
	}
	defer mat.Close()
	fmt.Printf("Screen: %dx%d\n", mat.Cols(), mat.Rows())

	// Test both old and captured templates
	type tplDef struct {
		label string
		path  string
	}
	templates := []tplDef{
		{"ORIGINAL btn_attack (from portrait)", "assets/templates/btn_attack.png"},
		{"CAPTURED btn_attack (from landscape)", "assets/templates_captured/btn_attack.png"},
	}

	for _, t := range templates {
		tpl := gocv.IMRead(t.path, gocv.IMReadColor)
		if tpl.Empty() {
			fmt.Printf("\n%s: FAILED to load\n", t.label)
			continue
		}

		fmt.Printf("\n--- %s ---\n", t.label)
		fmt.Printf("  Template size: %dx%d\n", tpl.Cols(), tpl.Rows())

		// Try many scales
		type match struct {
			conf  float64
			scale float64
			x, y  int
		}
		var all []match
		for s := 0.2; s <= 2.5; s += 0.05 {
			w := int(float64(tpl.Cols()) * s)
			h := int(float64(tpl.Rows()) * s)
			if w < 10 || h < 10 || w > mat.Cols() || h > mat.Rows() {
				continue
			}
			resized := gocv.NewMat()
			gocv.Resize(tpl, &resized, image.Point{X: w, Y: h}, 0, 0, gocv.InterpolationLinear)

			result := gocv.NewMat()
			gocv.MatchTemplate(mat, resized, &result, gocv.TmCcoeffNormed, gocv.NewMat())

			_, maxVal, _, maxLoc := gocv.MinMaxLoc(result)
			if maxVal >= 0.4 {
				all = append(all, match{
					conf:  float64(maxVal),
					scale: s,
					x:     maxLoc.X + w/2,
					y:     maxLoc.Y + h/2,
				})
			}
			resized.Close()
			result.Close()
		}
		tpl.Close()

		sort.Slice(all, func(i, j int) bool { return all[i].conf > all[j].conf })

		if len(all) == 0 {
			fmt.Println("  No matches >= 0.4")
		} else {
			fmt.Printf("  Top 5 matches:\n")
			for i := 0; i < len(all) && i < 5; i++ {
				fmt.Printf("    conf=%.4f scale=%.2f at (%d,%d)\n",
					all[i].conf, all[i].scale, all[i].x, all[i].y)
			}
		}
	}

	// Visualize attack button location
	fmt.Println("\n--- Bottom-left area scan (where attack button should be) ---")
	for y := 540; y < 720; y += 20 {
		b := mat.GetUCharAt(y, 50*3)
		g := mat.GetUCharAt(y, 50*3+1)
		r := mat.GetUCharAt(y, 50*3+2)
		fmt.Printf("  y=%3d at x=50: RGB(%3d,%3d,%3d)\n", y, r, g, b)
	}

	// Try template matching on a cropped bottom-left region
	fmt.Println("\n--- Template match on bottom-left region only (0,450 to 400,720) ---")
	region := mat.Region(image.Rect(0, 450, 400, 720))
	defer region.Close()

	tpl := gocv.IMRead("assets/templates/btn_attack.png", gocv.IMReadColor)
	if !tpl.Empty() {
		defer tpl.Close()
		fmt.Printf("  Region: %dx%d, Template: %dx%d\n", region.Cols(), region.Rows(), tpl.Cols(), tpl.Rows())

		bestConf := 0.0
		bestScale := 0.0
		bestPt := image.Point{}
		for s := 0.3; s <= 1.5; s += 0.05 {
			w := int(float64(tpl.Cols()) * s)
			h := int(float64(tpl.Rows()) * s)
			if w < 10 || h < 10 || w > region.Cols() || h > region.Rows() {
				continue
			}
			resized := gocv.NewMat()
			gocv.Resize(tpl, &resized, image.Point{X: w, Y: h}, 0, 0, gocv.InterpolationLinear)
			result := gocv.NewMat()
			gocv.MatchTemplate(region, resized, &result, gocv.TmCcoeffNormed, gocv.NewMat())
			_, maxVal, _, maxLoc := gocv.MinMaxLoc(result)
			if maxVal > float32(bestConf) {
				bestConf = float64(maxVal)
				bestScale = s
				bestPt = image.Pt(maxLoc.X+w/2, maxLoc.Y+h/2)
			}
			resized.Close()
			result.Close()
		}
		fmt.Printf("  Best in region: conf=%.4f scale=%.2f at region(%d,%d) -> screen(%d,%d)\n",
			bestConf, bestScale, bestPt.X, bestPt.Y, bestPt.X, bestPt.Y+450)
	}

	// Save visualization
	vis := mat.Clone()
	defer vis.Close()
	gocv.Rectangle(&vis, image.Rect(0, 450, 400, 720), color.RGBA{0, 255, 255, 255}, 2)
	gocv.IMWrite("/tmp/screen_region.png", vis)
}
