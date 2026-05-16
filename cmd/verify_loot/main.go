package main

import (
	"flag"
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

	inputPath := flag.String("input", "", "Path to screenshot (omit to capture live)")
	deviceID := flag.String("device", "", "ADB Device ID (optional)")
	flag.Parse()

	// 1. Load or Capture Screen
	var screen gocv.Mat
	var cal *game.Calibration
	
	if *inputPath != "" && fileExists(*inputPath) {
		log.Info().Str("path", *inputPath).Msg("loading local image")
		screen = gocv.IMRead(*inputPath, gocv.IMReadColor)
		// For local images, we assume standard reference resolution or calculate scale
		cal = &game.Calibration{
			PhysicalW: screen.Cols(),
			PhysicalH: screen.Rows(),
			ScaleX:    float64(screen.Cols()) / game.RefWidth,
			ScaleY:    float64(screen.Rows()) / game.RefHeight,
		}
	} else {
		zl := &adbLogAdapter{log: log.Logger}
		client := adb.NewClient(
			adb.WithHost("127.0.0.1"),
			adb.WithPort(5037),
			adb.WithTimeout(10*time.Second),
			adb.WithLogger(zl),
		)

		log.Info().Msg("connecting to ADB...")
		
		// If no device specified, try to find one
		if *deviceID == "" {
			devs, err := client.Devices()
			if err != nil {
				log.Fatal().Err(err).Msg("error listing devices")
			}
			if len(devs) == 0 {
				log.Fatal().Msg("no ADB devices found")
			}
			if len(devs) == 1 {
				*deviceID = devs[0]
			} else {
				// Multiple devices: prefer 127.0.0.1:5555
				for _, d := range devs {
					if strings.Contains(d, "127.0.0.1:5555") {
						*deviceID = d
						break
					}
				}
				if *deviceID == "" {
					log.Fatal().Strs("devices", devs).Msg("multiple devices found, specify one with -device")
				}
			}
			log.Info().Str("device", *deviceID).Msg("auto-selected device")
		}
		
		client.DeviceID = *deviceID

		if err := client.Connect(); err != nil {
			log.Fatal().Err(err).Msg("ADB connect error")
		}
		defer client.Close()
		
		log.Info().Msg("capturing live screen...")
		var err error
		screen, err = client.CaptureToMat()
		if err != nil {
			log.Fatal().Err(err).Msg("capture error")
		}

		// Calibrate based on live screen
		calibrator := game.NewCalibrator(client)
		cal, err = calibrator.Calibrate()
		if err != nil {
			log.Fatal().Err(err).Msg("calibration error")
		}
	}
	defer screen.Close()

	if screen.Empty() {
		log.Fatal().Msg("empty screen buffer")
	}

	// 2. Initialize Loot Recognizer
	ts, err := game.NewTemplateStore("assets/templates")
	if err != nil {
		log.Fatal().Err(err).Msg("TemplateStore error")
	}
	if err := ts.LoadTemplates(); err != nil {
		log.Fatal().Err(err).Msg("LoadTemplates error")
	}
	defer ts.Close()

	lr := game.NewLootRecognizer(cal, ts, log.Logger)
	lr.Debug = true
	defer lr.Close()

	// 3. Perform Recognition
	log.Info().Msg("starting loot recognition")
	start := time.Now()
	loot, err := lr.ReadAvailableLoot(screen)
	elapsed := time.Since(start)

	if err != nil {
		log.Error().Err(err).Msg("recognition error")
	} else {
		log.Info().
			Int("gold", loot.Gold).
			Int("elixir", loot.Elixir).
			Int("dark_elixir", loot.DarkElixir).
			Dur("duration", elapsed).
			Msg("final results")
	}
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

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
