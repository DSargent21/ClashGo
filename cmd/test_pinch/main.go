package main

import (
	"fmt"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
)

func main() {
	client := adb.NewClient(adb.WithHost("127.0.0.1"), adb.WithPort(5037))
	client.DeviceID = "localhost:5555"

	if err := client.Connect(); err != nil {
		return
	}

	fmt.Println("Testing ZoomOut keys...")
	for i := 0; i < 5; i++ {
		client.KeyEvent(20) // DPAD_DOWN
		time.Sleep(100 * time.Millisecond)
	}

	time.Sleep(2 * time.Second)

	fmt.Println("Testing ZoomOut Ctrl+-...")
	client.Shell("input keycombination CTRL_LEFT MINUS")

	fmt.Println("Done")
}
