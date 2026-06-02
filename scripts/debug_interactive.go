package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"strings"

	"github.com/Ducky705/ClashGo/internal/adb"
	"github.com/Ducky705/ClashGo/internal/game"
	"github.com/Ducky705/ClashGo/internal/vision"
	"github.com/Ducky705/ClashGo/internal/config"
	"github.com/Ducky705/ClashGo/internal/attack"
	"github.com/Ducky705/ClashGo/pkg/strategy"
	"github.com/rs/zerolog"
	"gocv.io/x/gocv"
)

type ManualEdge struct {
	P1 image.Point `json:"p1"`
	P2 image.Point `json:"p2"`
}

type PrecisionConfig struct {
	Edges        map[string]ManualEdge   `json:"edges"`
	SpellEdgesA  map[string]ManualEdge   `json:"spell_edges_a"`
	SpellEdgesB  map[string]ManualEdge   `json:"spell_edges_b"`
	HeroTargets  map[string]image.Point  `json:"hero_targets"`
	BarY         int                    `json:"bar_y"`
	Width        int                    `json:"width"`
	Height       int                    `json:"height"`
}

func main() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("============================================")
	fmt.Println("   ClashGo Interactive Deployment Debugger   ")
	fmt.Println("============================================")

	// 1. Initialize ADB Client
	client := adb.NewClient(func(c *adb.Client) {
		c.DeviceID = "localhost:5555" // Try default local BlueStacks port
	})
	
	if err := client.AutoDetectDevice(); err != nil {
		fmt.Printf("Device auto-detect info: %v\n", err)
	}

	adbConnected := false
	if err := client.Connect(); err == nil {
		fmt.Println("ADB Device connected successfully!")
		adbConnected = true
	} else {
		fmt.Printf("ADB Connection warning: %v. (Live features will be disabled)\n", err)
	}
	defer client.Close()

	for {
		fmt.Println("\n--- Select an Option ---")
		fmt.Println("1) Analyze 'screen_20260515_215234.png' (Static)")
		fmt.Println("2) Analyze 'screen_20260515_215242.png' (Static)")
		fmt.Println("3) Capture live screen & analyze (Live)")
		fmt.Println("4) Execute interactive LIVE DEPLOYMENT on device")
		fmt.Println("5) Exit")
		fmt.Print("Enter option (1-5): ")
		
		input, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		choice := strings.TrimSpace(input)

		if choice == "5" || choice == "exit" || choice == "quit" {
			fmt.Println("Goodbye!")
			break
		}

		switch choice {
		case "1", "2", "3":
			var imgPath string
			var screen gocv.Mat
			if choice == "1" {
				imgPath = "screen_20260515_215234.png"
				screen = gocv.IMRead(imgPath, gocv.IMReadColor)
			} else if choice == "2" {
				imgPath = "screen_20260515_215242.png"
				screen = gocv.IMRead(imgPath, gocv.IMReadColor)
			} else {
				if !adbConnected {
					fmt.Println("Error: ADB is not connected. Connect a device first.")
					continue
				}
				fmt.Println("Capturing live screen...")
				screen, err = client.CaptureToMat()
				if err != nil {
					fmt.Printf("Error capturing screen: %v\n", err)
					continue
				}
				imgPath = "live_captured.png"
				gocv.IMWrite(imgPath, screen)
				fmt.Println("Saved screenshot to live_captured.png")
			}

			if screen.Empty() {
				fmt.Printf("Error: Screen image at %s is empty or not found\n", imgPath)
				continue
			}

			analyzeImage(screen, imgPath, client)
			screen.Close()

		case "4":
			if !adbConnected {
				fmt.Println("Error: ADB is not connected. Connect a device first.")
				continue
			}
			fmt.Print("Are you sure you want to execute live deployment? Ensure the game is in battle! (y/n): ")
			confirmInput, _ := reader.ReadString('\n')
			if strings.ToLower(strings.TrimSpace(confirmInput)) == "y" {
				executeLiveDeployment(client)
			} else {
				fmt.Println("Canceled live deployment.")
			}

		default:
			fmt.Println("Invalid option. Try again.")
		}
	}
}

