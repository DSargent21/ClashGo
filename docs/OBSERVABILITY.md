# Observability: the bot's "eye view"

ClashGO drives an Android emulator (BlueStacks) over ADB. When the bot
misbehaves, the first question is always *"what does the emulator screen
actually show right now?"* — and answering it from a terminal (or a
text-only AI session) is exactly the problem this tooling solves.

Everything below renders what the game renders as **text**: classifier
state + per-rule pixel evidence, a color-mapped ASCII layout, and the
actual on-screen words via Apple Vision OCR.

## Quick start

```bash
# One-shot: capture + classify + color map + OCR, artifacts in obs/<ts>/
./tools/observe.sh

# Live loop, refresh every 3s
./tools/observe.sh --watch

# Just the classifier + layout, no OCR (faster)
./tools/observe.sh --no-ocr
```

Raw tools underneath:

```bash
go run ./cmd/screendump                      # live capture + classify + ascii
go run ./cmd/screendump -img /tmp/x.png      # analyze a saved frame
go run ./cmd/screendump -ocr                 # + Apple Vision OCR
go run ./cmd/screendump -watch -ocr          # live loop every 3s
```

`screendump` prints, in one pass:

```
=== SCREEN 14:22:01 (860x732) ===
CLASSIFIER: state=MainVillage score=...
--- state rules (pixel passes) ---
  * MainVillage      pass=4/4 minpass=2
    SearchMap        pass=1/4 minpass=1
    ...
--- color map (G=green O=orange R=red B=blue P=purple Y=yellow ...) ---
...ASCII layout of the frame...
--- key button pixels (ref coords -> live RGB) ---
  attack(64,666)     ( 64,666) RGB(...) dim
--- OCR (Apple Vision) ---
  365,520,150,30 | Continue
```

The rule list is the **same classifier the bot runs**, so a `pass=N/M`
that crosses `minpass` shows you *exactly why* the bot thinks it's on a
given screen — no guessing.

## Tool inventory

| Tool | Purpose |
|------|---------|
| `cmd/screendump` | Capture / classify / color-map / probe / OCR. The core. |
| `tools/observe.sh` | One-shot bundle of the above + artifacts under `obs/`. |
| `tools/ocr.swift` | Apple Vision OCR helper (compiled on demand, no deps). |
| `cmd/classify_probe` | Print scores for every state rule + key pixel reads. |
| `cmd/result_probe` | Trace a battle-result OCR misparse to the failing ROI. |
| `cmd/swipe_probe` | Verify human-gesture swipes move the camera live. |

## The boot-splash chain (findings)

The most valuable find so far: **what the game actually shows after every
relaunch**, and how the bot must handle each screen:

```
"ТАР!" (tap-to-continue / collect splash)
    │  tap the beige prompt text (ref 450,195)
    ▼
CoC castle logo / connecting splash   (static 1-3 min, NO progress bar)
    │  just wait — no tap needed
    ▼
News / announcement splash (e.g. "Meteor Golem" troop intro)
    │  tap the light-green Continue button (ref 403,535)
    ▼
Main Village → normal attack loop
```

These states are `StateTapToContinue`, `StateLogo`, and `StateNewsSplash`
in `internal/game/types.go`, with rules in `internal/game/classifier.go`
and dismissal taps in `internal/bot/bot.go::processFrame` (plus mirrored
cases in `internal/bot/wall_upgrade.go`).

### The bug this uncovered

The bot was trapped in an **endless force-stop/relaunch loop**: the
"ТАР!" collect splash's orange artwork scored 1/9 pixels on the
`StateBattle` rule, so the bot thought it was "in battle"; the
stuck-watchdog force-stopped the game; every relaunch re-showed the
splash → repeat, ~15 restarts in 12 minutes, zero attacks.

The fix had three parts (see `CHANGELOG.md` [Unreleased]):

1. **Correct classification** — dedicated splash rules, verified at
   **0 distance** on live captures and **0 false positives** on the
   village. The strongest discriminator is the beige "ТАР!" text
   (never present on the village); the green Continue button and the
   dark near-black corners round it out.
2. **Auto-dismissal** — `processFrame` fires one guarded goroutine per
   splash that taps the prompt (450,195) or Continue (403,535). It
   deliberately does **not** `recordActivity`, so a genuinely-stuck
   splash variant still trips the stuck-watchdog instead of being masked.
3. **Boot-grace** — `checkStuck` gives splash states a **5-minute**
   window (the castle logo has no progress indicator at all) instead of
   the generic 35s timeout that caused mid-boot restarts.

Live verification after the fix: 1 restart (the normal startup one),
splash auto-dismissals logged as `boot splash detected` /
`boot splash dismissed`, and complete attacks with correct result
parsing.

## Debugging workflow

1. **Look**: `./tools/observe.sh` — what screen is it on?
2. **Why**: read the `state rules` block. If a rule is 1 pixel short of
   `minpass`, the rule is too weak for that screen (tighten it). If a
   rule fires that shouldn't, it's a false positive (check the village
   with `go run ./cmd/screendump`).
3. **What does it say**: the OCR block reads buttons/announcements
   (e.g. "ТАР!", "Continue", "Attack!") with `x,y` coordinates so you
   can map text → tap target.
4. **Verify a fix**: capture a frame, confirm the classifier agrees,
   re-run the bot, and watch `boot splash detected` / `dismissed` in the
   log.

## Reproducing screenshots

`observe.sh` saves every run under `obs/<timestamp>/` with the raw PNG,
the annotated PNG, and the full text report. `obs/latest` symlinks the
most recent run so scripts can always find the current frame at a stable
path. These are scratch artifacts and are **git-ignored**.

## Requirements

- `adb` on `PATH`
- `go` (for `screendump` / the probe tools)
- `swiftc` (macOS, for OCR — the Vision framework)
- `gocv` OpenCV build (the repo's existing vision dependency)
