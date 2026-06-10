package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"

	"gocv.io/x/gocv"
)

type ManualLabelConfig struct {
	Slots []SlotLabel `json:"slots"`
}

type SlotLabel struct {
	X    int    `json:"x"`
	Name string `json:"name"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run scripts/label_slots.go <attack_screenshot.png>")
		return
	}

	path := os.Args[1]
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		fmt.Printf("Error: Could not read %s\n", path)
		return
	}
	defer img.Close()

	// Load slot centers from previous manual calibration
	var mConf struct {
		CardWidth  int   `json:"card_width"`
		CardHeight int   `json:"card_height"`
		SlotXs     []int `json:"slot_xs"`
		SlotY      int   `json:"slot_y"`
	}
	data, err := os.ReadFile("assets/manual_slots.json")
	if err != nil {
		fmt.Println("Error: Please run scripts/calibrate_slots_manual.go first!")
		return
	}
	json.Unmarshal(data, &mConf)

	win := gocv.NewWindow("MANUAL TROOP LABELING")
	defer win.Close()

	units := []string{
		"Valkyrie", "Electro Dragon", "Balloon", "Archer Queen", 
		"Minion Prince", "Grand Warden", "Dragon Duke", 
		"Rage Spell", "Freeze Spell", "Ice Spell", 
		"Barbarian King", "Royal Champion", "Siege Machine", 
		"Earthquake Spell", "Event Troop", "Empty",
	}

	fmt.Println("\n--- MANUAL TROOP LABELING ---")
	fmt.Println("For each slot, press the KEY matching the unit:")
	menuKeys := "v0123456789abcde"
	for i, u := range units {
		fmt.Printf("[%c] %s\n", menuKeys[i], u)
	}
	fmt.Println("\nControls: ARROWS to nudge slot | 's' to save | 'q' to quit")

	labels := make(map[int]string)
	currentSlot := 0

	for {
		display := img.Clone()
		
		// Draw all boxes
		for i := range mConf.SlotXs {
			x := mConf.SlotXs[i]
			rect := image.Rect(x-mConf.CardWidth/2, mConf.SlotY-mConf.CardHeight/2, x+mConf.CardWidth/2, mConf.SlotY+mConf.CardHeight/2)
			c := color.RGBA{255, 255, 255, 255}
			if i == currentSlot {
				c = color.RGBA{0, 255, 255, 255}
				gocv.Rectangle(&display, rect, c, 3)
			} else {
				gocv.Rectangle(&display, rect, c, 1)
			}

			if name, ok := labels[i]; ok {
				gocv.PutText(&display, name, image.Pt(rect.Min.X, rect.Max.Y+20), gocv.FontHersheySimplex, 0.4, color.RGBA{0, 255, 0, 255}, 1)
			}
		}

		msg := fmt.Sprintf("LABEL SLOT %d: PRESS KEY (Arrows to nudge)", currentSlot+1)
		if currentSlot >= len(mConf.SlotXs) {
			msg = "ALL LABELED! PRESS 'S' TO SAVE"
		}
		gocv.PutText(&display, msg, image.Pt(20, 40), gocv.FontHersheySimplex, 0.7, color.RGBA{0, 255, 255, 255}, 2)

		win.IMShow(display)
		key := win.WaitKey(10)
		display.Close()

		if key == 'q' || key == 27 {
			break
		}
		
		// Nudge Logic
		if currentSlot < len(mConf.SlotXs) {
			if key == 2 || key == 65362 { // Up
				mConf.SlotY--
			} else if key == 3 || key == 65364 { // Down
				mConf.SlotY++
			} else if key == 0 || key == 65361 { // Left
				mConf.SlotXs[currentSlot]--
			} else if key == 1 || key == 65363 { // Right
				mConf.SlotXs[currentSlot]++
			}
		}

		if currentSlot < len(mConf.SlotXs) {
			char := string(rune(key))
			idx := -1
			for i, c := range menuKeys {
				if string(c) == char {
					idx = i
					break
				}
			}

			if idx >= 0 && idx < len(units) {
				labels[currentSlot] = units[idx]
				fmt.Printf("✓ Slot %d set to %s\n", currentSlot+1, units[idx])
				currentSlot++
			}
		} else if key == 's' && currentSlot >= len(mConf.SlotXs) {
			// Save new positions to manual_slots.json too
			outSlots, _ := json.MarshalIndent(mConf, "", "  ")
			os.WriteFile("assets/manual_slots.json", outSlots, 0644)

			var final ManualLabelConfig
			for i, x := range mConf.SlotXs {
				final.Slots = append(final.Slots, SlotLabel{X: x, Name: labels[i]})
			}
			out, _ := json.MarshalIndent(final, "", "  ")
			os.WriteFile("assets/manual_labels.json", out, 0644)
			fmt.Println("\n✅ SAVED assets/manual_labels.json and assets/manual_slots.json")
			break
		}
	}
}
