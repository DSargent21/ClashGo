package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"strings"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"
)

func main() {
	// Professional logging setup
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
	zerolog.SetGlobalLevel(zerolog.DebugLevel)

	inputPath := flag.String("input", "", "Path to screenshot (optional, captures live if omitted)")
	outputPath := flag.String("output", "loot_diag.png", "Path to save diagnostic image")
	deviceID := flag.String("device", "", "ADB Device ID (optional)")
	flag.Parse()

	var screen gocv.Mat
	var cal *game.Calibration
	var err error

	// 1. Acquire Image
	var rawPath string
	if *inputPath != "" {
		if _, err := os.Stat(*inputPath); err != nil {
			log.Fatal().Err(err).Str("path", *inputPath).Msg("input file not found")
		}
		log.Info().Str("path", *inputPath).Msg("loading input image")
		screen = gocv.IMRead(*inputPath, gocv.IMReadColor)
		rawPath = *inputPath
		cal = &game.Calibration{
			PhysicalW: screen.Cols(),
			PhysicalH: screen.Rows(),
			ScaleX:    float64(screen.Cols()) / game.RefWidth,
			ScaleY:    float64(screen.Rows()) / game.RefHeight,
		}
	} else {
		client := connectADB(*deviceID)
		defer client.Close()

		log.Info().Msg("capturing screen...")
		screen, err = client.CaptureToMat()
		if err != nil {
			log.Fatal().Err(err).Msg("capture failed")
		}

		rawPath = "captured_screen.png"
		gocv.IMWrite(rawPath, screen)
		log.Info().Str("path", rawPath).Msg("raw capture saved for future tuning")

		calibrator := game.NewCalibrator(client)
		cal, err = calibrator.Calibrate()
		if err != nil {
			log.Warn().Err(err).Msg("calibration failed, using defaults")
			cal = &game.Calibration{
				PhysicalW: screen.Cols(),
				PhysicalH: screen.Rows(),
				ScaleX:    float64(screen.Cols()) / game.RefWidth,
				ScaleY:    float64(screen.Rows()) / game.RefHeight,
			}
		}
	}
	defer screen.Close()

	if screen.Empty() {
		log.Fatal().Msg("empty image")
	}

	// 2. Initialize Loot Recognizer
	ts, err := game.NewTemplateStore("assets/templates")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create template store")
	}
	if err := ts.LoadTemplates(); err != nil {
		log.Fatal().Err(err).Msg("failed to load templates")
	}
	defer ts.Close()

	lr := game.NewLootRecognizer(cal, ts, log.Logger)
	lr.Debug = true
	defer lr.Close()

	// 3. Run Recognition
	log.Info().Msg("starting loot recognition...")
	start := time.Now()
	report, err := lr.ReadLootDetailed(screen)
	duration := time.Since(start)

	if err != nil {
		log.Fatal().Err(err).Msg("recognition error")
	}

	// 4. Print Summary
	fmt.Println("\n" + strings.Repeat("=", 40))
	fmt.Printf(" LOOT DETECTION REPORT\n")
	fmt.Println(strings.Repeat("=", 40))
	fmt.Printf(" Gold:        %10d (Conf: %.2f)\n", report.Resources.Gold, report.GoldConf)
	fmt.Printf(" Elixir:      %10d (Conf: %.2f)\n", report.Resources.Elixir, report.ElixirConf)
	fmt.Printf(" Dark Elixir: %10d (Conf: %.2f)\n", report.Resources.DarkElixir, report.DeConf)
	fmt.Println(strings.Repeat("-", 40))
	fmt.Printf(" Scale:       %10.2f\n", report.Scale)
	fmt.Printf(" Duration:    %10s\n", duration)
	fmt.Println(strings.Repeat("=", 40))

	// 5. Draw Diagnostic Image
	diag := screen.Clone()
	defer diag.Close()

	// Icons
	gocv.Rectangle(&diag, report.GoldIcon, color.RGBA{255, 215, 0, 255}, 1)
	gocv.Rectangle(&diag, report.ElixirIcon, color.RGBA{255, 0, 255, 255}, 1)
	gocv.Rectangle(&diag, report.DeIcon, color.RGBA{100, 100, 100, 255}, 1)

	// ROIs
	drawROI(diag, report.GoldROI, color.RGBA{255, 215, 0, 255}, fmt.Sprintf("Gold: %d", report.Resources.Gold))
	for _, b := range report.GoldBlobs {
		gocv.Rectangle(&diag, b, color.RGBA{255, 255, 255, 255}, 1)
	}

	drawROI(diag, report.ElixirROI, color.RGBA{255, 0, 255, 255}, fmt.Sprintf("Elixir: %d", report.Resources.Elixir))
	for _, b := range report.ElixirBlobs {
		gocv.Rectangle(&diag, b, color.RGBA{255, 255, 255, 255}, 1)
	}

	drawROI(diag, report.DeROI, color.RGBA{100, 100, 100, 255}, fmt.Sprintf("DE: %d", report.Resources.DarkElixir))
	for _, b := range report.DeBlobs {
		gocv.Rectangle(&diag, b, color.RGBA{255, 255, 255, 255}, 1)
	}

	if gocv.IMWrite(*outputPath, diag) {
		log.Info().Str("path", *outputPath).Msg("diagnostic image saved")
	} else {
		log.Error().Str("path", *outputPath).Msg("failed to save diagnostic image")
	}
}

