# Decisions

Architectural and process decisions future sessions must not silently reverse.
To change one: add a new entry that supersedes it and update the code in the
same task. Never delete history; mark it `Superseded by D-x`.

## D-1 Persistent ADB socket transport
Status: accepted

Capture and input go through one persistent TCP connection to the local ADB
server (`internal/adb`), not `adb shell` per action. Reason: 30–50 ms capture
instead of process spawn per frame. Consequence: reconnect/recovery logic must
stay inside the transport; callers never dial ADB themselves.

## D-2 Vision stays pure and offline-testable
Status: accepted

Classification/OCR/loot reading take a frame and return a verdict; no taps, no
sleeps, no device access inside `internal/game` and `internal/vision`.
Consequence: every new detector ships with a recorded-frame fixture test, and
unreadable frames return `Unknown` instead of a guess.

## D-3 One verified increment per session
Status: accepted

A session picks one task from `BUILD_PLAN.md`, implements it, adds an offline
test, runs `./tools/verify.sh`, updates `PROGRESS.md`/`BUILD_PLAN.md`, commits,
then stops or takes the next unblocked task. Reason: long autonomous runs fail
by over-scoping and by declaring victory early. Consequence: no multi-feature
commits, no task marked done from code inspection alone.

## D-4 State lives in the repository, not the conversation
Status: accepted

`PROGRESS.md` (where we are), `BUILD_PLAN.md` (what is next and how it is
accepted), `DECISIONS.md` (what must not be reversed), `docs/DESIGN.md`
(architecture). Consequence: every session must be startable from a fresh
context window; if the files disagree with the code, the code wins and the
files get fixed in the same task.

## D-5 Verification gate is the only proof of done
Status: accepted

`tools/verify.sh` runs `go vet`, `go test`, `go test -race`, `go build`. Tests
never need an emulator, account or network. Real-device checks stay behind the
`livesmoke` tag / `make smoke-live*` and are never run by an agent unless the
user explicitly asks (they spend a real army).

## D-6 No external agent orchestrator
Status: accepted

The Freebuff CLI has no headless/non-interactive run mode, so the loop is driven
by Freebuff (Mission or a normal session) following `AGENTS.md`, with the
repository as durable memory. Consequence: do not add a scheduler/cron that
spawns `freebuff`; revisit only if a supported headless mode appears.

## D-7 Stdlib and existing dependencies first
Status: accepted

No new Go module for something the standard library or an installed package
already does. Consequence: agent proposals that add a dependency need a stated
reason and a decision entry here.

## D-9 One control, one home
Status: accepted

Every setting is rendered in exactly one place. Per-plan choices (active
strategy, recipe slot, auto-end) live on Army; engine-wide thresholds,
builders/lab/pets, schedule and storage live on Config; walls live with
Builders next to the reserved-builder count. Reason: duplicated controls drift
out of sync (Auto end read 50% on Config while the plan carried another
percent) and make a setting look optional twice. Consequence: a new control
joins an existing owner instead of being copied onto a second page; cross-links
may summarise, but not edit. See `docs/UI.md` → Control ownership.

## D-8 Deliberate pauses are not hangs
Status: accepted

Scheduler breaks, full storages and empty army publish a single halt reason
(`BotStats.halted_reason`); watchdogs must not restart a halted bot, and pacing
counters in `session_state.json` survive restarts. Consequence: any loop change
must keep halt state and persistence intact, with a fake-clock test.
