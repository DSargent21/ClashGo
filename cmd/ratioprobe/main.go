package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Ducky705/ClashGO/internal/attack"
	"gocv.io/x/gocv"
)

// Usage: ratioprobe <dir> — prints GetSlotActivityRatioStatic for each hero
// slot over every frame in dir (hero confirm debug dumps).
func main() {
	dir := os.Args[1]
	// Hero card slots observed live (x from tap trace, y=682 band; GW sits higher).
	slots := []struct {
		name string
		x, y int
	}{
		{"MinionPrince", 550, 682},
		{"ArcherQueen", 450, 682},
		{"DragonDuke", 751, 682},
		{"GrandWarden", 643, 646},
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var names []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".png" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	fmt.Printf("%-26s", "frame")
	for _, s := range slots {
		fmt.Printf(" %-14s", s.name)
	}
	fmt.Println()
	for _, n := range names {
		img := gocv.IMRead(filepath.Join(dir, n), gocv.IMReadColor)
		if img.Empty() {
			continue
		}
		fmt.Printf("%-26s", n)
		for _, s := range slots {
			r := attack.GetSlotActivityRatioStatic(img, s.x, s.y, img.Cols())
			fmt.Printf(" %-14.4f", r)
		}
		fmt.Println()
		img.Close()
	}
}
