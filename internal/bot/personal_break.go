// Package bot — personal_break.go
//
// Supercell puts a player on a "Personal Break" after a long uninterrupted
// session. The dialog blocks the whole village, the classifier has no rule for
// it, and before this file the flow was: Unknown -> self-heal taps -> 35 s
// watchdog -> game restart -> the dialog is still there -> repeat. That burned
// the account's session, looked nothing like a player, and broke farming until
// a human closed the game.
//
// The dialog is text, so it is read as text: one OCR pass on an unclassified
// frame (never on a classified one) at most every personalBreakOCRInterval,
// matching phrases that only that dialog carries. Detection triggers the
// scheduler's break, which already knows how to pause input, stop the game, and
// resume afterwards.
package bot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Ducky705/ClashGO/internal/vision"
	"gocv.io/x/gocv"
)

// personalBreakOCRInterval bounds the OCR cost. An unclassified screen persists
// for seconds at a time and OCR is ~100 ms, so a 12 s floor keeps the capture
// loop clean while still noticing the dialog within one watchdog window.
const personalBreakOCRInterval = 12 * time.Second

// personalBreakPhrases are the strings the dialog uses. They are matched on the
// OCR'd, lower-cased, space-collapsed text of the whole frame, so word wrapping
// cannot split a phrase the way a per-region match would.
var personalBreakPhrases = []string{
	"personal break",
	"taking a break",
	"take a break",
	"session is ending",
}

// personalBreakActions are what the game offers on that dialog. Seeing one is
// corroboration: the words above appear in clan chat and in news copy, the
// action buttons only on the dialog.
var personalBreakActions = []string{"okay", "end session", "continue"}

// personalBreakDetected OCRs an unclassified frame and starts a scheduler break
// when the dialog is on it. Callers treat a true return as "do not tap".
func (b *Bot) personalBreakDetected(screen gocv.Mat) bool {
	if b.scheduler == nil || screen.Empty() {
		return false
	}
	if time.Since(b.personalBreakLast) < personalBreakOCRInterval {
		return false
	}
	b.personalBreakLast = time.Now()

	path := filepath.Join(os.TempDir(), fmt.Sprintf("clashgo_break_%d.png", time.Now().UnixNano()))
	if !gocv.IMWrite(path, screen) {
		return false
	}
	defer func() { _ = os.Remove(path) }()

	regions, err := vision.OCRTextRegions(path)
	if err != nil || len(regions) == 0 {
		return false
	}
	var sb strings.Builder
	for _, r := range regions {
		sb.WriteString(strings.ToLower(r.Text))
		sb.WriteByte(' ')
	}
	text := strings.Join(strings.Fields(sb.String()), " ")

	phrase := ""
	for _, needle := range personalBreakPhrases {
		if strings.Contains(text, needle) {
			phrase = needle
			break
		}
	}
	if phrase == "" {
		return false
	}
	corroborated := false
	for _, action := range personalBreakActions {
		if strings.Contains(text, action) {
			corroborated = true
			break
		}
	}
	if !corroborated {
		b.logger.Debug().Str("phrase", phrase).Msg("personal-break phrase seen without a dialog action button; ignoring (chat/news copy)")
		return false
	}

	// Only warn once the dialog is actually identified. The old check sat
	// before the OCR, so every unclassified frame — a loading screen, a
	// transition, the BlueStacks launcher — logged "a Personal Break dialog
	// may be on screen" every 12 s for the whole stall (live 2026-11-16).
	if !b.cfg().Schedule.Enabled {
		// The break state lives in the scheduler; with pacing off there is
		// nothing to pause.
		b.logger.Warn().Str("phrase", phrase).Msg("Personal Break dialog detected, but schedule.enabled is false so the bot cannot pause for it — enable the pacing schedule or close the dialog by hand")
		return false
	}

	b.logger.Warn().Str("phrase", phrase).Msg("Personal Break dialog detected; pausing the account for its own break")
	b.scheduler.TriggerPersonalBreak(15*time.Minute, "in-game Personal Break dialog")
	b.zoomedOut.Store(false)
	return true
}
