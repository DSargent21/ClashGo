package main

import (
	"fmt"
	"os/exec"
	"github.com/Ducky705/ClashGo/internal/adb"
)

func main() {
	client := adb.NewClient(adb.WithDeviceID("emulator-5554"))
	if err := client.Connect(); err != nil {
		fmt.Printf("Connect failed: %v\n", err)
		return
	}
	defer client.Close()

	fmt.Println("Running Hardware-Level Key Emulation (Down/Up)...")
	fmt.Println("Focusing BlueStacks...")
	
	// This script uses the most realistic 'hardware' emulation possible with AppleScript
	script := `
		tell application "BlueStacks" to activate
		delay 1.2
		tell application "System Events"
			repeat 10 times
				key down "i"
				delay 0.05
				key up "i"
				delay 0.05
			end repeat
		end tell
	`
	exec.Command("osascript", "-e", script).Run()

	fmt.Println("Sending background ADB keycodes...")
	client.KeyEvent(37) // KEYCODE_I
	
	fmt.Println("Done. Please check if it zoomed. If you see 'iiii' in your terminal, it means focus failed.")
}
