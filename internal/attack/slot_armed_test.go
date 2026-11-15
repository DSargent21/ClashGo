package attack

import (
	"testing"

	"gocv.io/x/gocv"
)

// An armed card paints its background cyan (measured live: 1429 such pixels on an
// armed siege card, 0-4 on its unarmed neighbours). This is the only evidence
// that survives a refused placement on a card the template set cannot name.
func TestSlotArmedStatic_DetectsTheArmedHighlight(t *testing.T) {
	const w, h = 1024, 768
	const k = 1.1334
	img := gocv.NewMatWithSize(h, w, gocv.MatTypeCV8UC3)
	defer img.Close()

	if SlotArmedStatic(img, 239, 705, k) {
		t.Fatal("an empty (dark) card read as armed")
	}

	// Paint the armed highlight the way the game does: RGB (40,190,210).
	for y := 690; y < 730; y++ {
		for x := 225; x < 255; x++ {
			img.SetUCharAt(y, x*3, 210)   // B
			img.SetUCharAt(y, x*3+1, 190) // G
			img.SetUCharAt(y, x*3+2, 40)  // R
		}
	}
	if !SlotArmedStatic(img, 239, 705, k) {
		t.Fatal("the armed highlight was not detected")
	}
	if SlotArmedStatic(img, 325, 705, k) {
		t.Fatal("a neighbour card inherited the armed highlight")
	}
}

// The threshold has to scale with the card, or a smaller emulator window reads as
// permanently unarmed and the refusal is missed again.
func TestSlotArmedStatic_ScalesWithTheCard(t *testing.T) {
	img := gocv.NewMatWithSize(768, 1024, gocv.MatTypeCV8UC3)
	defer img.Close()
	for y := 690; y < 700; y++ {
		for x := 230; x < 240; x++ {
			img.SetUCharAt(y, x*3, 210)
			img.SetUCharAt(y, x*3+1, 190)
			img.SetUCharAt(y, x*3+2, 40)
		}
	}
	// 100 highlight px clears the bar at the reference scale, not at 1.13x.
	if SlotArmedStatic(img, 239, 705, 1.0) != (100 >= minArmedCyan) {
		t.Fatal("threshold does not follow the reference scale")
	}
}
