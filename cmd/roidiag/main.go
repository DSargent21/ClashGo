package main

import (
	"fmt"
	"os"

	"gocv.io/x/gocv"
	"github.com/Ducky705/ClashGo/internal/vision"
	"github.com/Ducky705/ClashGo/pkg/strategy"
	"image"
	"math"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: go run main.go <screenshot> <strategy_yaml>")
		return
	}

	screenPath := os.Args[1]
	yamlPath := os.Args[2]

	// 1. Load Strategy
	s, err := strategy.ParseYAML(yamlPath)
	if err != nil {
		fmt.Printf("Error loading strategy: %v\n", err)
		return
	}

	// 2. Load Screen
	screen := gocv.IMRead(screenPath, gocv.IMReadColor)
	if screen.Empty() {
		fmt.Printf("Error reading screen: %s\n", screenPath)
		return
	}
	defer screen.Close()

	// 3. Find Edges
	top, bottom, left, right, err := vision.CalculateBaseEdges(screen, 500)
	if err != nil {
		fmt.Printf("Error calculating edges: %v\n", err)
		return
	}

	fmt.Printf("\n🚀 DRY RUN: Strategy '%s'\n", s.Name)
	fmt.Printf("Detected Base Diamond: Top%v, Bottom%v, Left%v, Right%v\n", top, bottom, left, right)

	// 4. Simulate Phases
	for _, phase := range s.Phases {
		fmt.Printf("\n--- Phase: %s ---\n", phase.Name)
		
		p1, p2 := calculateDeploymentArea(s.TargetEdge, phase.Offset, top, bottom, left, right)

		for _, unit := range phase.Units {
			fmt.Printf("  [Unit] %s (Amount: %s)\n", unit.Name, unit.Amount)
			
			if phase.Pattern == "Line" {
				fmt.Printf("  [Action] adb shell input swipe %d %d %d %d 300\n", p1.X, p1.Y, p2.X, p2.Y)
			} else {
				mid := image.Point{X: (p1.X + p2.X) / 2, Y: (p1.Y + p2.Y) / 2}
				fmt.Printf("  [Action] adb shell input tap %d %d\n", mid.X, mid.Y)
			}
		}
		if phase.DelayAfterMS > 0 {
			fmt.Printf("  [Wait] %dms\n", phase.DelayAfterMS)
		}
	}
}

func calculateDeploymentArea(edge string, offset int, top, bottom, left, right image.Point) (image.Point, image.Point) {
	var p1, p2 image.Point
	switch edge {
	case "TopRight": p1, p2 = top, right
	case "BottomRight": p1, p2 = right, bottom
	case "BottomLeft": p1, p2 = bottom, left
	case "TopLeft": p1, p2 = left, top
	default: p1, p2 = top, right
	}

	dx := p2.X - p1.X
	dy := p2.Y - p1.Y
	mag := math.Sqrt(float64(dx*dx + dy*dy))
	nx := float64(dy) / mag
	ny := float64(-dx) / mag

	p1.X += int(nx * float64(offset))
	p1.Y += int(ny * float64(offset))
	p2.X += int(nx * float64(offset))
	p2.Y += int(ny * float64(offset))

	return p1, p2
}
