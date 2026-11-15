package game

import (
	"math"
	"os"
	"testing"

	"gocv.io/x/gocv"
)

// The storage halt turns on a bar reading 100%, so the fill measurement has to be
// accurate at every fill level, not just full and empty. This sweeps 0..100% in
// 1% steps over the HUD chrome the game actually draws — dark empty-bar wash,
// light rounded border, white amount text over the fill — and requires the
// reading to land within one pixel of the painted fill (the bar is ~204 px long,
// so 1 px is 0.5%).
func TestStorageBarAccuracy_FillSweep(t *testing.T) {
	const w, h = 1024, 768
	k := 1.1334
	// Same rounding as production, or the test paints a 1 px shorter bar.
	barRight := w - int(math.Round(storageBarRightRef*k))
	barLeft := barRight - int(math.Round(storageBarLenRef*k))
	barLen := float64(barRight - barLeft)
	if barLen < 100 {
		t.Fatalf("bar is %.0f px long; the sweep needs a real bar", barLen)
	}
	img := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	defer img.Close()

	set := func(x, y int, b, g, r uint8) {
		img.SetUCharAt(y, x*3, b)
		img.SetUCharAt(y, x*3+1, g)
		img.SetUCharAt(y, x*3+2, r)
	}

	worst, worstAt := 0.0, 0
	for pct := 0; pct <= 100; pct++ {
		yc := int(float64(storageBarCentersRef[0]) * k)
		half := int(storageBarHalfRef * k)
		fillCols := int(math.Round(barLen * float64(pct) / 100))
		// Paint from the bar's own left end, so 100% covers the whole track
		// instead of starting exactly at barRight (which paints nothing).
		start := barLeft + (barRight - barLeft - fillCols)

		for y := yc - half; y <= yc+half; y++ {
			for x := barLeft; x < barRight; x++ {
				switch {
				case x >= start && pct > 0:
					// Gold fill, with the vertical shading the HUD paints on it.
					shade := uint8(0)
					if y < yc {
						shade = 30
					}
					set(x, y, 32, 197, 255-shade)
				case x == barLeft || x == barRight-1 || y == yc-half || y == yc+half:
					// Light rounded border: bright but desaturated, never fill.
					set(x, y, 150, 150, 150)
				default:
					// Empty-bar wash: dark, desaturated.
					set(x, y, 70, 70, 70)
				}
			}
		}
		// Amount text: white glyphs sitting over the fill at the right end.
		for y := yc - half/2; y <= yc+half/2; y++ {
			for x := barRight - 60; x < barRight-8; x++ {
				if (x+y)%3 != 0 {
					set(x, y, 245, 250, 255)
				}
			}
		}

		got := ReadVillageStorageFillMat(img, k)
		want := 100 * float64(fillCols) / barLen
		err := math.Abs(got.GoldFillPercent - want)
		if err > worst {
			worst, worstAt = err, pct
		}
		if err > 1.0 {
			t.Fatalf("painted %d%% (%.1f%% exact) read back as %.1f%%: off by %.1f%%", pct, want, got.GoldFillPercent, err)
		}
		if pct == 100 && got.GoldFillPercent < 99.5 {
			t.Fatalf("a fully painted bar read %.1f%%, so a full storage could never halt", got.GoldFillPercent)
		}
		if pct == 0 && got.GoldFillPercent != 0 {
			t.Fatalf("an unpainted bar read %.1f%%, want 0", got.GoldFillPercent)
		}
	}
	t.Logf("worst error %.2f%% (at %d%% fill) over 101 fill levels", worst, worstAt)
}

