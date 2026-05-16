package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"strings"

	"github.com/Ducky705/ClashGo/internal/attack"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: ./simulate <screenshot> <strategy>")
		return
	}

	screenPath, yamlPath := os.Args[1], os.Args[2]
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

	s, _ := strategy.ParseYAML(yamlPath)
	screen := gocv.IMRead(screenPath, gocv.IMReadColor)
	defer screen.Close()
	w, h := screen.Cols(), screen.Rows()

	cfg := config.DefaultConfig()
	cal := &game.Calibration{PhysicalW: w, PhysicalH: h} 
	executor := attack.NewExecutor(nil, cal, &cfg.Attack, logger)

	var bT, bB, bL, bR image.Point // Blue (Base)
	var fT, fB, fL, fR image.Point // Yellow (Field)
	mBarY := int(float64(h) * 0.82)

	calData, _ := os.ReadFile("assets/base_calibration.json")
	var mCal attack.BaseCalibration
	json.Unmarshal(calData, &mCal)
	
	scaleX, scaleY := float64(w)/float64(mCal.Width), float64(h)/float64(mCal.Height)
	bT = image.Pt(int(float64(mCal.BaseTop.X)*scaleX), int(float64(mCal.BaseTop.Y)*scaleY))
	bB = image.Pt(int(float64(mCal.BaseBottom.X)*scaleX), int(float64(mCal.BaseBottom.Y)*scaleY))
	bL = image.Pt(int(float64(mCal.BaseLeft.X)*scaleX), int(float64(mCal.BaseLeft.Y)*scaleY))
	bR = image.Pt(int(float64(mCal.BaseRight.X)*scaleX), int(float64(mCal.BaseRight.Y)*scaleY))
	fT = image.Pt(int(float64(mCal.FieldTop.X)*scaleX), int(float64(mCal.FieldTop.Y)*scaleY))
	fB = image.Pt(int(float64(mCal.FieldBottom.X)*scaleX), int(float64(mCal.FieldBottom.Y)*scaleY))
	fL = image.Pt(int(float64(mCal.FieldLeft.X)*scaleX), int(float64(mCal.FieldLeft.Y)*scaleY))
	fR = image.Pt(int(float64(mCal.FieldRight.X)*scaleX), int(float64(mCal.FieldRight.Y)*scaleY))
	mBarY = int(float64(mCal.BarY) * scaleY)

	targetEdge := s.TargetEdge
	if strings.EqualFold(targetEdge, "Random") { targetEdge = "TopRight" }

	debugImg := screen.Clone()
	defer debugImg.Close()

	// Draw diamonds with correct colors (OpenCV is BGR)
	drawDiamond(&debugImg, bT, bR, bB, bL, color.RGBA{255, 0, 0, 255}, "BLUE (BASE)")
	drawDiamond(&debugImg, fT, fR, fB, fL, color.RGBA{0, 255, 255, 255}, "YELLOW (FIELD)")
	gocv.Line(&debugImg, image.Pt(0, mBarY), image.Pt(w, mBarY), color.RGBA{0, 0, 255, 255}, 2) // Red Bar

	fmt.Printf("\n🚀 SIMULATION: %s | Edge: %s\n", s.Name, targetEdge)

	for _, phase := range s.Phases {
		p1, p2 := executor.CalculateInBetween(targetEdge, phase.Offset, bT, bB, bL, bR, fT, fB, fL, fR)
		p1, p2 = executor.MaximizeLineSpread(p1, p2, w, mBarY)

		col := color.RGBA{0, 255, 0, 255}
		if phase.Pattern == "Ability" { continue }
		
		gocv.Line(&debugImg, p1, p2, col, 3)
		gocv.PutText(&debugImg, phase.Name, p1, gocv.FontHersheySimplex, 0.8, col, 2)
	}

	gocv.IMWrite("simulation_debug.png", debugImg)
	fmt.Println("\n✅ Simulation complete. Check simulation_debug.png for BLUE/YELLOW lines.")
}

func drawDiamond(img *gocv.Mat, t, r, b, l image.Point, c color.RGBA, label string) {
	gocv.Line(img, t, r, c, 2); gocv.Line(img, r, b, c, 2); gocv.Line(img, b, l, c, 2); gocv.Line(img, l, t, c, 2)
}
