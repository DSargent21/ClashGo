package main

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
)

func main() {
	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = "localhost:5555"

	fmt.Println("== Shell(wm size) — should auto-fallback via wedge prefix")
	out, err := client.Shell("wm size")
	fmt.Printf("out=%q err=%v\n", out, err)

	fmt.Println("== CaptureScreen (exec service, should fallback via shell screencap)")
	buf, err := client.CaptureScreen()
	fmt.Printf("bytes=%d err=%v\n", len(buf), err)
	if err == nil && len(buf) >= 12 {
		w := binary.LittleEndian.Uint32(buf[0:4])
		h := binary.LittleEndian.Uint32(buf[4:8])
		fmt.Printf("dimensions=%dx%d (want 860x732)\n", w, h)
	}

	fmt.Println("== Shell(pm list packages | head -2)")
	out, err = client.Shell("pm list packages | head -2")
	fmt.Printf("out=%q err=%v\n", out, err)

	fmt.Println("== Tap (input tap via shell)")
	err = client.TapFast(430, 400, 0)
	fmt.Printf("tap err=%v\n", err)

	fmt.Println("== ScreenSize (full parse path)")
	w, h, err := client.ScreenSize()
	fmt.Printf("ScreenSize: %dx%d err=%v\n", w, h, err)
}
