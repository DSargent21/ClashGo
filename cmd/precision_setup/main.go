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
	centerPt   image.Point
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

	fmt.Println("\n--- ULTIMATE PRECISION SETUP (MIRRORED SINGLE-EDGE FLOW) ---")
	fmt.Println("Configure BottomLeft reference edge, and we will mirror to all other sides.")
	fmt.Println("\nControls: 'r' to reset, 'u' to undo last click, 's' to save, 'q' to quit.")
	
	step := 0
	win.SetMouseHandler(func(event int, x, y int, flags int, userdata interface{}) {
		if event == 1 { // LBUTTONDOWN
			p := image.Pt(x, y)
			
			switch step {
			case 0: // Troop Line Start
				tempPoints = append(tempPoints, p)
				fmt.Printf("• Troop Line START set to %v\n", p)
			case 1: // Troop Line End
				config.Edges["BottomLeft"] = ManualEdge{P1: tempPoints[len(tempPoints)-1], P2: p}
				fmt.Printf("✓ Troop Line END set to %v\n", p)
			case 2: // Hero/Siege Target
				config.HeroTargets["BottomLeft"] = p
				fmt.Printf("✓ Hero/Siege Target set to %v\n", p)
			case 3: // Spell Line A Start
				tempPoints = append(tempPoints, p)
				fmt.Printf("• Spell Line A START set to %v\n", p)
			case 4: // Spell Line A End
				config.SpellEdgesA["BottomLeft"] = ManualEdge{P1: tempPoints[len(tempPoints)-1], P2: p}
				fmt.Printf("✓ Spell Line A END set to %v\n", p)
			case 5: // Spell Line B Start
				tempPoints = append(tempPoints, p)
				fmt.Printf("• Spell Line B START set to %v\n", p)
			case 6: // Spell Line B End
				config.SpellEdgesB["BottomLeft"] = ManualEdge{P1: tempPoints[len(tempPoints)-1], P2: p}
				fmt.Printf("✓ Spell Line B END set to %v\n", p)
			case 7: // Village Center
				centerPt = p
				fmt.Printf("✓ Village Center set to %v. Mirroring to other sides...\n", p)
				mirrorPlacements()
			case 8: // Safety Bar Y
				config.BarY = y
				fmt.Printf("✓ Safety BarY set to %d\n", y)
				fmt.Println("\nALL POINTS SET! Press 's' to save or 'r' to reset.")
			}
			step++
		}
	}, nil)

	for {
		display := img.Clone()
		
		msg := ""
		switch step {
		case 0: msg = "CLICK TROOP LINE START (BottomLeft)"
		case 1: msg = "CLICK TROOP LINE END (BottomLeft)"
		case 2: msg = "CLICK HERO/SIEGE TARGET Point (BottomLeft)"
		case 3: msg = "CLICK SPELL LINE A START (BottomLeft)"
		case 4: msg = "CLICK SPELL LINE A END (BottomLeft)"
		case 5: msg = "CLICK SPELL LINE B START (BottomLeft)"
		case 6: msg = "CLICK SPELL LINE B END (BottomLeft)"
		case 7: msg = "CLICK VILLAGE CENTER POINT (Town Hall)"
		case 8: msg = "CLICK TOP OF TROOP BAR (Safety limit)"
		default: msg = "ALL DONE! PRESS 'S' TO SAVE"
		}
		
		gocv.PutText(&display, msg, image.Pt(20, 40), gocv.FontHersheySimplex, 0.7, color.RGBA{0, 255, 255, 255}, 2)
		gocv.PutText(&display, "'U' to UNDO last click | 'R' to RESET", image.Pt(20, img.Rows()-20), gocv.FontHersheySimplex, 0.5, color.RGBA{200, 200, 200, 255}, 1)

		// Draw Troop Lines (Green)
		for name, e := range config.Edges {
			gocv.Line(&display, e.P1, e.P2, color.RGBA{0, 255, 0, 255}, 2)
			gocv.PutText(&display, name, e.P1, gocv.FontHersheySimplex, 0.4, color.RGBA{0, 255, 0, 255}, 1)
		}
		// Draw Spell Lines A (Purple/Pink)
		for _, e := range config.SpellEdgesA {
			gocv.Line(&display, e.P1, e.P2, color.RGBA{255, 0, 255, 255}, 2)
		}
		// Draw Spell Lines B (Light Purple)
		for _, e := range config.SpellEdgesB {
			gocv.Line(&display, e.P1, e.P2, color.RGBA{200, 0, 200, 255}, 2)
		}
		// Draw Hero Targets (Blue)
		for _, p := range config.HeroTargets {
			gocv.Circle(&display, p, 10, color.RGBA{255, 0, 0, 255}, 2) 
		}
		// Draw Village Center (Red Cross)
		if step > 7 {
			gocv.Line(&display, image.Pt(centerPt.X-15, centerPt.Y), image.Pt(centerPt.X+15, centerPt.Y), color.RGBA{0, 0, 255, 255}, 2)
			gocv.Line(&display, image.Pt(centerPt.X, centerPt.Y-15), image.Pt(centerPt.X, centerPt.Y+15), color.RGBA{0, 0, 255, 255}, 2)
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
			centerPt = image.Point{}
		} else if key == 'u' && step > 0 {
			step--
			switch step {
			case 8:
				config.BarY = 0
			case 7:
				centerPt = image.Point{}
				// Clear mirrored placements
				for _, name := range []string{"BottomRight", "TopLeft", "TopRight"} {
					delete(config.Edges, name)
					delete(config.HeroTargets, name)
					delete(config.SpellEdgesA, name)
					delete(config.SpellEdgesB, name)
				}
			case 0, 3, 5:
				if len(tempPoints) > 0 {
					tempPoints = tempPoints[:len(tempPoints)-1]
				}
			case 1: delete(config.Edges, "BottomLeft")
			case 2: delete(config.HeroTargets, "BottomLeft")
			case 4: delete(config.SpellEdgesA, "BottomLeft")
			case 6: delete(config.SpellEdgesB, "BottomLeft")
			}
			fmt.Printf("↶ Undid last step. Back to step %d\n", step+1)
		} else if key == 's' && step == 9 {
			config.Width = img.Cols()
			config.Height = img.Rows()
			data, _ := json.MarshalIndent(config, "", "  ")
			os.WriteFile("assets/precision_config.json", data, 0644)
			fmt.Println("\n✅ SAVED assets/precision_config.json")
			break
		}
	}
}

