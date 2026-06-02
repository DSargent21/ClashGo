//go:build darwin

package adb

import (
	"fmt"
	"os/exec"
	"time"
)

// EnsureBlueStacksMac guarantees BlueStacks is running with specific resolution and DPI.
func (c *Client) EnsureBlueStacksMac(width, height, dpi int) error {
	c.log.Info(fmt.Sprintf("enforcing BlueStacks resolution: %dx%d (DPI: %d)", width, height, dpi))

	// 1. Kill existing BlueStacks to ensure config can be written safely
	_ = exec.Command("osascript", "-e", "quit app \"BlueStacks\"").Run()
	time.Sleep(2 * time.Second)
	_ = exec.Command("killall", "-9", "BlueStacks").Run()
	time.Sleep(1 * time.Second)

	// 2. Write configuration using 'defaults'
	// Try both the new now.gg bundle ID and the classic BlueStacks bundle ID
	bundleIDs := []string{"com.now.gg.BlueStacks", "com.BlueStacks.AppPlayer"}
	
	for _, bundleID := range bundleIDs {
		c.log.Info(fmt.Sprintf("writing configuration to %s", bundleID))
		commands := [][]string{
			{"write", bundleID, "Guests/Android/FrameBuffer/0/GuestWidth", "-int", fmt.Sprint(width)},
			{"write", bundleID, "Guests/Android/FrameBuffer/0/WindowWidth", "-int", fmt.Sprint(width)},
			{"write", bundleID, "Guests/Android/FrameBuffer/0/GuestHeight", "-int", fmt.Sprint(height)},
			{"write", bundleID, "Guests/Android/FrameBuffer/0/WindowHeight", "-int", fmt.Sprint(height)},
			{"write", bundleID, "Guests/Android/FrameBuffer/0/Dpi", "-int", fmt.Sprint(dpi)},
		}

		for _, args := range commands {
			_ = exec.Command("defaults", args...).Run()
		}
	}

	// 3. Launch BlueStacks
	c.log.Info("launching BlueStacks...")
	if err := exec.Command("open", "-a", "BlueStacks").Run(); err != nil {
		return fmt.Errorf("open BlueStacks failed: %w", err)
	}

	return nil
}
