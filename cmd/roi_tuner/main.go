package main

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"gocv.io/x/gocv"
)

var templates [][]gocv.Mat

type resource struct {
	name                                    string
	x1, y1, x2, y2                          int
	want                                     int
	got                                      int
}

var resources = []*resource{
	{name: "Gold",   x1: 46, y1: 72,  x2: 260, y2: 94,  want: 443168},
	{name: "Elixir", x1: 46, y1: 102, x2: 420, y2: 130, want: 434311},
	{name: "DE",     x1: 46, y1: 132, x2: 220, y2: 160, want: 4822},
}

var selected = 0
var fixedPaths = []string{
	"screen_20260515_215234.png",
	"screen_20260515_215242.png",
	"screen_20260515_215252.png",
	"screen_20260515_215301.png",
	"screen_20260515_215309.png",
	"screen_20260515_215316.png",
}
var activeImg = 0
var cycleMode = false

func main() {
	templates = loadDigitTemplates()

	if len(os.Args) > 1 {
		if os.Args[1] == "--cycle" {
			cycleMode = true
		} else if idx, err := strconv.Atoi(os.Args[1]); err == nil && idx >= 0 && idx < len(fixedPaths) {
			activeImg = idx
			cycleMode = true
		}
	}

	// Set initial wants based on first image
	setWants(activeImg)

	window := gocv.NewWindow("ROI Tuner")
	defer window.Close()

	img := loadImage(activeImg)
	if img.Empty() {
		return
	}
	defer img.Close()

	fmt.Println("=== ROI Tuner ===")
	fmt.Println("Keys:")
	fmt.Println("  1/2/3 or g/e/d : select Gold/Elixir/DE")
	fmt.Println("  j/k            : previous/next image (cycle mode)")
	fmt.Println("  s              : save ROIs (drag-select)")
	fmt.Println("  S (shift+s)    : show current ROI stats")
	fmt.Println("  q              : quit")
	fmt.Println()

	w := img.Cols()
	h := img.Rows()
	window.ResizeWindow(w, h)

	window.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		// Optional: show coordinates on status bar
	}, nil)

	run := true
	for run {
		if cycleMode {
			newIdx := activeImg
			_ = newIdx
		}

		display := img.Clone()
		gray := gocv.NewMat()
		gocv.CvtColor(img, &gray, gocv.ColorBGRToGray)

		yPos := 20
		for i, r := range resources {
			rect := image.Rect(r.x1, r.y1, r.x2, r.y2)
			clr := color.RGBA{0, 255, 0, 255}
			if i == selected {
				clr = color.RGBA{255, 0, 0, 255}
			}
			gocv.Rectangle(&display, rect, clr, 2)

			region := gray.Region(rect)
			r.got, _, _ = analyzeROI(region, rect)
			region.Close()

			ok := "FAIL"
			if r.got == r.want {
				ok = "OK"
			}
			sel := "  "
			if i == selected {
				sel = "->"
			}
			prefix := string(r.name[0])
			line := fmt.Sprintf("%s %s: (%3d,%3d)-(%3d,%3d) [%3dx%2d] got=%-10d want=%-10d %s",
				sel, prefix, r.x1, r.y1, r.x2, r.y2, r.x2-r.x1, r.y2-r.y1, r.got, r.want, ok)
			gocv.PutText(&display, line, image.Pt(10, yPos), gocv.FontHersheyPlain, 1.0, clr, 1)
			yPos += 18
		}

		imgName := "custom"
		if cycleMode {
			imgName = fmt.Sprintf("%s [%d/%d]", filepath.Base(fixedPaths[activeImg]), activeImg+1, len(fixedPaths))
		}
		statusLine := fmt.Sprintf("Image: %s | Sel: %s | j/k img, 1/2/3 res, s=select ROI, q=quit",
			imgName, resources[selected].name)
		gocv.PutText(&display, statusLine, image.Pt(10, img.Rows()-10),
			gocv.FontHersheyPlain, 0.8, color.RGBA{255, 255, 0, 255}, 1)

		window.IMShow(display)

		// Only free the clone, keep gray alive for mouse handler if needed
		display.Close()

		key := window.WaitKey(50)
		gray.Close()

		switch key {
		case 'q':
			run = false
		case 's':
			// Use SelectROI for drag-and-drop
			selROI := window.SelectROI(img)
			if selROI.Dx() > 0 && selROI.Dy() > 0 {
				r := resources[selected]
				r.x1 = selROI.Min.X
				r.y1 = selROI.Min.Y
				r.x2 = selROI.Max.X
				r.y2 = selROI.Max.Y
				fmt.Printf("%s ROI set to (%d,%d)-(%d,%d) [%dx%d]\n",
					r.name, r.x1, r.y1, r.x2, r.y2, r.x2-r.x1, r.y2-r.y1)
			}
		case 'S': // uppercase S
			fmt.Println("\n=== Current ROI Configuration ===")
			for _, r := range resources {
				rect := image.Rect(r.x1, r.y1, r.x2, r.y2)
				r.got, _, _ = analyzeROI(img.Region(rect), rect)
				fmt.Printf("  %s: (%d, %d, %d, %d) got=%d want=%d\n",
					r.name, r.x1, r.y1, r.x2, r.y2, r.got, r.want)
			}
		case '1', 'g':
			selected = 0
		case '2', 'e':
			selected = 1
		case '3', 'd':
			selected = 2
		case 'j':
			if cycleMode {
				activeImg = (activeImg + 1) % len(fixedPaths)
				img.Close()
				img = loadImage(activeImg)
				setWants(activeImg)
			}
		case 'k':
			if cycleMode {
				activeImg = (activeImg - 1 + len(fixedPaths)) % len(fixedPaths)
				img.Close()
				img = loadImage(activeImg)
				setWants(activeImg)
			}
		case 'a': // left edge left
			resources[selected].x1--
		case 'w': // top edge up
			resources[selected].y1--
		case 'l': // right edge right
			resources[selected].x2++
		case 'x': // bottom edge down
			resources[selected].y2++
		case 'h': // left edge right
			resources[selected].x1++
		case 'u': // top edge down
			resources[selected].y1++
		case 'z': // right edge left
			resources[selected].x2--
		case 'n': // bottom edge up
			resources[selected].y2--
		case '+', '=':
			if selected >= 0 {
				fmt.Println("C +1 (future: tune adaptive param)")
			}
		case '-', '_':
			if selected >= 0 {
				fmt.Println("C -1 (future: tune adaptive param)")
			}
		}
	}

	fmt.Println("\nFinal ROI Configuration:")
	for _, r := range resources {
		fmt.Printf("  {\"%s\", %d, %d, %d, %d},\n",
			r.name, r.x1, r.y1, r.x2, r.y2)
	}
}

