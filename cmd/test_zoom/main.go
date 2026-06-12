package main

import (
	"fmt"
	"strings"
	"github.com/Ducky705/ClashGO/internal/adb"
)

func main() {
	client := adb.NewClient(adb.WithHost("127.0.0.1"), adb.WithPort(5037))
	client.DeviceID = "localhost:5555"
	client.Connect()

	w, h, _ := client.ScreenSize()
	device, _ := client.DetectTouchDevice()
	
	fmt.Printf("Device: %s, Screen: %dx%d\n", device, w, h)

	const touchMax = 32767
	scale := func(pixel, size int) int { return (pixel * touchMax) / size }

	var batch strings.Builder
	add := func(typ, code, value int) {
		batch.WriteString(fmt.Sprintf("sendevent %s %d %d %d && ", device, typ, code, value))
	}

	// Pinch Out
	f1Start := [2]int{w/2 - w/4, h/2 - h/4}
	f1End := [2]int{w/2 - 50, h/2 - 50}
	f2Start := [2]int{w/2 + w/4, h/2 + h/4}
	f2End := [2]int{w/2 + 50, h/2 + 50}

	add(3, 57, 1) // Tracking ID
	add(1, 330, 1) // BTN_TOUCH
	add(3, 53, scale(f1Start[0], w))
	add(3, 54, scale(f1Start[1], h))
	add(0, 2, 0)
	
	add(3, 57, 2)
	add(3, 53, scale(f2Start[0], w))
	add(3, 54, scale(f2Start[1], h))
	add(0, 2, 0)
	add(0, 0, 0)

	steps := 15
	for i := 1; i <= steps; i++ {
		add(3, 53, scale(f1Start[0] + (f1End[0]-f1Start[0])*i/steps, w))
		add(3, 54, scale(f1Start[1] + (f1End[1]-f1Start[1])*i/steps, h))
		add(0, 2, 0)
		add(3, 53, scale(f2Start[0] + (f2End[0]-f2Start[0])*i/steps, w))
		add(3, 54, scale(f2Start[1] + (f2End[1]-f2Start[1])*i/steps, h))
		add(0, 2, 0)
		add(0, 0, 0)
	}

	add(3, 57, -1)
	add(0, 2, 0)
	add(3, 57, -1)
	add(1, 330, 0) // BTN_TOUCH UP
	add(0, 2, 0)
	
	batch.WriteString(fmt.Sprintf("sendevent %s 0 0 0", device))

	fmt.Println("Executing SendEvent pinch out batch...")
	client.Shell(batch.String())
	fmt.Println("Done. Did the emulator zoom out?")
}
