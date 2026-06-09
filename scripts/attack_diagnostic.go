package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strings"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/Ducky705/ClashGO/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type BaseCalibration struct {
	BaseTop      image.Point `json:"base_top"`
	BaseRight    image.Point `json:"base_right"`
	BaseBottom   image.Point `json:"base_bottom"`
	BaseLeft     image.Point `json:"base_left"`
	FieldTop     image.Point `json:"field_top"`
	FieldRight   image.Point `json:"field_right"`
	FieldBottom  image.Point `json:"field_bottom"`
	FieldLeft    image.Point `json:"field_left"`
	BarY         int         `json:"bar_y"`
	Width        int         `json:"width"`
	Height       int         `json:"height"`
}

func main() {
	logger := zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"}).With().Timestamp().Logger()

	// 1. Initialize ADB
	client := adb.NewClient(func(c *adb.Client) {
		c.DeviceID = "127.0.0.1:5555"
	})
	if err := client.Connect(); err != nil {
		logger.Fatal().Err(err).Msg("failed to connect to ADB")
	}
	defer client.Close()

	// 2. Capture Screen
	logger.Info().Msg("capturing live screen...")
	screen, err := client.CaptureToMat()
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to capture screen")
	}
	defer screen.Close()

	w, h := screen.Cols(), screen.Rows()
	debugImg := screen.Clone()
	defer debugImg.Close()

	// 3. Load Strategy
	stratPath := "assets/strategies/auto_edrag_rush.yaml"
	s, err := strategy.ParseYAML(stratPath)
	if err != nil {
		logger.Fatal().Err(err).Str("path", stratPath).Msg("failed to load strategy")
	}
	logger.Info().Str("strategy", s.Name).Msg("loaded strategy")

	// 4. Load Calibration
	var bT, bB, bL, bR image.Point // Base
	var fT, fB, fL, fR image.Point // Field
	mBarY := int(float64(h) * 0.78)
	
	calData, err := os.ReadFile("assets/base_calibration.json")
	if err == nil {
		var cal BaseCalibration
		if json.Unmarshal(calData, &cal) == nil {
			scaleX, scaleY := float64(w)/float64(cal.Width), float64(h)/float64(cal.Height)
			bT = image.Pt(int(float64(cal.BaseTop.X)*scaleX), int(float64(cal.BaseTop.Y)*scaleY))
			bB = image.Pt(int(float64(cal.BaseBottom.X)*scaleX), int(float64(cal.BaseBottom.Y)*scaleY))
			bL = image.Pt(int(float64(cal.BaseLeft.X)*scaleX), int(float64(cal.BaseLeft.Y)*scaleY))
			bR = image.Pt(int(float64(cal.BaseRight.X)*scaleX), int(float64(cal.BaseRight.Y)*scaleY))
			fT = image.Pt(int(float64(cal.FieldTop.X)*scaleX), int(float64(cal.FieldTop.Y)*scaleY))
			fB = image.Pt(int(float64(cal.FieldBottom.X)*scaleX), int(float64(cal.FieldBottom.Y)*scaleY))
			fL = image.Pt(int(float64(cal.FieldLeft.X)*scaleX), int(float64(cal.FieldLeft.Y)*scaleY))
			fR = image.Pt(int(float64(cal.FieldRight.X)*scaleX), int(float64(cal.FieldRight.Y)*scaleY))
			mBarY = int(float64(cal.BarY) * scaleY)
		}
	}

	// Safety: Ensure mBarY is at most 78% of screen height
	if mBarY > int(float64(h)*0.78) {
		mBarY = int(float64(h) * 0.78)
	}
	logger.Info().Int("bar_y", mBarY).Msg("using safety limit")

	// Draw Diamonds
	drawDiamond(&debugImg, bT, bR, bB, bL, color.RGBA{255, 0, 0, 255})   // Blue in BGR
	drawDiamond(&debugImg, fT, fR, fB, fL, color.RGBA{0, 255, 255, 255}) // Yellow in BGR
	gocv.Line(&debugImg, image.Pt(0, mBarY), image.Pt(w, mBarY), color.RGBA{0, 0, 255, 255}, 2) // Red BarY

	// 5. Simulate Detection
	barROI := image.Rect(0, int(float64(h)*0.6), w, h)
	gocv.Rectangle(&debugImg, barROI, color.RGBA{255, 255, 255, 255}, 1) // White ROI box

	for _, phase := range s.Phases {
		logger.Info().Str("phase", phase.Name).Msg("simulating phase")

		for _, unit := range phase.Units {
			unitName := strings.ToLower(strings.TrimSpace(unit.Name))
			fileName := strings.ReplaceAll(unitName, " ", "_")
			tplPath := fmt.Sprintf("assets/templates/attack/%s.png", fileName)
			tpl := gocv.IMRead(tplPath, gocv.IMReadColor)
			if tpl.Empty() {
				logger.Warn().Str("path", tplPath).Msg("template missing")
				continue
			}
			defer tpl.Close()

			matches, _ := vision.MatchMultiScaleROI(screen, tpl, 0.1, 1.2, 30, 0.4, barROI)
			if len(matches) == 0 {
				logger.Warn().Str("unit", unit.Name).Msg("unit NOT FOUND")
				continue
			}

			best := matches[0]
			logger.Info().Str("unit", unit.Name).Float64("conf", best.Confidence).Interface("pos", best.Point).Msg("found unit")
			
			// Mark selection point
			gocv.Circle(&debugImg, best.Point, 15, color.RGBA{255, 255, 0, 255}, 3) // Yellow
			gocv.PutText(&debugImg, unit.Name, best.Point.Add(image.Pt(10, -10)), gocv.FontHersheySimplex, 0.5, color.RGBA{255, 255, 0, 255}, 1)

			// Calculate deployment
			edge := s.TargetEdge
			if strings.EqualFold(edge, "Random") {
				edge = "TopRight" // Fixed for diag
			}
			p1, p2 := calculateInBetween(edge, phase.Offset, bT, bB, bL, bR, fT, fB, fL, fR)
			p1, p2 = maximizeLineSpread(p1, p2, w, mBarY)

			gocv.Line(&debugImg, p1, p2, color.RGBA{0, 255, 0, 255}, 2) // Green Line
			mid := image.Point{X: (p1.X + p2.X) / 2, Y: (p1.Y + p2.Y) / 2}
			gocv.Circle(&debugImg, mid, 10, color.RGBA{0, 255, 0, 255}, -1)
		}
	}

	outPath := "attack_diagnostic.png"
	gocv.IMWrite(outPath, debugImg)
	logger.Info().Str("path", outPath).Msg("diagnostic image saved!")
}

