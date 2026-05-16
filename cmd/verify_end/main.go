package main

import (
	"fmt"
	"os"

	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run cmd/verify_end/main.go <screenshot.png>")
		return
	}

	path := os.Args[1]
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		fmt.Printf("Error: Could not read image at %s\n", path)
		return
	}
	defer img.Close()

	// 1. Setup minimal calibration and templates
	// We use 860x732 as the reference resolution for the provided ROIs
	cal := &game.Calibration{ScaleX: float64(img.Cols()) / 860.0, ScaleY: float64(img.Rows()) / 732.0}
	ts, err := game.NewTemplateStore("assets/templates")
	if err != nil {
		fmt.Printf("Error loading templates: %v\n", err)
		return
	}
	if err := ts.LoadTemplates(); err != nil {
		fmt.Printf("Error loading templates: %v\n", err)
		return
	}
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

	// 2. Run the recognizer
	lr := game.NewLootRecognizer(cal, ts, logger)
	defer lr.Close()

	// DEBUG: Check star pixel colors
	starPoints := []struct { name string; x, y int }{
		{"Left Star", 327, 205},
		{"Middle Star", 430, 196},
		{"Right Star", 535, 210},
	}
	fmt.Println("\n--- Star Pixel Debug ---")
	for _, p := range starPoints {
		sx, sy := cal.ScaleRef(p.x, p.y)
		if sx >= 0 && sx < img.Cols() && sy >= 0 && sy < img.Rows() {
			b := img.GetUCharAt(sy, sx*3)
			g := img.GetUCharAt(sy, sx*3+1)
			r := img.GetUCharAt(sy, sx*3+2)
			fmt.Printf("%s at (%d, %d): RGB(%d, %d, %d) Sum: %d\n", p.name, sx, sy, r, g, b, int(r)+int(g)+int(b))
		}
	}

	fmt.Println("\n🔍 Analyzing End Attack Screen...")
	result, err := lr.ReadBattleResult(img)
	if err != nil {
		fmt.Printf("❌ Error: %v\n", err)
		return
	}

	fmt.Println("\n=== RESULTS ===")
	fmt.Printf("Stars detected: %d\n", result.Stars)
	fmt.Println("\n--- Battle Loot ---")
	fmt.Printf("Gold:   %d\n", result.Loot.Gold)
	fmt.Printf("Elixir: %d\n", result.Loot.Elixir)
	fmt.Printf("DE:     %d\n", result.Loot.DarkElixir)

	fmt.Println("\n--- Bonus Loot ---")
	fmt.Printf("Gold:   %d\n", result.Bonus.Gold)
	fmt.Printf("Elixir: %d\n", result.Bonus.Elixir)
	fmt.Printf("DE:     %d\n", result.Bonus.DarkElixir)
	
	fmt.Println("\n--- Session Tracking Simulation ---")
	totalGold := result.Loot.Gold + result.Bonus.Gold
	totalElixir := result.Loot.Elixir + result.Bonus.Elixir
	totalDE := result.Loot.DarkElixir + result.Bonus.DarkElixir
	fmt.Printf("Total Gold:   %d\n", totalGold)
	fmt.Printf("Total Elixir: %d\n", totalElixir)
	fmt.Printf("Total DE:     %d\n", totalDE)
}
