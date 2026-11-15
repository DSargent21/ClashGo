package game

import (
	"math"
	"testing"

	"gocv.io/x/gocv"
)

// The halt gate reads a full bar as a full storage: a bar painted entirely in
// its resource colour must read 100%, and one with nothing painted must read 0%.
func TestReadVillageStorageFill_FullBarReadsFull(t *testing.T) {
	const w, h = 1024, 768
	k := 1.1334
	// Same rounding as production, or the test paints a 1 px shorter bar.
	barRight := w - int(math.Round(storageBarRightRef*k))
	barLeft := barRight - int(math.Round(storageBarLenRef*k))

	img := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	defer img.Close()

	paint := func(centerRef int, b, g, r uint8) {
		yc := int(float64(centerRef) * k)
		half := int(storageBarHalfRef * k)
		for y := yc - half; y <= yc+half; y++ {
			for x := barLeft; x < barRight; x++ {
				img.SetUCharAt(y, x*3, b)
				img.SetUCharAt(y, x*3+1, g)
				img.SetUCharAt(y, x*3+2, r)
			}
		}
	}
	paint(storageBarCentersRef[0], 32, 197, 255) // gold
	paint(storageBarCentersRef[1], 196, 43, 196) // elixir
	paint(storageBarCentersRef[2], 192, 39, 192) // dark elixir

	got := ReadVillageStorageFillMat(img, k)
	if got.GoldFillPercent < 99 || got.ElixirFillPercent < 99 || got.DEFillPercent < 99 {
		t.Fatalf("full bars read as %+v, want ~100%% each", got)
	}
}

func TestReadVillageStorageFill_EmptyBarsReadZero(t *testing.T) {
	img := gocv.NewMatWithSize(768, 1024, gocv.MatTypeCV8UC3)
	defer img.Close()
	got := ReadVillageStorageFillMat(img, 1.1334)
	if got.GoldFillPercent != 0 || got.ElixirFillPercent != 0 || got.DEFillPercent != 0 {
		t.Fatalf("empty HUD read as %+v, want zeroes", got)
	}
}

// A bar filled to its right half reads ~50%, and the glyph-free measurement is
// the point: counting colours around the amount text capped a full bar at ~70%.
func TestReadVillageStorageFill_HalfBarReadsHalf(t *testing.T) {
	const w, h = 1024, 768
	k := 1.1334
	// Same rounding as production, or the test paints a 1 px shorter bar.
	barRight := w - int(math.Round(storageBarRightRef*k))
	barLeft := barRight - int(math.Round(storageBarLenRef*k))
	mid := barLeft + (barRight-barLeft)/2

	img := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	defer img.Close()
	yc := int(float64(storageBarCentersRef[0]) * k)
	half := int(storageBarHalfRef * k)
	for y := yc - half; y <= yc+half; y++ {
		for x := mid; x < barRight; x++ {
			img.SetUCharAt(y, x*3, 32)
			img.SetUCharAt(y, x*3+1, 197)
			img.SetUCharAt(y, x*3+2, 255)
		}
	}
	got := ReadVillageStorageFillMat(img, k)
	if got.GoldFillPercent < 45 || got.GoldFillPercent > 55 {
		t.Fatalf("half bar read %.1f%%, want ~50%%", got.GoldFillPercent)
	}
}