func loadImage(idx int) gocv.Mat {
	path := fixedPaths[idx]
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		fmt.Printf("Cannot read %s\n", path)
	}
	return img
}

func setWants(idx int) {
	wantVals := [][]int{
		{443168, 434311, 4822},
		{1347740, 527360, 339},
		{1413444, 1410840, 9698},
		{665136, 339251, 13048},
		{649714, 644436, 6825},
		{597833, 254860, 11114},
	}
	if idx >= 0 && idx < len(wantVals) {
		for i := 0; i < 3 && i < len(wantVals[idx]); i++ {
			resources[i].want = wantVals[idx][i]
		}
	}
}

func analyzeROI(region gocv.Mat, roiRect image.Rectangle) (int, []image.Rectangle, float64) {
	roiH := region.Rows()
	thresh := gocv.NewMat()
	defer thresh.Close()

	gocv.AdaptiveThreshold(region, &thresh, 255, gocv.AdaptiveThresholdGaussian, gocv.ThresholdBinary, 9, 4)

	contours := gocv.FindContours(thresh, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	minH := maxInt(7, roiH/4)
	maxH := roiH - 1

	type blob struct {
		rect  image.Rectangle
		digit int
		conf  float32
	}

	var detected []blob

	for i := 0; i < contours.Size(); i++ {
		rect := gocv.BoundingRect(contours.At(i))
		if rect.Dy() < minH || rect.Dy() > maxH {
			continue
		}
		if rect.Dx() < 2 || rect.Dx() > 26 {
			continue
		}
		if rect.Min.Y <= 1 || rect.Max.Y >= roiH-1 {
			continue
		}

		if float64(rect.Dx()) > float64(rect.Dy())*1.6 {
			mid := rect.Dx() / 2
			for _, half := range []image.Rectangle{
				image.Rect(rect.Min.X, rect.Min.Y, rect.Min.X+mid, rect.Max.Y),
				image.Rect(rect.Min.X+mid, rect.Min.Y, rect.Max.X, rect.Max.Y),
			} {
				d := matchDigit(region.Region(half))
				if d.digit >= 0 {
					d.rect = half.Add(roiRect.Min)
					detected = append(detected, d)
				}
			}
			continue
		}

		d := matchDigit(region.Region(rect))
		if d.digit >= 0 {
			d.rect = rect.Add(roiRect.Min)
			detected = append(detected, d)
		}
	}

	sort.Slice(detected, func(i, j int) bool {
		return detected[i].rect.Min.X < detected[j].rect.Min.X
	})

	var sb string
	var blobs []image.Rectangle
	for _, d := range detected {
		sb += strconv.Itoa(d.digit)
		blobs = append(blobs, d.rect)
	}
	val, _ := strconv.Atoi(sb)
	return val, blobs, 0
}

func matchDigit(blobGray gocv.Mat) struct {
	rect  image.Rectangle
	digit int
	conf  float32
} {
	defer blobGray.Close()

	raw := gocv.NewMat()
	defer raw.Close()
	gocv.Threshold(blobGray, &raw, 0, 255, gocv.ThresholdBinary|gocv.ThresholdOtsu)

	bestDigit := -1
	maxConf := float32(-1.0)
	diff := gocv.NewMat()
	defer diff.Close()

	for j, variants := range templates {
		for _, tpl := range variants {
			if tpl.Empty() {
				continue
			}
			tw, th := tpl.Cols(), tpl.Rows()
			if tw < 2 || th < 2 {
				continue
			}
			scaled := gocv.NewMat()
			gocv.Resize(raw, &scaled, image.Point{X: tw, Y: th}, 0, 0, gocv.InterpolationCubic)
			gocv.AbsDiff(scaled, tpl, &diff)
			sum := diff.Sum()
			pixels := float64(tw * th)
			conf := 1.0 - float32(sum.Val1/(pixels*255.0))
			scaled.Close()
			if conf > maxConf {
				maxConf = conf
				bestDigit = j
			}
			if maxConf > 0.98 {
				return struct {
					rect  image.Rectangle
					digit int
					conf  float32
				}{digit: bestDigit, conf: maxConf}
			}
		}
	}

	if bestDigit >= 0 && maxConf > 0.50 {
		return struct {
			rect  image.Rectangle
			digit int
			conf  float32
		}{digit: bestDigit, conf: maxConf}
	}
	return struct {
		rect  image.Rectangle
		digit int
		conf  float32
	}{digit: -1}
}

func loadDigitTemplates() [][]gocv.Mat {
	templateDir := "assets/templates"

	templates := make([][]gocv.Mat, 10)
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("digit_%d", i)
		path := filepath.Join(templateDir, name+".png")
		tpl := gocv.IMRead(path, gocv.IMReadGrayScale)
		if tpl.Empty() {
			templates[i] = nil
			continue
		}
		binary := gocv.NewMat()
		gocv.Threshold(tpl, &binary, 0, 255, gocv.ThresholdBinary|gocv.ThresholdOtsu)
		tpl.Close()
		binary2 := gocv.NewMat()
		gocv.BitwiseNot(binary, &binary2)
		templates[i] = []gocv.Mat{binary, binary2}
	}
	return templates
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
