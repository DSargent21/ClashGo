# Chest Reward Recovery

Clash of Clans runs periodic events where, after a battle, you get a reward
chest: tap it (repeatedly) until it opens, then tap **Continue**. ClashGO
detects this overlay and dismisses it automatically.

## How it works

1. **Detection** — the classifier (`StateChestReward`,
   `internal/game/classifier.go`) watches for two structural invariants that
   stay constant across events:
   - a dark "TAP TO OPEN" shadow band along the bottom-center, and
   - a warm (yellow/orange) glow at screen center where the lit chest sits.
   It does **not** depend on the chest's exact paint job, so it keeps working
   when CoC reskins the event every couple of months.
    - Optional `assets/templates/hammer.png` (the tap-to-open hammer icon)
      adds confidence when present but can never trigger the state on its own.
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

To enable the faster Skip → Confirm path (fewer taps), capture the Skip and
Confirm-Yes rects:

```sh
go run cmd/pick_chest_roi -also-buttons          # skip_button
go run cmd/pick_chest_roi -confirm-only          # confirm_yes_button
```

`assets/chest_dismiss_roi.json` already ships with `tap_roi`, `tap_roi_alt`,
and `hammer_taps` tuned for the default layout; bump `hammer_taps` if a
particular event chest needs more hits to break.

## Tuning

- Chest still not detected? Run `go run cmd/debug_state` on a live chest
  screen and check the printed per-rule scores; the dark-band / glow pixels
  in `classifier.go` can be loosened (raise each `PixelCheck.Tolerance`).
- Continue misses? Recapture `btn_continue` (event art changed) or set
  `continue_button.json` to the button's rect.