func analyzeImage(screen gocv.Mat, imgPath string, client *adb.Client) {
	w, h := screen.Cols(), screen.Rows()
	debugImg := screen.Clone()
	defer debugImg.Close()

	// Load Strategy
	stratPath := "assets/strategies/auto_edrag_rush.yaml"
	s, err := strategy.ParseYAML(stratPath)
	if err != nil {
		fmt.Printf("Failed to load strategy: %v\n", err)
		return
	}
	fmt.Printf("\nLoaded Strategy: %s | Target Edge: %s\n", s.Name, s.TargetEdge)

	// Load Calibration & Precision Config
	var pCfg PrecisionConfig
	pData, err := os.ReadFile("assets/precision_config.json")
	if err != nil {
		fmt.Printf("Error loading precision config: %v\n", err)
		return
	}
	if err := json.Unmarshal(pData, &pCfg); err != nil {
		fmt.Printf("Error parsing precision config: %v\n", err)
		return
	}

	scaleX, scaleY := float64(w)/float64(pCfg.Width), float64(h)/float64(pCfg.Height)
	mBarY := int(float64(pCfg.BarY) * scaleY)
	if mBarY > int(float64(h)*0.92) {
		mBarY = int(float64(h) * 0.92)
	}

	cal := &game.Calibration{
		PhysicalW: w,
		PhysicalH: h,
		ScaleX:    scaleX,
		ScaleY:    scaleY,
	}

	// Draw basic layout lines
	// Draw horizontal line at mBarY (Red)
	gocv.Line(&debugImg, image.Pt(0, mBarY), image.Pt(w, mBarY), color.RGBA{0, 0, 255, 255}, 2)
	gocv.PutText(&debugImg, fmt.Sprintf("Bar Limit (Y=%d)", mBarY), image.Pt(10, mBarY-10), gocv.FontHersheySimplex, 0.5, color.RGBA{0, 0, 255, 255}, 1)

	// Draw Diamond bounds if target edge exists
	targetEdge := s.TargetEdge
	if strings.EqualFold(targetEdge, "Random") {
		targetEdge = "TopRight"
	}
	if edge, ok := pCfg.Edges[targetEdge]; ok {
		p1 := image.Pt(int(float64(edge.P1.X)*scaleX), int(float64(edge.P1.Y)*scaleY))
		p2 := image.Pt(int(float64(edge.P2.X)*scaleX), int(float64(edge.P2.Y)*scaleY))
		gocv.Line(&debugImg, p1, p2, color.RGBA{0, 255, 0, 255}, 2)
		gocv.PutText(&debugImg, fmt.Sprintf("Deploy Edge: %s", targetEdge), p1.Add(image.Pt(0, -10)), gocv.FontHersheySimplex, 0.5, color.RGBA{0, 255, 0, 255}, 1)
	}

	globalUsedSlots := make(map[int]bool)

	fmt.Println("\n--- Template Detection Results ---")
	for _, phase := range s.Phases {
		fmt.Printf("Phase [%s]:\n", phase.Name)
		for _, unit := range phase.Units {
			unitName := strings.ToLower(strings.TrimSpace(unit.Name))
			isSpell := strings.Contains(unitName, "spell")
			isSiege := strings.Contains(unitName, "slammer") || strings.Contains(unitName, "siege")
			isHero := strings.Contains(unitName, "king") || strings.Contains(unitName, "queen") || strings.Contains(unitName, "warden") || strings.Contains(unitName, "prince") || strings.Contains(unitName, "duke") || strings.Contains(unitName, "champion")

			fileName := strings.ReplaceAll(unitName, " ", "_")
			tplPath := fmt.Sprintf("assets/templates/attack/%s.png", fileName)
			tpl := gocv.IMRead(tplPath, gocv.IMReadColor)
			if tpl.Empty() {
				fmt.Printf("  - %s: Template file missing at %s\n", unit.Name, tplPath)
				continue
			}

			barROI := image.Rect(0, mBarY, w, h)
			thresholds := []float32{0.80, 0.70, 0.60, 0.55}
			if isHero || isSiege {
				thresholds = []float32{0.75, 0.65, 0.55, 0.50}
			}
			if isSpell {
				thresholds = []float32{0.80, 0.75, 0.70, 0.65}
			}

			var bestMatch *vision.Match
			for _, th := range thresholds {
				matches, _ := vision.MatchMultiScaleROI(screen, tpl, 0.2, 1.2, 20, th, barROI)
				if len(matches) > 0 {
					// Filter out matches that overlap with already used slots
					for _, m := range matches {
						isUsed := false
						for ux := range globalUsedSlots {
							if math.Abs(float64(m.Point.X-ux)) < float64(w)*0.05 {
								isUsed = true
								break
							}
						}
						if !isUsed {
							bestMatch = &m
							break
						}
					}
				}
				if bestMatch != nil {
					break
				}
			}
			tpl.Close()

			if bestMatch != nil {
				globalUsedSlots[bestMatch.Point.X] = true
				fmt.Printf("  ✓ Found %s at X=%d, Y=%d (Conf: %.2f)\n", unit.Name, bestMatch.Point.X, bestMatch.Point.Y, bestMatch.Confidence)
				// Draw green circle on template match
				gocv.Circle(&debugImg, bestMatch.Point, 18, color.RGBA{0, 255, 0, 255}, 2)
				gocv.PutText(&debugImg, unit.Name, bestMatch.Point.Add(image.Pt(-20, -25)), gocv.FontHersheySimplex, 0.4, color.RGBA{0, 255, 0, 255}, 1)
			} else {
				if phase.Name != "Heroes" || !strings.Contains(unit.Name, "Ability") {
					fmt.Printf("  ✗ NOT FOUND: %s\n", unit.Name)
				}
			}
		}
	}

	fmt.Println("\n--- Slot Sweep Fallback Detection (For event/unmatched units) ---")
	slotY := mBarY + (h-mBarY)/2
	step := int(75.0 * cal.ScaleX)
	startX := int(40.0 * cal.ScaleX)

	for x := startX; x < w-20; x += step {
		// Check overlap
		alreadyUsed := false
		for ux := range globalUsedSlots {
			if math.Abs(float64(x-ux)) < float64(w)*0.04 {
				alreadyUsed = true
				break
			}
		}
		if alreadyUsed {
			continue
		}

		// isSlotEmpty implementation check
		size := int(float64(h) * 0.03)
		region := image.Rect(x-size, slotY-size, x+size, slotY+size)
		if region.Min.X < 0 { region.Min.X = 0 }
		if region.Min.Y < 0 { region.Min.Y = 0 }
		if region.Max.X > w { region.Max.X = w }
		if region.Max.Y > h { region.Max.Y = h }

		sub := screen.Region(region)
		hsv := gocv.NewMat()
		gocv.CvtColor(sub, &hsv, gocv.ColorBGRToHSV)
		
		lower := gocv.NewScalar(90, 20, 30, 0)
		upper := gocv.NewScalar(130, 120, 100, 0)
		mask := gocv.NewMat()
		gocv.InRangeWithScalar(hsv, lower, upper, &mask)

		count := gocv.CountNonZero(mask)
		total := sub.Rows() * sub.Cols()
		ratio := float64(count) / float64(total)

		hsv.Close()
		mask.Close()
		sub.Close()

		isEmpty := ratio > 0.7
		if !isEmpty {
			fmt.Printf("  ★ Swept Active Slot found at X=%d, Y=%d (Empty ratio: %.2f) -> WILL DEPLOY!\n", x, slotY, ratio)
			// Draw yellow circle on swept slot
			gocv.Circle(&debugImg, image.Pt(x, slotY), 18, color.RGBA{0, 255, 255, 255}, 2)
			gocv.PutText(&debugImg, "SWEPT", image.Pt(x-15, slotY-25), gocv.FontHersheySimplex, 0.4, color.RGBA{0, 255, 255, 255}, 1)
		} else {
			// Draw light blue small cross for empty slots for debugging
			gocv.Line(&debugImg, image.Pt(x-5, slotY), image.Pt(x+5, slotY), color.RGBA{255, 255, 0, 255}, 1)
			gocv.Line(&debugImg, image.Pt(x, slotY-5), image.Pt(x, slotY+5), color.RGBA{255, 255, 0, 255}, 1)
		}
	}

	outPath := "interactive_debug_result.png"
	gocv.IMWrite(outPath, debugImg)
	fmt.Printf("\nAnalysis complete! Highlighted image saved to %s\n", outPath)
	fmt.Println("Open it to verify green (strategy) and yellow (sweep) detections.")
}

