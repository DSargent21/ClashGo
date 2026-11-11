package main

// uiact drives the emulator through the bot's own ADB client, because the
// platform hd-adb binary answers "error: closed" on this BlueStacks build while
// the Go client's wedge-safe shell fallback still works.
//
// Usage:
//
//	uiact sh "<command>"   run a shell command
//	uiact tap <x> <y>      tap a pixel
//	uiact key <keycode>    press a key (3 = HOME, 4 = BACK)
//	uiact launch <pkg>     launch the app's launcher activity
import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/Ducky705/ClashGO/internal/adb"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: uiact sh|tap|key|launch ...")
		os.Exit(2)
	}
	client := adb.NewClient(
		adb.WithHost("127.0.0.1"),
		adb.WithPort(5037),
		adb.WithTimeout(30*time.Second),
	)
	client.DeviceID = "localhost:5555"

	var err error
	switch os.Args[1] {
	case "sh":
		var out string
		out, err = client.Shell(os.Args[2])
		if err == nil {
			fmt.Print(out)
		}
	case "tap":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "tap needs x y")
			os.Exit(2)
		}
		x, _ := strconv.Atoi(os.Args[2])
		y, _ := strconv.Atoi(os.Args[3])
		err = client.TapFast(x, y, 60)
		if err == nil {
			fmt.Printf("tapped %d,%d\n", x, y)
		}
	case "key":
		code, _ := strconv.Atoi(os.Args[2])
		_, err = client.Shell(fmt.Sprintf("input keyevent %d", code))
		if err == nil {
			fmt.Printf("key %d\n", code)
		}
	case "launch":
		_, err = client.Shell("monkey -p " + os.Args[2] + " -c android.intent.category.LAUNCHER 1")
		if err == nil {
			fmt.Println("launch sent")
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown action", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
