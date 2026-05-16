package main

import (
	"fmt"
	"image"
	"os"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/config"
	"gocv.io/x/gocv"
)

func main() {
	cfg := config.LoadOrDefault("config.json")
	client := adb.NewClient(func(c *adb.Client) {
		c.DeviceID = cfg.Device.DeviceID
	})
	if err := client.Connect(); err != nil {
		fmt.Printf("❌ Connection failed: %v\n", err)
		os.Exit(1)
	}

	screen, err := client.CaptureToMat()
	if err != nil {
		fmt.Printf("❌ Capture failed: %v\n", err)
		os.Exit(1)
	}
	defer screen.Close()

	if screen.Empty() {
		fmt.Println("❌ Capture is empty")
		return
	}

	fmt.Printf("Searching for SILVER in bottom-right of %dx%d screen...\n", screen.Cols(), screen.Rows())

	// Silver: R, G, B are similar and relatively high
	// We'll use HSV for better robustness
	hsv := gocv.NewMat()
	defer hsv.Close()
	gocv.CvtColor(screen, &hsv, gocv.ColorBGRToHSV)

	// Silver/Grey in HSV: Saturation is very low, Value is high
	lower := gocv.NewScalar(0, 0, 150, 0)
	upper := gocv.NewScalar(180, 50, 255, 0)
	
	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(hsv, lower, upper, &mask)

	// Focus bottom-right
	roi := image.Rect(400, 400, screen.Cols(), screen.Rows())
	sub := mask.Region(roi)
	defer sub.Close()

	contours := gocv.FindContours(sub, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	for i := 0; i < contours.Size(); i++ {
		area := gocv.ContourArea(contours.At(i))
		if area < 1000 { continue }
		
		rect := gocv.BoundingRect(contours.At(i))
		center := image.Pt(rect.Min.X+rect.Dx()/2 + roi.Min.X, rect.Min.Y+rect.Dy()/2 + roi.Min.Y)
		fmt.Printf("Found Silver Blob: Center=(%d, %d) Area=%.0f\n", center.X, center.Y, area)
	}
}