func executeLiveDeployment(client *adb.Client) {
	fmt.Println("Starting live deployment execution...")
	
	// 1. Capture screen
	screen, err := client.CaptureToMat()
	if err != nil {
		fmt.Printf("Error: Capture failed: %v\n", err)
		return
	}
	defer screen.Close()

	w, h := screen.Cols(), screen.Rows()

	// 2. Load Config & Strategy
	botCfg := config.DefaultConfig()
	executor := attack.NewExecutor(client, &game.Calibration{
		PhysicalW: w,
		PhysicalH: h,
		ScaleX:    float64(w) / 860.0,
		ScaleY:    float64(h) / 732.0,
	}, &botCfg.Attack, zerolog.New(os.Stdout).With().Timestamp().Logger())

	stratPath := "assets/strategies/auto_edrag_rush.yaml"
	s, err := strategy.ParseYAML(stratPath)
	if err != nil {
		fmt.Printf("Error loading strategy: %v\n", err)
		return
	}

	fmt.Println("Executing DeployDynamic live on device...")
	remaining, err := executor.DeployDynamic(s, screen)
	if err != nil {
		fmt.Printf("Error during live deployment: %v\n", err)
	} else {
		fmt.Printf("Live deployment finished successfully! Remaining undeployed slots: %d\n", remaining)
	}
}