// Each bar must read only its own row: the gold, elixir and DE bars are 45-52 ref
// px apart with a 24 ref px tall probe, one bad centre made gold read 0% while
// elixir read DE. Fill one bar and require the other two stay at zero.
func TestStorageBarAccuracy_RowsAreIndependent(t *testing.T) {
	const w, h = 1024, 768
	k := 1.1334
	// Same rounding as production, or the test paints a 1 px shorter bar.
	barRight := w - int(math.Round(storageBarRightRef*k))
	barLeft := barRight - int(math.Round(storageBarLenRef*k))

	colors := [3][3]uint8{{32, 197, 255}, {196, 43, 196}, {192, 39, 192}}
	for row := 0; row < 3; row++ {
		img := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
		yc := int(float64(storageBarCentersRef[row]) * k)
		half := int(storageBarHalfRef * k)
		start := barLeft + (barRight-barLeft)/4
		for y := yc - half; y <= yc+half; y++ {
			for x := start; x < barRight; x++ {
				img.SetUCharAt(y, x*3, colors[row][0])
				img.SetUCharAt(y, x*3+1, colors[row][1])
				img.SetUCharAt(y, x*3+2, colors[row][2])
			}
		}
		got := ReadVillageStorageFillMat(img, k)
		img.Close()
		vals := [3]float64{got.GoldFillPercent, got.ElixirFillPercent, got.DEFillPercent}
		for i, v := range vals {
			if i == row {
				if v < 70 || v > 80 {
					t.Fatalf("row %d filled from 75%% read %.1f%%", row, v)
				}
				continue
			}
			if v != 0 {
				t.Fatalf("row %d filled, but row %d read %.1f%%: probes overlap", row, i, v)
			}
		}
	}
}

// The strongest check available: read the bars and compare them with the game's
// OWN numbers, not with an earlier reading of the same bars.
//
// Both captures are live 1024x768 village frames whose HUD amounts are legible in
// the image (gold / elixir, in the comments below), and 31,000,000 is the
// storage cap the game itself printed on an Elixir Storage info panel ("Storage
// Capacity: 31 000 000" after an attack pinned elixir there). So the expected
// fill is amount/31,000,000 and the reader is graded against it.
//
// 1 px of the 219 px bar is 0.46%, so the tolerance is 1 point.
func TestStorageBarAccuracy_MatchesGameNumbers(t *testing.T) {
	cases := []struct {
		path         string
		gold, elixir float64 // amounts shown in the HUD of that capture
		elixirFull   bool
	}{
		// HUD: gold 15 019 210, elixir 31 000 000 (after one attack).
		{"testdata/live/village_hud_after_attack_1024x768.png", 15019210, 31000000, true},
		// HUD: gold 13 502 326, elixir 30 756 996.
		{"testdata/live/village_hud_full_elixir_1024x768.png", 13502326, 30756996, false},
	}
	const cap = 31000000
	checked := 0
	for _, c := range cases {
		// Live captures are gitignored, same as the other testdata/live frames;
		// skip rather than fail on a clone that does not have them.
		if _, err := os.Stat(c.path); err != nil {
			t.Logf("%s is not present (live captures are gitignored)", c.path)
			continue
		}
		img := gocv.IMRead(c.path, gocv.IMReadColor)
		if img.Empty() {
			t.Fatalf("%s: unreadable", c.path)
		}
		checked++
		got := ReadVillageStorageFillMat(img, 1.1334)
		img.Close()
		for _, v := range []struct {
			name   string
			got    float64
			amount float64
		}{
			{"gold", got.GoldFillPercent, c.gold},
			{"elixir", got.ElixirFillPercent, c.elixir},
		} {
			want := 100 * v.amount / cap
			if want > 100 {
				want = 100
			}
			if diff := math.Abs(v.got - want); diff > 1.0 {
				t.Fatalf("%s: %s read %.1f%%, but the game says %.0f/%.0f = %.1f%%", c.path, v.name, v.got, v.amount, float64(cap), want)
			}
			t.Logf("%s: %s read %.1f%%, game says %.1f%% (off %.2f)", c.path, v.name, v.got, want, math.Abs(v.got-want))
		}
		if c.elixirFull && !got.IsFull(false, true, false, "ANY", 95) {
			t.Fatalf("%s: a bar the game shows at 100%% did not trip the 95%% halt gate", c.path)
		}
	}
	if checked == 0 {
		t.Skip("no live HUD captures present (gitignored)")
	}
}
