package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"
	"strings"

	"gocv.io/x/gocv"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run scripts/visualize_troops.go <image_path>")
		return
	}

	imgPath := os.Args[1]
	img := gocv.IMRead(imgPath, gocv.IMReadColor)
	if img.Empty() {
		fmt.Printf("Failed to read image: %s\n", imgPath)
		return
	}
	defer img.Close()

	output := img.Clone()
	defer output.Close()

	// Load ground truth labels
	var lConf struct {
		Slots []struct {
			X    int    `json:"x"`
			Name string `json:"name"`
		} `json:"slots"`
	}
	labelData, err := os.ReadFile("assets/manual_labels.json")
	if err == nil {
		json.Unmarshal(labelData, &lConf)
	}

	// Load geometry
	var mConf struct {
		CardWidth  int `json:"card_width"`
		CardHeight int `json:"card_height"`
		SlotY      int `json:"slot_y"`
	}
	geomData, _ := os.ReadFile("assets/manual_slots.json")
	json.Unmarshal(geomData, &mConf)

	fmt.Fprintf(os.Stderr, "Visualizing with Ground-Truth Manual Labels...\n")

	for i, slot := range lConf.Slots {
		rect := image.Rect(slot.X-mConf.CardWidth/2, mConf.SlotY-mConf.CardHeight/2, slot.X+mConf.CardWidth/2, mConf.SlotY+mConf.CardHeight/2)
		
		drawColor := color.RGBA{0, 255, 0, 255}
		isLabelEmpty := strings.ToLower(slot.Name) == "empty"
		if isLabelEmpty {
			drawColor = color.RGBA{0, 0, 255, 180}
		}
		
		gocv.Rectangle(&output, rect, drawColor, 2)
		
		label := slot.Name
		if isLabelEmpty { label = "EMPTY" }
		
		tsize := gocv.GetTextSize(label, gocv.FontHersheySimplex, 0.4, 1)
		
		// Perfect label placement: Inside top, alternating to inside bottom if even/odd
		textX := rect.Min.X + (rect.Dx()-tsize.X)/2
		textY := rect.Min.Y + 15
		if i%2 == 1 { textY = rect.Max.Y - 5 }
		
		bg := image.Rect(textX-2, textY-tsize.Y-2, textX+tsize.X+2, textY+2)
		gocv.Rectangle(&output, bg, color.RGBA{0, 0, 0, 255}, -1)
		gocv.PutText(&output, label, image.Pt(textX, textY), gocv.FontHersheySimplex, 0.4, color.RGBA{255, 255, 255, 255}, 1)
		
		fmt.Fprintf(os.Stderr, "Slot %d (X=%d): %s\n", i+1, slot.X, slot.Name)
	}

	gocv.IMWrite("troop_debug_visualization_groundtruth.png", output)
}
