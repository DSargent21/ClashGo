# Chest Reward Recovery

Clash of Clans runs periodic events where, after a battle, you get a reward
chest: tap it (repeatedly) until it opens, then tap **Continue**. ClashGO
detects this overlay and dismisses it automatically.

## How it works

1. **Detection** — the classifier (`StateChestReward`,
   `internal/game/classifier.go`) fires **only** when the `hammer`
   ("TAP TO OPEN") template matches. It is template-only by design: the
   hammer icon + prompt vanish the moment the chest breaks and never appear
   on a normal village screen, so no pixel rule can false-trigger dismissal.
   The hammer icon is small, stable UI chrome, so the rule survives CoC
   event art swaps. Without `assets/templates/hammer.png` loaded,
   `StateChestReward` never fires.
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

The detection rule works out-of-the-box on the pixel invariants. For maximum
robustness against art swaps, capture two templates once:

```sh
# 1. Get to the chest screen, then in another terminal:
go run cmd/capture_template -name=hammer -drag
#    drag a TIGHT rect around the tap-to-open hammer icon (not the whole
#    chest box) — it's small UI chrome, stable across event art. conf ~0.9.

# 2. After the chest opens (or on a known Continue overlay), capture it:
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
  that classifies as `Unknown` (instead of `StateChestReward`) means the
  `hammer` template isn't matching — recapture it (event art changed). If
  the template matches but dismissal misbehaves, adjust the tap zone in
  `assets/chest_dismiss_roi.json`.
- Continue misses? Recapture `btn_continue` (event art changed) or set
  `continue_button.json` to the button's rect.
