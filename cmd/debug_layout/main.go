package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/attack"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/Ducky705/ClashGO/internal/paths"
	"github.com/Ducky705/ClashGO/internal/vision"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"
)

func main() {
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
	zerolog.SetGlobalLevel(zerolog.DebugLevel)

	// Initialize ADB
	client := adb.NewClient()
	if err := client.AutoDetectDevice(); err != nil {
		log.Warn().Err(err).Msg("auto-detect failed, using default ID")
	}
	if err := client.Connect(); err != nil {
		log.Fatal().Err(err).Msg("failed to connect to ADB")
	}
	defer client.Close()

	// Load Calibration
	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to calibrate")
	}

	botCfg := config.DefaultConfig()
	executor := attack.NewExecutor(client, cal, &botCfg.Attack, log.Logger)

	// Capture Screen
	log.Info().Msg("capturing screen...")
	screen, err := client.CaptureToMat()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to capture screen")
	}
	defer screen.Close()

	w, h := screen.Cols(), screen.Rows()
	mBarY := int(float64(h) * 0.78) // default fallback

	// Parse Precision Config
	var pCfg attack.PrecisionConfig
	pData, err := os.ReadFile(paths.Resolve("precision_config.json"))
	if err == nil && json.Unmarshal(pData, &pCfg) == nil {
		scaleY := float64(h) / float64(pCfg.Height)
		mBarY = int(float64(pCfg.BarY) * scaleY)
		if mBarY > int(float64(h)*0.92) {
			mBarY = int(float64(h) * 0.92)
		}
	}

	// Parse layouts
	slots := executor.ParseLayout(screen, pCfg, w, h, mBarY)
	barROI := image.Rect(0, mBarY, w, h)

	// Load Ground-Truth Manual Labels
	manualMap := make(map[int]string)
	if data, err := os.ReadFile(paths.Resolve("manual_labels.json")); err == nil {
		var lConf struct {
			Slots []struct {
				X    int    `json:"x"`
				Name string `json:"name"`
			} `json:"slots"`
		}
		if json.Unmarshal(data, &lConf) == nil {
			for _, slot := range lConf.Slots {
				manualMap[slot.X] = slot.Name
			}
		}
	}

	// Visual Diagnostics Overlay
	debugImg := screen.Clone()
	defer debugImg.Close()

	// Draw Y-bar limit
	gocv.Line(&debugImg, image.Pt(0, mBarY), image.Pt(w, mBarY), color.RGBA{0, 0, 255, 255}, 2)

	// Draw categorized circles and names over detected active slots
	for _, slot := range slots {
		c := color.RGBA{255, 255, 255, 255} // Default white
		manualLabel := "None"
		if label, ok := manualMap[slot.X]; ok {
			manualLabel = label
		}

		// Find best matching template for this slot
		bestTplName := "Unknown"
		bestConf := 0.0

		// Check all templates in executor using multi-scale search
		for tName, tpl := range executor.GetTemplates() {
			if tpl.Empty() {
				continue
			}
			matches, _ := vision.MatchMultiScaleROICached(screen, tpl, tName, 0.2, 1.2, 20, 0.50, barROI)
			for _, m := range matches {
				if math.Abs(float64(m.Point.X-slot.X)) < float64(w)*0.045 {
					if m.Confidence > bestConf {
						bestConf = m.Confidence
						bestTplName = tName
					}
				}
			}
		}

		switch slot.Category {
		case "Troop":
			c = color.RGBA{255, 0, 0, 255} // Blue in BGR
		case "Siege":
			c = color.RGBA{0, 255, 255, 255} // Yellow in BGR
		case "Hero":
			c = color.RGBA{0, 255, 0, 255} // Green in BGR
		case "Spell":
			c = color.RGBA{255, 0, 255, 255} // Purple in BGR
		case "CC":
			c = color.RGBA{0, 165, 255, 255} // Orange in BGR
		}

		gocv.Circle(&debugImg, image.Pt(slot.X, slot.Y), 22, c, 2)
		
		// Draw a solid dark background rectangle for text readability
		rectText := image.Rect(slot.X-42, slot.Y-80, slot.X+42, slot.Y-28)
		gocv.Rectangle(&debugImg, rectText, color.RGBA{20, 20, 20, 255}, -1)
		gocv.Rectangle(&debugImg, rectText, c, 1) // Border matching category

		// Auto detected template name
		txtAuto := bestTplName
		if len(txtAuto) > 10 {
			txtAuto = txtAuto[:10] // Truncate to fit
		}
		txtAuto = "A:" + txtAuto

		// Confidence and manual override text
		txtConf := fmt.Sprintf("C:%.2f", bestConf)
		
		txtMan := manualLabel
		if len(txtMan) > 10 {
			txtMan = txtMan[:10]
		}
		txtMan = "M:" + txtMan

		gocv.PutText(&debugImg, txtAuto, image.Pt(slot.X-38, slot.Y-68), gocv.FontHersheySimplex, 0.30, color.RGBA{0, 255, 0, 255}, 1)
		gocv.PutText(&debugImg, txtConf, image.Pt(slot.X-38, slot.Y-53), gocv.FontHersheySimplex, 0.30, color.RGBA{150, 255, 150, 255}, 1)
		gocv.PutText(&debugImg, txtMan, image.Pt(slot.X-38, slot.Y-38), gocv.FontHersheySimplex, 0.30, color.RGBA{0, 255, 255, 255}, 1)
		
		// Draw X coord below circle
		gocv.PutText(&debugImg, fmt.Sprintf("%d", slot.X), image.Pt(slot.X-15, slot.Y+40), gocv.FontHersheySimplex, 0.35, color.RGBA{200, 200, 200, 255}, 1)
	}

	outputPath := "layout_debug.png"
	gocv.IMWrite(outputPath, debugImg)
	log.Info().Str("path", outputPath).Msg("Saved layout diagnostics screenshot")
}
