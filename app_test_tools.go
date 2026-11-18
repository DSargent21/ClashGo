//go:build !cli

package main

import (
	"fmt"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
	"github.com/Ducky705/ClashGO/internal/bot"
	"github.com/Ducky705/ClashGO/internal/config"
	"github.com/Ducky705/ClashGO/internal/game"
	"github.com/rs/zerolog/log"
)

// runDeviceTestMenu is the device-bound half of TestDeviceMenu: it opens the
// in-game Build Overview, optionally applies the filter, and closes it. It only
// taps navigation (builder icon, filter icon/buttons, close); it never confirms
// an upgrade. The wiring tests replace it so the Settings control can be
// exercised without ADB while still asserting the action and the config
// (device, display scale, upgrade filters) the menu would have used.
var runDeviceTestMenu = func(cfg *config.BotConfig, action string) error {
	client := adb.NewClient(
		adb.WithHost(cfg.Device.ADBHost),
		adb.WithPort(cfg.Device.ADBPort),
		adb.WithTimeout(30*time.Second),
		adb.WithJitterTaps(false),
		adb.WithJitterDelays(false),
	)
	client.DeviceID = cfg.Device.DeviceID
	defer client.Close()

	w, h, err := client.ScreenSize()
	if err != nil {
		return fmt.Errorf("read device screen: %w", err)
	}
	cal := game.NewCalibration(w, h)
	cal.Verified = true
	if cfg.Device.DisplayScale > 0 {
		cal.SetDisplayScale(cfg.Device.DisplayScale)
	}

	bm := bot.NewBuilderManager(client, cal, cfg.Upgrade, log.With().Str("component", "test_tools").Logger())

	switch action {
	case "open":
		return bm.OpenMenu()
	case "close":
		return bm.CloseMenu()
	default:
		if err := bm.OpenMenu(); err != nil {
			return err
		}
		time.Sleep(1200 * time.Millisecond)
		return bm.ApplyBuildingInGameFilter()
	}
}

// TestDeviceMenu is the Settings > Testing button handler. Validation and the
// running-bot guard live here, before any device is touched.
func (a *App) TestDeviceMenu(action string) error {
	switch action {
	case "open", "close", "filters":
	default:
		return fmt.Errorf("unknown test menu action %q", action)
	}

	a.mu.Lock()
	busy := a.bot != nil || a.cancel != nil
	a.mu.Unlock()
	if busy {
		return fmt.Errorf("stop the bot before using test tools")
	}

	return runDeviceTestMenu(config.LoadOrDefault("config.json"), action)
}