func drawDiamond(img *gocv.Mat, t, r, b, l image.Point, c color.RGBA) {
	gocv.Line(img, t, r, c, 2)
	gocv.Line(img, r, b, c, 2)
	gocv.Line(img, b, l, c, 2)
	gocv.Line(img, l, t, c, 2)
}

func calculateInBetween(edge string, offset int, bT, bB, bL, bR, fT, fB, fL, fR image.Point) (p1, p2 image.Point) {
	pct := float64(offset) / 100.0
	var baseP1, baseP2, fieldP1, fieldP2 image.Point
	switch edge {
	case "TopRight": baseP1, baseP2, fieldP1, fieldP2 = bT, bR, fT, fR
	case "BottomRight": baseP1, baseP2, fieldP1, fieldP2 = bR, bB, fR, fB
	case "BottomLeft": baseP1, baseP2, fieldP1, fieldP2 = bB, bL, fB, fL
	case "TopLeft": baseP1, baseP2, fieldP1, fieldP2 = bL, bT, fL, fT
	default: baseP1, baseP2, fieldP1, fieldP2 = bT, bR, fT, fR
	}

	p1 = image.Pt(
		int(float64(baseP1.X) + float64(fieldP1.X-baseP1.X)*pct),
		int(float64(baseP1.Y) + float64(fieldP1.Y-baseP1.Y)*pct),
	)
	p2 = image.Pt(
		int(float64(baseP2.X) + float64(fieldP2.X-baseP2.X)*pct),
		int(float64(baseP2.Y) + float64(fieldP2.Y-baseP2.Y)*pct),
	)
	return
}

func maximizeLineSpread(p1, p2 image.Point, w, barY int) (image.Point, image.Point) {
	dx, dy := float64(p2.X-p1.X), float64(p2.Y-p1.Y)
	mag := math.Sqrt(dx*dx + dy*dy)
	if mag == 0 { return p1, p2 }
	ux, uy := dx/mag, dy/mag
	ext1 := image.Pt(p1.X-int(ux*2000), p1.Y-int(uy*2000))
	ext2 := image.Pt(p2.X+int(ux*2000), p2.Y+int(uy*2000))
	safeRect := image.Rect(0, 0, w, barY)
	return clipLineToRect(ext1, ext2, safeRect), clipLineToRect(ext2, ext1, safeRect)
}

func clipLineToRect(p1, p2 image.Point, r image.Rectangle) image.Point {
	x1, y1 := float64(p1.X), float64(p1.Y)
	x2, y2 := float64(p2.X), float64(p2.Y)

	if x1 >= float64(r.Min.X) && x1 <= float64(r.Max.X) && y1 >= float64(r.Min.Y) && y1 <= float64(r.Max.Y) {
		return p1
	}

	dx := x2 - x1
	dy := y2 - y1
	t := 1.0

	if dx > 0 {
		tl := (float64(r.Min.X) - x1) / dx
		if tl > 0 && tl < t { t = tl }
	} else if dx < 0 {
		tr := (float64(r.Max.X) - x1) / dx
		if tr > 0 && tr < t { t = tr }
	}

	if dy > 0 {
		tt := (float64(r.Min.Y) - y1) / dy
		if tt > 0 && tt < t { t = tt }
	} else if dy < 0 {
		tb := (float64(r.Max.Y) - y1) / dy
		if tb > 0 && tb < t { t = tb }
	}

	return image.Pt(int(x1+t*dx), int(y1+t*dy))
}
