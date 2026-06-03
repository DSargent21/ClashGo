package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/bot"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// Setup console logger
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: "15:04:05"}).With().Timestamp().Logger()

	cfg := config.DefaultConfig()
	cfg.Upgrade.UpgradeWalls = true
	cfg.Device.RestartOnStartup = true

	fmt.Println("Ensuring BlueStacks is running on macOS...")
	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = cfg.Device.DeviceID

	if err := client.EnsureBlueStacksMac(cfg.Device.Width, cfg.Device.Height, cfg.Device.DPI); err != nil {
		fmt.Printf("Error ensuring BlueStacks: %v\n", err)
	}

	fmt.Println("Connecting to ADB (waiting up to 90s)...")
	deadline := time.Now().Add(90 * time.Second)
	connected := false
	for time.Now().Before(deadline) {
		_ = client.AutoDetectDevice()
		if err := client.Reconnect(); err == nil {
			connected = true
			break
		}
		time.Sleep(3 * time.Second)
	}

	if !connected {
		fmt.Println("ADB Connection Error: Timeout waiting for BlueStacks")
		return
	}
	defer client.Close()

	fmt.Println("Waiting for Android boot completed...")
	if err := client.WaitForBoot(90 * time.Second); err != nil {
		fmt.Printf("Boot wait failed: %v\n", err)
	}

	fmt.Println("Launching Clash of Clans app...")
	packageName := cfg.Device.PackageName
	if packageName == "" {
		packageName = "com.supercell.clashofclans"
	}
	if err := client.StartApp(packageName); err != nil {
		fmt.Printf("Error starting app: %v\n", err)
	}
	fmt.Println("Waiting 15s for game to render...")
	time.Sleep(15 * time.Second)

	fmt.Println("Calibrating...")
	calibrator := game.NewCalibrator(client)
	cal, err := calibrator.Calibrate()
	if err != nil {
		fmt.Printf("Calibration failed: %v\n", err)
		return
	}
	_ = cal

	fmt.Println("Loading templates...")
	templates, err := game.NewTemplateStore("assets/templates")
	if err != nil {
		fmt.Printf("Failed to load templates: %v\n", err)
		return
	}
	templates.LoadTemplates()

	// Instantiate bot
	b, err := bot.NewBot(cfg)
	if err != nil {
		fmt.Printf("Failed to create bot: %v\n", err)
		return
	}

	gc := game.NewGameContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Println("Running wall upgrade test...")
	// Start bot dependencies or just run it synchronously
	b.UpgradeWalls(gc)

	fmt.Println("Test run finished.")
	_ = ctx
}