func connectADB(deviceID string) *adb.Client {
	zl := &adbLogAdapter{log: log.Logger}
	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(10*time.Second),
		adb.WithLogger(zl),
	)

	log.Info().Msg("connecting to ADB...")
	if deviceID == "" {
		devs, err := client.Devices()
		if err != nil {
			log.Fatal().Err(err).Msg("failed to list devices")
		}
		if len(devs) == 0 {
			log.Fatal().Msg("no ADB devices found")
		}
		if len(devs) == 1 {
			deviceID = devs[0]
		} else {
			for _, d := range devs {
				if strings.Contains(d, "127.0.0.1:5555") {
					deviceID = d
					break
				}
			}
			if deviceID == "" {
				log.Fatal().Strs("devices", devs).Msg("multiple devices found, specify one with -device")
			}
		}
		log.Info().Str("device", deviceID).Msg("auto-selected device")
	}
	client.DeviceID = deviceID

	if err := client.Connect(); err != nil {
		log.Fatal().Err(err).Msg("ADB connect error")
	}
	return client
}

func drawROI(img gocv.Mat, rect image.Rectangle, c color.RGBA, label string) {
	if rect.Empty() {
		return
	}
	gocv.Rectangle(&img, rect, c, 2)
	
	// Draw label background
	labelPos := image.Pt(rect.Min.X, rect.Min.Y-10)
	if labelPos.Y < 20 {
		labelPos.Y = rect.Max.Y + 20
	}
	
	gocv.PutText(&img, label, labelPos, gocv.FontHersheySimplex, 0.8, c, 2)
}

type adbLogAdapter struct {
	log zerolog.Logger
}

func (a *adbLogAdapter) Debug() bool                         { return a.log.GetLevel() <= zerolog.DebugLevel }
func (a *adbLogAdapter) Debugf(format string, v ...any)      { a.log.Debug().Msgf(format, v...) }
func (a *adbLogAdapter) Info(msg string)                     { a.log.Info().Msg(msg) }
func (a *adbLogAdapter) Warn(msg string)                     { a.log.Warn().Msg(msg) }
func (a *adbLogAdapter) Error(msg string)                    { a.log.Error().Msg(msg) }
func (a *adbLogAdapter) WithFields(f map[string]any) adb.Logger { return &adbLogAdapter{log: a.log.With().Fields(f).Logger()} }
