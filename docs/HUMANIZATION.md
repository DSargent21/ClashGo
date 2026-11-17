# Humanization audit

What makes this bot look like a bot, what was changed, and what cannot be fixed
from inside the client.

Read the honest part first: **no client-side change prevents a ban.** Botting is
a Terms-of-Service violation, and Supercell's detections are mostly server-side
(account uptime, play-pattern aggregates, client integrity) or report-driven.
Everything below only removes *behavioural* signatures — the things that make a
human reviewer or a statistical play-pattern check notice the account. It is
not a guarantee, and it should not be treated as one.

## What was audited

| Surface | Where it lives | Signal |
| --- | --- | --- |
| Tap coordinates | `internal/adb/client.go` | Gaussian spread per tap |
| Tap kinematics | `internal/adb/client.go`, `bezier.go` | press duration, arc, velocity |
| UI decision taps | `internal/game/navigator.go`, `internal/bot/bot.go` | reaction delay |
| Troop deploy cadence | `internal/attack/{executor,sweep,attack}.go` | taps per second |
| Hero ability cadence | `internal/attack/attack.go` | taps per second |
| Inter-raid gap | `internal/bot/bot.go` | raids per hour, on a beat |
| Idle behaviour | `internal/bot/idle.go`, `internal/game/navigator.go` | camera movement regularity |
| Session / break length | `internal/bot/scheduler.go` | daily uptime shape |
| Active hours, rate caps | `internal/bot/scheduler.go`, config defaults | 24/7 farming |
| Recovery behaviour | `internal/bot/{recovery,personal_break}.go` | loops, re-taps, restarts |

Out of scope: anything the *server* sees that the client cannot influence
(account age, trophy trajectory, defence results, reports).

## Findings and changes

### 1. Deploy cadence was superhuman

`sleepBetweenBatches` used 60±20 ms between three-tap batches, and `TapTriple`
spaced its taps 55±12 ms. That is roughly **24 taps per second** from one finger
for the whole deploy window, on the same beat every raid. No human produces it.

Now: 85±25 ms inside a batch, 110±35 ms between batches, with a 12% longer
branch → a fast-human ~10 taps/s, irregular. Cost is under two seconds across a
full army, inside the 7 s `DeployGoal`. Hero abilities moved from 80 ms apart to
260±70 ms, and the normal sweep path from 50±15 ms to 95±30 ms.

### 2. Every raid started on the same beat

`min_seconds_between_attacks` was applied verbatim: Return Home, wait exactly
30 s, search. Fifty raids a day, all 30 s apart. The shipped default is now
300 s (a raid every ~5-6 min, plus the pauses below); an explicit
`min_seconds_between_attacks` in config.json still overrides it, including a
faster value the user chooses for themselves.

Now `humanizedAttackGap` applies ±22% and, about one raid in ten, a 30-150 s
extra pause (checking the result, answering a message). The configured value
remains the floor of the distribution.

### 3. Idle camera pan was a metronome

The bot panned the village every 18.0 s, forever, and returned the camera to
the same origin each time.

Now `idlePanDelay`: 22-57 s normally, and about one wait in eight is a 2-5
minute stare. Still no taps, still never overlaps an attack, still does not
reset the watchdog.

### 4. Session and break lengths were uniform

75 ± 20 minutes and 20 ± 7 minutes, uniformly drawn, every session, every day.
Real players have short check-ins and occasional absences.

`humanizedMinutes` makes about one session in six a short 55-75% check-in;
`humanizedBreakMinutes` makes about one break in ten 1-3 hours longer. With
jitter set to 0 both helpers return the configured value unchanged, so a
deterministic config (and the scheduler tests) stay deterministic.

### 5. UI taps were instantaneous at 2 px of spread

Graph navigation used `Client.Tap`: a 0 ms down/up at 2 px Gaussian spread.
Decision taps elsewhere already used `TapHuman` (180-550 ms reaction latency,
20% held 60-130 ms) — navigation was the gap.

`Navigator.executeStep` now uses `TapRandomized`, and screen-transition waits are
±20% (`humanPause`). The default tap jitter rose from 2 px to 5 px
(`DefaultMaxJitterPixels`), and `config.Load` raises older configs below 4 px to
4 px. A deliberate `jitter_taps: false` or `max_jitter_pixels: 0` is preserved.

### 6. Already sound (left alone)

- `TapHuman` latency and hold distribution; `SwipeBezier` arcs and eased velocity.
- Scheduler active hours (07:00-01:00), session/break jitter, hourly and daily
  attack caps — safe defaults, all editable.
- Personal Break detection pauses the account instead of fighting the dialog.
- Overlay dismissal is evidence-gated (no blind tapping), and the chest flow is
  cascade-capped instead of looping.
- Watchdogs distinguish deliberate halts from hangs, so a break is not restarted
  into.

## Live check (2026-11-16, BlueStacks, 1024x768)

One real raid was run through `make smoke-live` with `CLASHGO_TAP_TRACE`, after
`./tools/verify.sh` passed. The tap trace is the measurement, not an inference:

- intra-batch taps: 45-118 ms (was 43-67 ms)
- between batches: 91-149 ms (was 40-80 ms)
- the deploy window fired 11 Balloons and 11 Electro Dragons, then a 5-cast rage
  spread, 2 heroes and one ability before the battle was lost.

What the raid did **not** establish: the emulator dropped its network connection
at 64% destruction (`connection lost during battle`), the result panel was never
read, and the game then sat on an unrecognised loading/Tap! screen that the
classifier reports as `Unknown`; the bot restart-looped on it. That screen is a
boot-splash classifier gap, separate from this audit and not fixed here. The
account's army was spent by that raid, so no second live attempt was possible.

A second live defect was found and fixed: `personal_break.go` logged "a Personal
Break dialog may be on screen" every 12 s on ANY unclassified frame because the
`schedule.enabled` check ran before the OCR. It now warns only after the dialog's
phrase and an action button are both read from the frame.

## Remaining, by risk

1. **Uptime shape is still the strongest signal.** Even with breaks, the account
   plays every day inside one 18-hour window with 50 raids. A lower
   `max_attacks_per_day`, or simply not running every day, does more for
   plausibility than any timing change.
2. **Attack execution is still identical in shape.** Same strategy, same deploy
   line, same spell pattern every raid. Varying the strategy/edge per raid is a
   product change, not a timing tweak, and is not implemented here.
3. **Emulator fingerprint.** Nothing in this repo hides BlueStacks or the ADB
   input path; a client-integrity check would see them regardless of timing.
4. **These numbers are distributions, not proof.** They were chosen from human
   motor ranges, not fitted to a labelled dataset. No detection study was run.