func mirrorPlacements() {
	Cx, Cy := centerPt.X, centerPt.Y

	// Reference BottomLeft data
	blEdge := config.Edges["BottomLeft"]
	blSpellA := config.SpellEdgesA["BottomLeft"]
	blSpellB := config.SpellEdgesB["BottomLeft"]
	blHero := config.HeroTargets["BottomLeft"]

	// 1. BottomRight (BR) - Horizontal Mirror
	config.Edges["BottomRight"] = ManualEdge{
		P1: image.Pt(2*Cx-blEdge.P2.X, blEdge.P2.Y),
		P2: image.Pt(2*Cx-blEdge.P1.X, blEdge.P1.Y),
	}
	config.SpellEdgesA["BottomRight"] = ManualEdge{
		P1: image.Pt(2*Cx-blSpellA.P2.X, blSpellA.P2.Y),
		P2: image.Pt(2*Cx-blSpellA.P1.X, blSpellA.P1.Y),
	}
	config.SpellEdgesB["BottomRight"] = ManualEdge{
		P1: image.Pt(2*Cx-blSpellB.P2.X, blSpellB.P2.Y),
		P2: image.Pt(2*Cx-blSpellB.P1.X, blSpellB.P1.Y),
	}
	config.HeroTargets["BottomRight"] = image.Pt(2*Cx-blHero.X, blHero.Y)

	// 2. TopLeft (TL) - Vertical Mirror
	config.Edges["TopLeft"] = ManualEdge{
		P1: image.Pt(blEdge.P2.X, 2*Cy-blEdge.P2.Y),
		P2: image.Pt(blEdge.P1.X, 2*Cy-blEdge.P1.Y),
	}
	config.SpellEdgesA["TopLeft"] = ManualEdge{
		P1: image.Pt(blSpellA.P2.X, 2*Cy-blSpellA.P2.Y),
		P2: image.Pt(blSpellA.P1.X, 2*Cy-blSpellA.P1.Y),
	}
	config.SpellEdgesB["TopLeft"] = ManualEdge{
		P1: image.Pt(blSpellB.P2.X, 2*Cy-blSpellB.P2.Y),
		P2: image.Pt(blSpellB.P1.X, 2*Cy-blSpellB.P1.Y),
	}
	config.HeroTargets["TopLeft"] = image.Pt(blHero.X, 2*Cy-blHero.Y)

	// 3. TopRight (TR) - Diagonal Mirror (Horizontal + Vertical)
	config.Edges["TopRight"] = ManualEdge{
		P1: image.Pt(2*Cx-blEdge.P2.X, 2*Cy-blEdge.P2.Y),
		P2: image.Pt(2*Cx-blEdge.P1.X, 2*Cy-blEdge.P1.Y),
	}
	config.SpellEdgesA["TopRight"] = ManualEdge{
		P1: image.Pt(2*Cx-blSpellA.P2.X, 2*Cy-blSpellA.P2.Y),
		P2: image.Pt(2*Cx-blSpellA.P1.X, 2*Cy-blSpellA.P1.Y),
	}
	config.SpellEdgesB["TopRight"] = ManualEdge{
		P1: image.Pt(2*Cx-blSpellB.P2.X, 2*Cy-blSpellB.P2.Y),
		P2: image.Pt(2*Cx-blSpellB.P1.X, 2*Cy-blSpellB.P1.Y),
	}
	config.HeroTargets["TopRight"] = image.Pt(2*Cx-blHero.X, 2*Cy-blHero.Y)
}
