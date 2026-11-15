package game

import (
	"image"

	"gocv.io/x/gocv"
)

// battleHUDVetoScore is the score reported when the battle HUD decides the state.
// It is only ever used for logging: the verdict comes from the HUD, not from a
// probe tally, so the number just needs to be recognisable in a log line.
const battleHUDVetoScore = 500

// liveBattleHUD reports whether the frame shows the in-battle HUD, recognised by
// the red SURRENDER pill in the bottom-left corner.
//
// Why this exists. On a real battle frame the probe rules picked the wrong state:
// StateChestReward won at priority 98 with 3 of its 4 "vault room" probes passing
// on scenery (the top-left and top-right dark probes landed on tree canopy, the
// bottom-right warm probe on dirt), the StateBattle rule only reached 3/12 at that
// geometry, and StateMainVillage matched 2/12 off the grass. The bot then ran the
// chest dismisser against a live battle, the attack sequence never saw StateBattle,
// and the raid sat at 0% damage until the timer expired with a full army. Probe
// lists cannot separate "dark stone wall" from "dark tree canopy"; the pill can.
//
// Measured on the live 1024x768 frame (2026-11-13 17:02, kept as
// testdata/corpus/battle_live_1024x768.png): the pill occupies (17-145, 579-610),
// its face is (253,93,100) with (248,13,22) borders and a white SURRENDER label,
// and 44.8% of the sampled box is red-dominant. A village frame has none: the
// corner button there is gold, whose R-G is ~58, well under the bar.
//
// The marker scales with the viewport (relative box), and a modal dims the frame,
// dropping the pill under the bar — the safe direction, because the modal gets
// dismissed by its own rule and the next clean frame reports Battle again.
func liveBattleHUD(screen gocv.Mat) bool {
	if screen.Empty() {
		return false
	}
	w, h := screen.Cols(), screen.Rows()
	if w < 600 || h < 500 {
		return false
	}

	// The box covers the pill with margin: measured 1.7-14.2% of the width and
	// 75.4-79.4% of the height, sampled 2-12% / 75.5-79.5%.
	x0, x1 := int(0.02*float64(w)), int(0.12*float64(w))
	y0, y1 := int(0.755*float64(h)), int(0.795*float64(h))
	if x1 <= x0 || y1 <= y0 || x1 > w || y1 > h {
		return false
	}

	// Clone before ToBytes: a Mat.Region view keeps the parent's stride, so
	// ToBytes on the view walks out of the submatrix and reads the rest of the
	// row. That silently produced "0 red pixels" on a frame where 45% of the box
	// is the pill, and it is why the first version of this veto never fired.
	view := screen.Region(image.Rect(x0, y0, x1, y1))
	defer view.Close()
	roi := view.Clone()
	defer roi.Close()
	buf := roi.ToBytes() // BGR, three bytes per pixel
	if len(buf) < 3 {
		return false
	}

	// 25% of the box: half the measured 44.8%, which a red UI badge in a village
	// corner cannot reach, and enough headroom for the white label and the
	// pill's rounded edges.
	red, total := 0, len(buf)/3
	for i := 0; i+2 < len(buf); i += 3 {
		if battleHUDRed(uint8(buf[i+2]), uint8(buf[i+1]), uint8(buf[i])) {
			red++
		}
	}
	return red*4 >= total
}

// battleHUDRed reports whether a pixel is the SURRENDER pill's red: bright and
// strongly red-dominant. The face (253,93,100) and the border (248,13,22) both
// qualify; gold buttons (R-G ~58), grass, dirt and brick roofs do not.
func battleHUDRed(r, g, b uint8) bool {
	return int(r) >= 200 && int(r)-int(g) >= 90 && int(r)-int(b) >= 90
}

// stateIsObstruction reports whether a state is a modal/overlay that has to be
// dismissed rather than something the battle HUD should override. The veto must
// never hide a "Connection lost", "Update available" or offer dialog: those are
// the states that unblock the bot.
func stateIsObstruction(s GameState) bool {
	switch s {
	case StateLoading, StateUpdatePrompt, StateConfirmExit, StateConnectionLost, StateInactivity,
		StateWelcomeBack, StateGemDialog, StateObstacleDialog, StateOfferPopup,
		StateNewsSplash, StateLogo, StateChatOpen, StateSettings, StateShieldInfo:
		return true
	}
	return false
}
