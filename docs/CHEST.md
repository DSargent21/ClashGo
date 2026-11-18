# Chest Reward Recovery

Clash of Clans runs periodic events where, after a battle, you get a reward
chest: tap it (repeatedly) until it opens, then tap **Continue**. ClashGO
detects this overlay and dismisses it automatically.

## How it works

1. **Detection** — the classifier (`StateChestReward`,
   `internal/game/classifier.go`) fires on the vault-room signature: at least
   3 of 4 pixel probes (arched window beam, the two top dark-stone walls,
   warm wood floor). No template is involved. The rule used to carry the
   `hammer` ("TAP TO OPEN") banner as a score bonus, but the pixel bar runs
   first and the template cannot create a detection — it only cost a
   full-frame search (removed 2026-11-17, see `docs/PERFORMANCE.md` P2).
2. **Break the chest** — `DismissChestReward`
   (`internal/game/chestdismiss.go`) taps the chest box (configurable hammer
   count) until the classifier no longer sees `StateChestReward`.
3. **Continue** — once the chest is open, it taps the **Continue** button:
   - preferred: a template-matched `btn_continue` (`assets/templates/btn_continue.png`),
   - fallback: the rect in `assets/continue_button.json`.
   Then verifies the classifier is back at `StateMainVillage`.

The bot dispatches the dismiss the instant `StateChestReward` is detected
(`internal/bot/bot.go`), bounded by `ChestWallClockLimit` (25s) and a
circuit-breaker (`MaxChestDismissLoops` = 15). The runtime kill-switch
`config.DeviceConfig.disable_chest_dismissal` hands off to the
stuck-watchdog instead.

## Setup (one-time, optional but recommended)

The detection rule works out-of-the-box on the pixel invariants. The only
template the flow uses is the Continue button — capture it once:

```sh
# On a known Continue overlay:
go run cmd/capture_template -name=btn_continue -drag
#    drag a tight rect around the Continue button.
```

To enable the faster Skip → Confirm path (fewer taps), pick the Skip and
Confirm-Yes rects with the calibration picker (`tools/picker.py`). Each run
captures the live screen and merges its keys into the output JSON, so run
them as two separate sessions with the manual navigation in between:

```sh
# 1. On the chest screen (Skip is visible):
python3 tools/picker.py -o assets/chest_dismiss_roi.json --rect skip_button

# 2. Manually tap Skip so the Confirm dialog renders, then pick it:
python3 tools/picker.py -o assets/chest_dismiss_roi.json --rect confirm_yes_button
```

`assets/chest_dismiss_roi.json` already ships with `tap_roi`, `tap_roi_alt`,
and `hammer_taps` tuned for the default layout; bump `hammer_taps` if a
particular event chest needs more hits to break.

## Tuning

- Chest still not detected? Check the bot's logs: a captured chest frame
  that classifies as `Unknown` (instead of `StateChestReward`) means fewer
  than 3 of the 4 vault-room probes hit — event art changed the room, so
  re-place the probes against a fresh capture. If detection works but
  dismissal misbehaves, adjust the tap zone in
  `assets/chest_dismiss_roi.json`.
- Continue misses? Recapture `btn_continue` (event art changed) or set
  `continue_button.json` to the button's rect.
