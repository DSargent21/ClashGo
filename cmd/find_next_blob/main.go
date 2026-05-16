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

	fmt.Printf("Analyzing %dx%d screen for Silver blobs (Next button)...\n", screen.Cols(), screen.Rows())

	// Silver range (BGR)
	lower := gocv.NewScalar(150, 150, 150, 0)
	upper := gocv.NewScalar(220, 220, 220, 0)
	
	mask := gocv.NewMat()
	defer mask.Close()
	gocv.InRangeWithScalar(screen, lower, upper, &mask)

	// Clean up mask
	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(3, 3))
	defer kernel.Close()
	gocv.MorphologyEx(mask, &mask, gocv.MorphOpen, kernel)

	contours := gocv.FindContours(mask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	fmt.Println("\n--- SILVER Blobs (Possible Next Button) ---")
	found := 0
	for i := 0; i < contours.Size(); i++ {
		area := gocv.ContourArea(contours.At(i))
		if area < 500 { continue } 

		rect := gocv.BoundingRect(contours.At(i))
		
		// Only look in the bottom-right quadrant
		if rect.Min.X < 400 || rect.Min.Y < 400 { continue }

		center := image.Pt(rect.Min.X+rect.Dx()/2, rect.Min.Y+rect.Dy()/2)
		fmt.Printf("Blob %d: Center=(%d, %d) Area=%.0f  Bounds=%v\n", 
			i, center.X, center.Y, area, rect)
		found++
	}
	if found == 0 {
		fmt.Println("No large silver blobs found in bottom-right.")
	}
}
