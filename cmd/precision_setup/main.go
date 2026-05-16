package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"os"

	"gocv.io/x/gocv"
)

type PrecisionConfig struct {
	Edges        map[string]ManualEdge   `json:"edges"`
	SpellEdgesA  map[string]ManualEdge   `json:"spell_edges_a"`
	SpellEdgesB  map[string]ManualEdge   `json:"spell_edges_b"`
	HeroTargets  map[string]image.Point  `json:"hero_targets"`
	BarY         int                    `json:"bar_y"`
	Width        int                    `json:"width"`
	Height       int                    `json:"height"`
}

type ManualEdge struct {
	P1 image.Point `json:"p1"`
	P2 image.Point `json:"p2"`
}

var (
	config = PrecisionConfig{
		Edges:        make(map[string]ManualEdge),
		SpellEdgesA:  make(map[string]ManualEdge),
		SpellEdgesB:  make(map[string]ManualEdge),
		HeroTargets:  make(map[string]image.Point),
	}
	tempPoints []image.Point
	edgeNames  = []string{"TopRight", "BottomRight", "BottomLeft", "TopLeft"}
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <battle_screenshot.png>")
		return
	}

	path := os.Args[1]
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		fmt.Printf("Error: Could not read %s\n", path)
		return
	}
	defer img.Close()

	win := gocv.NewWindow("ULTIMATE PRECISION SETUP")
	defer win.Close()

	fmt.Println("\n--- ULTIMATE PRECISION SETUP (DOUBLE SPELL LINES) ---")
	fmt.Println("Define lines for troops, heroes, and TWO spell lines for all 4 sides.")
	fmt.Println("\nControls: 'r' to reset, 'u' to undo last click, 's' to save, 'q' to quit.")
	
	step := 0
	win.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if event == 1 { // LBUTTONDOWN
			p := image.Pt(x, y)
			
			// Setup for each edge (7 clicks per edge)
			edgeIdx := step / 7
			subStep := step % 7
			
			if edgeIdx < 4 {
				name := edgeNames[edgeIdx]
				switch subStep {
				case 0: // Troop Line Start
					tempPoints = append(tempPoints, p)
					fmt.Printf("• [%s] Troop Line START set to %v\n", name, p)
				case 1: // Troop Line End
					config.Edges[name] = ManualEdge{P1: tempPoints[len(tempPoints)-1], P2: p}
					fmt.Printf("✓ [%s] Troop Line END set to %v\n", name, p)
				case 2: // Hero/Siege Target
					config.HeroTargets[name] = p
					fmt.Printf("✓ [%s] Hero/Siege Target set to %v\n", name, p)
				case 3: // Spell Line A Start
					tempPoints = append(tempPoints, p)
					fmt.Printf("• [%s] Spell Line A START set to %v\n", name, p)
				case 4: // Spell Line A End
					config.SpellEdgesA[name] = ManualEdge{P1: tempPoints[len(tempPoints)-1], P2: p}
					fmt.Printf("✓ [%s] Spell Line A END set to %v\n", name, p)
				case 5: // Spell Line B Start
					tempPoints = append(tempPoints, p)
					fmt.Printf("• [%s] Spell Line B START set to %v\n", name, p)
				case 6: // Spell Line B End
					config.SpellEdgesB[name] = ManualEdge{P1: tempPoints[len(tempPoints)-1], P2: p}
					fmt.Printf("✓ [%s] Spell Line B END set to %v\n", name, p)
				}
				step++
				return
			}
			
			// Final step: Safety Bar
			if step == 28 {
				config.BarY = y
				fmt.Printf("✓ Safety BarY set to %d\n", y)
				step++
				fmt.Println("\nALL POINTS SET! Press 's' to save or 'r' to reset.")
			}
		}
	}, nil)

	for {
		display := img.Clone()
		
		msg := ""
		if step < 28 {
			edgeIdx := step / 7
			subStep := step % 7
			name := edgeNames[edgeIdx]
			switch subStep {
			case 0: msg = fmt.Sprintf("[%s] CLICK TROOP LINE START", name)
			case 1: msg = fmt.Sprintf("[%s] CLICK TROOP LINE END", name)
			case 2: msg = fmt.Sprintf("[%s] CLICK HERO/SIEGE TARGET (Point)", name)
			case 3: msg = fmt.Sprintf("[%s] CLICK SPELL LINE A START", name)
			case 4: msg = fmt.Sprintf("[%s] CLICK SPELL LINE A END", name)
			case 5: msg = fmt.Sprintf("[%s] CLICK SPELL LINE B START", name)
			case 6: msg = fmt.Sprintf("[%s] CLICK SPELL LINE B END", name)
			}
		} else if step == 28 {
			msg = "CLICK TOP OF TROOP BAR (Safety limit)"
		} else {
			msg = "ALL DONE! PRESS 'S' TO SAVE"
		}
		
		gocv.PutText(&display, msg, image.Pt(20, 40), gocv.FontHersheySimplex, 0.7, color.RGBA{0, 255, 255, 255}, 2)
		gocv.PutText(&display, "'U' to UNDO last click | 'R' to RESET", image.Pt(20, img.Rows()-20), gocv.FontHersheySimplex, 0.5, color.RGBA{200, 200, 200, 255}, 1)

		// Draw Troop Lines (Green)
		for _, e := range config.Edges {
			gocv.Line(&display, e.P1, e.P2, color.RGBA{0, 255, 0, 255}, 2)
		}
		// Draw Spell Lines (Purple)
		for _, e := range config.SpellEdgesA {
			gocv.Line(&display, e.P1, e.P2, color.RGBA{255, 0, 255, 255}, 2)
		}
		for _, e := range config.SpellEdgesB {
			gocv.Line(&display, e.P1, e.P2, color.RGBA{200, 0, 200, 255}, 2)
		}
		// Draw Hero Targets (Red)
		for _, p := range config.HeroTargets {
			gocv.Circle(&display, p, 10, color.RGBA{0, 0, 255, 255}, 2) 
		}
		if config.BarY > 0 {
			gocv.Line(&display, image.Pt(0, config.BarY), image.Pt(img.Cols(), config.BarY), color.RGBA{0, 0, 255, 255}, 2)
		}

		win.IMShow(display)
		key := win.WaitKey(10)
		display.Close()

		if key == 'q' {
			break
		} else if key == 'r' {
			step = 0
			config.Edges = make(map[string]ManualEdge)
			config.SpellEdgesA = make(map[string]ManualEdge)
			config.SpellEdgesB = make(map[string]ManualEdge)
			config.HeroTargets = make(map[string]image.Point)
			config.BarY = 0
			tempPoints = nil
		} else if key == 'u' && step > 0 {
			step--
			if step == 28 {
				config.BarY = 0
			} else {
				edgeIdx := step / 7
				subStep := step % 7
				name := edgeNames[edgeIdx]
				switch subStep {
				case 0, 3, 5: // These added to tempPoints
					if len(tempPoints) > 0 {
						tempPoints = tempPoints[:len(tempPoints)-1]
					}
				case 1: delete(config.Edges, name)
				case 2: delete(config.HeroTargets, name)
				case 4: delete(config.SpellEdgesA, name)
				case 6: delete(config.SpellEdgesB, name)
				}
			}
			fmt.Printf("↶ Undid last step. Back to step %d\n", step+1)
		} else if key == 's' && step == 29 {
			config.Width = img.Cols()
			config.Height = img.Rows()
			data, _ := json.MarshalIndent(config, "", "  ")
			os.WriteFile("assets/precision_config.json", data, 0644)
			fmt.Println("\n✅ SAVED assets/precision_config.json")
			break
		}
	}
}
