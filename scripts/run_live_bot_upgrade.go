package main

import (
	"fmt"
	"time"

	"github.com/Ducky705/ClashGo/internal/bot"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/game"
)

func main() {
	fmt.Println("Loading configuration...")
	cfg := config.LoadOrDefault("config.json")
	if cfg == nil {
		cfg = config.DefaultConfig()
	}

	// Force restart and clean state
	cfg.Device.RestartOnStartup = true
	cfg.Upgrade.UpgradeWalls = true

	fmt.Println("Initializing bot...")
	b, err := bot.NewBot(cfg)
	if err != nil {
		fmt.Printf("Error initializing bot: %v\n", err)
		return
	}
	defer b.Stop()

	// Wait for game to settle fully after startup
	fmt.Println("Waiting 10 seconds for calibration and UI elements to load...")
	time.Sleep(10 * time.Second)

	fmt.Println("Starting Wall Upgrade sequence...")
	gc := game.NewGameContext()
	b.UpgradeWalls(gc)

	fmt.Println("Sequence finished.")
}
