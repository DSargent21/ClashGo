package bot

import (
	"errors"
	"fmt"

	"github.com/rs/zerolog"
)

// Battle zoom is a precondition, not a nicety.
//
// Every coordinate this bot deploys on was authored or detected against a fully
// zoomed-out battlefield: the deploy band, the red-zone perimeter, the pinned
// lines in precision_config.json, the hero ground ladder. At a closer zoom the
// same pixel lands on a different world position - typically inside the base -
// and the game answers the tap with "You cannot deploy troops on the Red area!"
// while the deploy phase burns.
//
// The village path already treats this as mandatory (bot.processFrame releases
// its zoomedOut flag and retries on the next frame). The battle path used to log
// "battle zoom-out encountered an error; proceeding with deployment" and deploy
// blind, which is the one remaining way for a run to tap on geometry nobody
// verified. This guard closes it: bounded retries, then a hard error.
const battleZoomAttempts = 2

// zoomMaxer is the slice of the navigator this guard needs. Narrow on purpose:
// the policy below is testable without a device, and a fake can prove the retry
// and the give-up path.
type zoomMaxer interface {
	ZoomOutMax() error
}

var errBattleZoomUnconfirmed = errors.New("battle camera is not at max zoom-out")

// ensureBattleZoom drives the closed-loop zoom-out until it confirms, and reports
// an error when it cannot. The returned error wraps the last failure so the log
// says why the camera would not settle (inert injector, capture failure,
// cancellation).
func ensureBattleZoom(z zoomMaxer, attempts int, logger zerolog.Logger) error {
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for i := 1; i <= attempts; i++ {
		if err := z.ZoomOutMax(); err != nil {
			last = err
			logger.Warn().Err(err).Int("attempt", i).Int("attempts", attempts).
				Msg("battle zoom-out did not confirm")
			continue
		}
		logger.Info().Int("attempts_used", i).Msg("battle camera confirmed at max zoom-out")
		return nil
	}
	if last == nil {
		last = errBattleZoomUnconfirmed
	}
	return fmt.Errorf("%w: %w", errBattleZoomUnconfirmed, last)
}
