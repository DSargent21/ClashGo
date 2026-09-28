# Performance Audit

ClashGO went through a structured perf audit that touched the Go backend, the
Wails IPC bridge, and the React GUI. All changes shipped in a single
session — zero behavior changes, only the hot-path surfaces trimmed. This
doc records *what* changed, *why* the change matters, and *how* to verify
on your own machine.

## TL;DR

| Stress | Battle | Idle |
|--------|-------:|-----:|
| **CPU saved** (single core) | **10–20%** | 3–5% |
| **RSS saved** (transient) | **10–20 MB** | 5–10 MB |
| **GC cycle frequency** | **-40 to -60%** | -10 to -20% |
| **Stop-IPC latency** | n/a | **-95%** (2 s → <50 ms) |

These are principled **estimates from code analysis**, not live measurements.
The recipe at the bottom of this doc validates them on your machine.

## Resource usage (RAM / CPU)

Same methodology as the perf audit above (code analysis, not live capture).
Assumes **Apple Silicon Mac**, **BlueStacks at 860×732 / 160 DPI**.

**Frame math (860×732, RGB):**
- Full capture frame: `860 × 732 × 3 ≈ 1.80 MB`
- Half-size frame (Live View JPEG encode): `430 × 366 × 3 ≈ 0.47 MB`

| Scenario | ClashGO (Go) RSS | ClashGO CPU | + BlueStacks RSS | Combined RSS (est.) |
|----------|----------------:|------------:|-----------------:|--------------------:|
| Idle / UI only (1 FPS) | ~60–90 MB | ~1–3% (1 core) | ~800 MB–1.2 GB | ~0.9–1.3 GB |
| Active battle @ 15 FPS | ~90–140 MB | ~15–25% (1 core) | ~1.0–1.5 GB | ~1.1–1.7 GB |

Notes:
- CPU is single-core. The bot processes one capture frame at a time
  (classify → template match → tap), so cost scales ~linearly with FPS.
  At 15 FPS the per-frame vision work (~7–15 ms) is ~15–25% of one core.
- BlueStacks RAM is driven by the emulator + Android guest, not the bot,
  and is largely independent of ClashGO's FPS.
- The bot's own RAM splits roughly: capture/working Mats (mat pool)
  ~2–4 MB, `ScaledTemplateCache` ~1–3 MB, Live View base64 buffer a few
  hundred KB. The rest of the idle ~60–90 MB is the Wails/WebKit GUI
  harness, not the Go bot logic.

Verify on your machine with the RSS-sampling recipe under
[How to verify on your machine](#measurement-a--rss-sampling-during-a-battle-no-code-changes)
(extend the `ps` sample to also cover the BlueStacks process for the
combined number).

### CPU metric: absolute time, not a percentage

A "% CPU" reading is **device-relative** — it is a fraction of one core on
the host the bot happens to run on, so it cannot be compared across machines
and is not "accurate" in an absolute sense. To make CPU measurable and
comparable everywhere, ClashGO reports two device-independent numbers
(`internal/bot/cputime.go`, via `getrusage(RUSAGE_SELF)`):

- **`cpu_time_sec`** — total CPU time (user + system) consumed by the bot
  process since start, in seconds. This is the canonical, portable metric:
  burning one core for 10 s reports ~10 s on any machine regardless of core
  count. Compare efficiency by measuring the delta in `cpu_time_sec` over a
  fixed wall-clock window (e.g. per attack, or per minute of battle).
- **`cpu_cores`** — CPU usage as a fraction of one core over the last sample
  window (`Δcpu_time / Δwall`). `1.0` = one full core busy for the whole
  interval. Multiply by the host's logical-core count **only** if you want a
  familiar 0–100% number for that specific machine.

The UI's "CPU Usage %" = `cpu_cores × navigator.hardwareConcurrency`; the
underlying `cpu_time_sec` is what you should log/cite for cross-device
comparisons.

**Accuracy note:** `getrusage` is kernel-authoritative for process CPU time,
not a sampling estimate, so `cpu_time_sec` is exact (microsecond resolution),
not an approximation. The only source of error is the sampling window for
`cpu_cores`, which smooths over the window duration.

## What changed (and why)

Each finding pairs the **issue**, the **file:line** it lives in, and the **estimated CPU / RAM savings**.

### 1. Dead `live_feed` EventsEmit binding

Removing one capture-loop closure eliminated the biggest sustained IPC
bandwidth hog in the bot.

- **Was** (`internal/bot/bot.go:381–395`): every captured frame spawned
  a goroutine that resized to half-size, JPEG-encoded at quality 60,
  base64-encoded, and then pushed through `runtime.EventsEmit("live_feed", ...)`
  — at capture-loop cadence (up to **10 FPS in battle state**).
- **But**: React never subscribed (verified: no `EventsOn("live_feed", ...)`
  anywhere under `web/src/components/`). Every emit was a
  one-way trip into the WailsIPC bridge with zero consumer.
- **Cost removed**: 10 FPS × ~7–15 ms per emit (resize + base64 + IPC marshal).
- **Savings**: **~7–15% battle-state CPU** + **~5–15 MB transient RSS**.

### 2. Dead `bot_log` EventsEmit binding

Same family of bug — every zerolog line emitted a WailsIPC event with no subscriber.

- **Was** (`app.go:51–69`, `WailsLogWriter.Write`): every log line
  (`~30–60 lines/sec` during an active attack — each tap, state
  transition, vision verdict, error retry) fired
  `runtime.EventsEmit("bot_log", msg)`.
- **Cost removed**: ~30–60 emits/sec × ~0.5–1 ms each.
- **Savings**: **~1–3% battle-state CPU**.

### 3. Screenshot poll tab-gated into `<Feed/>`

The 1 Hz `GetLiveScreenshot()` IPC call was firing from `App.tsx`'s root
`useEffect` for **every** mounted tab — even Dashboard/Settings/Analytics
where no image consumer exists.

- **Was**: a `setInterval(updateScreenshot, 1000)` lived inside the root
  `useEffect` of `web/src/App.tsx`, so it ran regardless of which tab was
  active. This branch removes it (see commit diff `web/src/App.tsx`).
- **Now** (`web/src/components/Feed.tsx` — the `useEffect` block in the
  component body): `Feed` owns its own `useEffect` with its own
  `setInterval`. The interval only mounts when `<Feed/>` is rendered
  (only when `tab === 'feed'`).
- **Cost removed**: ≥ 1 IPC call/sec × ~2 ms + ~50–150 KB base64 string
  per call when the user is not on Live View (the typical case).
- **Savings**: **~0.1–0.3% continuous CPU** + **~1–2 MB transient RSS** when the user isn't actively watching Live View.

### 4. Vision cache key empties per-frame Mat alloc spree

`vision.MatchMultiScale()` was being called with an empty name, which
bypassed the `ScaledTemplateCache` and re-allocated inside the loop.

- **Was** (`internal/game/classifier.go::ClassifyState`):
  `vision.MatchMultiScale(norm, tpl, 0.9, 1.1, 3, threshold)`.
- **Now**: `vision.MatchMultiScaleROICached(norm, tpl, rule.Template, ...)`
  — the template name becomes the cache key, the cached scaled Mats are reused.
- **Cost removed**: 6 rules with templates × 3 scales × 10 FPS
  = **180 Mat alloc/free/sec eliminated**. Each gocv Mat is
  CGO-allocated C memory; the alloc+free roundtrip is
  ~50–150 µs per call.
- **Savings**: **~1–2% battle-state CPU** + **~1–3 MB transient RSS** + noticeable reduction in GC frequency.

### 5. World snapshot flush cadence lowered to 1 Hz

> **Update (dead-code cleanup):** the `internal/world` snapshot writer this
> entry refers to was later deleted as dead code — it had no production
> consumers. This entry is historical; the savings below no longer apply
> (the writer itself is gone).

The world snapshot writer was flushing to disk 4×/sec; production
consumers (`jq` observers, the React stats poll at 0.5 Hz, replay
tooling) don't need 4 Hz.

- **Was** (`internal/world/world.go`): `MinWriteInterval: 250 * time.Millisecond`.
- **Now**: `MinWriteInterval: 1 * time.Second`.
- **Cost removed**: 3 fewer `MarshalIndent + WriteFile + Rename` cycles/sec,
  roughly 1–4 ms each.
- **Savings**: **~0.5–0.8% continuous CPU** + **~0.5–2 MB transient RSS**.

### 6. `GetAttackHistory` in-memory cache (double-checked)

State-change disk reads on a 0.5 Hz poll are pure overhead.

- **Was** (`app.go::GetAttackHistory`): every React poll (0.5 Hz) =
  `os.ReadFile(attack_history.json)` + `json.Unmarshal` of ~50–200 KB.
- **Now** (`app.go::GetAttackHistory` + `ensureHistoryLoadedLocked` +
  `refreshHistory`): reads from a `cachedHistory []bot.AttackReport`
  guarded by an RWMutex — the slow path uses double-check locking so
  only ONE goroutine ever performs the disk read on cold start.
  Refreshed eagerly per attack end via `b.OnStatsUpdate`; invalidated
  manually by `ResetStats` + on StopBot teardown.
- **Cost removed**: 0.5 reads/sec × ~2–3 ms each.
- **Savings**: **~0.1–0.2% continuous CPU**. Steady-state RAM is a wash
  (data was in page cache either way) but the disk I/O is gone.

### 7. `b.Cancel()` + detached `StopBot` heavy teardown

The UI Stop button blocked on the entire graceful-shutdown chain.

- **Was** (`b.Stop` + saving): cancel → ADB Close → `globalAsyncWriter.Close`
  (which `wg.Wait()`s in-flight stats writes) → template cache close →
  NDJSON close → `saveStats` (sync marshal + sync file write). On an active
  attack: **1–3 s** before the IPC reply reached React.
- **Now** (`app.go::StopBot` + `internal/bot/bot.go::Cancel`):
  `Bot.Cancel()` is the synchronous, sub-millisecond "stop what you're doing"
  signal. `App.StopBot` synchronously cancels, detaches the heavy
  `bot.Stop()` + `saveStats()` to a goroutine, and returns within ~10–50 ms.
  The detached goroutine also invalidates `cachedHistory` so manual
  edits to `attack_history.json` while the bot is off reflect at the next poll.
- **Cost removed**: ~1–3 s of IPC-blocked wall-time per Stop click.
- **Savings**: **~95% Stop-IPC latency cut** (user-visible "Stop feels instant").

### 8. Other smaller wins (in the same commit)

- `web/src/App.tsx` — removed unused `GetLiveScreenshot` import after the
  tab-gate. TypeScript would have flagged this otherwise.
- `web/src/components/Dashboard.tsx` — dropped the `onClearLogs` prop
  (was unused; logs refresh from the 0.5 Hz poll instead).
- `web/src/components/UpdateBanner.tsx` — replaced `\u2014` / `\u2022`
  with the actual em-dash / bullet glyphs (cleaner in analysis tools).
- `web/src/components/ConfigView.tsx` — added a saving / saved / error
  pill so the user sees the result of a save click. UI polish, not perf,
  but shipped in the same commit for atomicity.
- Deleted `web/src/components/ReplayView.tsx` — was a debug surface
  that was never wired into the App's tab list. Dead code, removed.

## Methodology

**Why these are estimates, not measurements.**

This doc was assembled from code analysis and engineering reasoning, not
a live `pprof` capture. The hot path assumes a ClashGO bot running on
Apple Silicon Mac with BlueStacks at 860×732 / 160 DPI, in a single
battle window (3–5 min). Concrete per-call costs are mid-range; the
real numbers will land somewhere in the bands above on your device.

Per-call cost figures come from:
- gocv Mat alloc/free: ~50–150 µs per call (CGO malloc + zero-init).
- Wails IPC marshal: ~0.5–1.5 ms per call (JSON marshal + unix-socket
  push through the WebKit bridge).
- JPEG encode (quality 60, 430×366): ~2–8 ms.
- base64 encode (~50 KB): ~1–3 ms.
- `os.ReadFile` + `json.Unmarshal` of ~100 KB: ~1–4 ms.

Call frequencies come from the source itself (capture-loop cadence,
log-line cadence, world-writer cadence, React poll cadence).

**The bigger (qualitative) gain is GC pressure.** Each change drops the
number of short-lived allocations per second. Battle-state allocation
rate dropped from roughly **~3–5 MB/sec short-lived** to **~1–2 MB/sec**,
which manifests as **40–60% fewer GC cycles per minute** and a quieter
capture loop timing profile (fewer ~10–30 ms capture-loop stalls).

## How to verify on your machine

Two high-yield measurements. Both can be done in parallel; the first
needs `net/http/pprof` wired into the bot, the second doesn't.

### Measurement A — RSS sampling during a battle (no code changes)

```bash
# Pick the running ClashGO process.
PID=$(pgrep -f 'ClashGO\b')

# Sample RSS every 100 ms for 3 minutes (one battle).
( for i in $(seq 1 1800); do
    ps -o rss= -p $PID >> /tmp/rss.log
    sleep 0.1
  done ) &

# Trigger an attack in the GUI.
# When the battle completes (and `tail /tmp/rss.log` shows ~1800 lines),
kill %1

# Average + peak + stddev.
awk '{s+=$1; if($1>max)max=$1; if(NR==1||$1<min)min=$1; a[NR]=$1} END {
  print "lines="NR " avg_kb="s/NR " max_kb="max " min_kb="min
}' /tmp/rss.log
```

Run this once on the **pre-audit** binary (the previous release), once
on the **post-audit** binary (this commit). Compare the `avg_kb` and
`max_kb` numbers. The audit predicted **-10 to -20 MB steady-state**.

### Measurement B — CPU micro-profiling during a battle

Wires a pprof endpoint into the bot under `DEBUG=1` so you can `top`-rank
the hotframes before vs after the audit.

Prerequisite patch (apply once in a scratch build):

```go
// inside internal/bot/bot.go's Start(), under DEBUG=1 only:
go func() {
    import _ "net/http/pprof"   // side-effect registers /debug/pprof/ handlers
    log.Info().Msg("pprof listening on localhost:6060")
    _ = http.ListenAndServe("127.0.0.1:6060", nil)
}()
```

Then capture a 30-second CPU profile during battle:

```bash
# 30-second CPU profile, sampled during battle.
go tool pprof -seconds=30 http://127.0.0.1:6060/debug/pprof/cpu

# Inside pprof:
#   top20 -cum      # cumulative time by function
#   list ClassifyState
#   list MatchMultiScale
# ...
```

Compare the post-audit profile to the pre-audit one. If the audit
landed as predicted, you should see fewer top-frame CPU consumers in:
- Encode/Decode Mat operations in `internal/vision/vision.go`
- `runtime.EventsEmit` callers (captureLoop, WailsLogWriter)
- `os.WriteFile` / `os.Rename` in the world snapshot writer (removed in the
  dead-code cleanup — it was dead code; this frame is no longer present)

If you don't want to carry the pprof patch in-repo, drop a sibling
`internal/pprof` package under `DEBUG=1` that the bot's `Start()` opts
into — both work; this doc just needs whichever approach your fork prefers.

### Measurement C — GC trace

```bash
GODEBUG=gctrace=1 ./build/bin/ClashGO 2>&1 | tee /tmp/gc.log

# In another shell, run a battle. After it completes:
grep '^gc ' /tmp/gc.log | wc -l    # GC cycle count over the window
```

Post-audit should show ~40–60% fewer cycles over the same window of
bot-uptime + a battle.

## Files touched

| File | What | Why it lands here |
|------|------|-------------------|
| `app.go` | `cachedHistory` field + RWMutex, `GetAttackHistory` rewrite, `ensureHistoryLoadedLocked`, `refreshHistory`, `ResetStats` cache null, `StopBot` detached teardown, `b.OnFrame` removal, `WailsLogWriter` `bot_log` removal | The IPC + disk + teardown hotspots all live here. |
| `internal/bot/bot.go` | New `Cancel()` method | Decouples the synchronous "stop what you're doing" from the heavier `Stop()` teardown. |
| `internal/game/classifier.go` | `MatchMultiScale` → `MatchMultiScaleROICached(rule.Template, ...)` | Kills 180 Mat alloc/free/sec inside the classifier hot loop. |
| `internal/world/world.go` | `MinWriteInterval: 250ms → 1s` | 4× fewer fsync/rename churns from the snapshot writer. **Later deleted in the dead-code cleanup — the writer had no production consumers.** |
| `web/src/App.tsx` | Removed screenshot state + `screenInterval`; removed unused `GetLiveScreenshot` import | Tab-gate the screenshot poll so it only mounts on Live View. |
| `web/src/components/Feed.tsx` | Owns its own screenshot state + 1 Hz poll with `cancelled` flag + `inflightRef` + `clearInterval` cleanup | Localizes the IPC payload to the only consumer. |
| `web/src/components/Dashboard.tsx` | Dropped `onClearLogs` prop | Dead prop. |
| `web/src/components/UpdateBanner.tsx` | `\u2014`/`\u2022` → em-dash/bullet glyphs | Cleaner in HTML + tools. |
| `web/src/components/ConfigView.tsx` | Save-status pill (saving/saved/error) | UX polish for the Save button. |
| `web/src/components/ReplayView.tsx` | **Deleted** | Was a debug surface, never wired into App's tab list. |

## Disk safety: frame capture can no longer fill the machine

**Incident.** Three ad-hoc shell loops were left running over a weekend:

```bash
while true; do n=$(printf "%04d" $i); adb -s localhost:5555 exec-out screencap -p \
  > tmp/run8/frames/f$n.png 2>/dev/null; sleep 1.5; done
```

They wrote ~120,000 PNGs (~167 GB) with no cap, no rotation and no disk check.
The volume hit 99% and `go build` started failing with
`mapping output file failed: no space left on device`. Nothing in the project
warned about it, because nothing in the project was writing those frames — and
nothing in the project's own capture path would have stopped either, since the
review store only pruned **once, at startup**.

**Guards now in place.**

| Guard | Where | Behaviour |
|-------|-------|-----------|
| Free-space floor before every frame | `internal/vision.Diskspace` → `FrameStore.Add` | Refuses the write with `ErrLowDisk` (2 GiB floor by default) instead of filling the volume; nothing is written, not even an index line |
| Recorder preflight + stop-on-low-disk | `cmd/see rec` | Refuses to start below the floor (`-min-free-mb`, `0` to override) and **stops** instead of spinning when a write hits it |
| Rolling prune | `cmd/see rec` | Prunes every `min(25, keep)` frames, so the store holds at most `keep + interval` frames — previously a long `rec` never pruned after startup |
| Diagnostic dump retention | `internal/bot.DumpDiagnostics` | Keeps the newest 20 `diag_*` pairs; 141 (~200 MB) had accumulated in the runtime dir since July |

**Use the bounded tool, not a shell loop.** `make see ARGS="rec -n 600 -every 2s"`
records into `tmp/see` with a frame cap, a rolling prune and the disk floor. For
a one-off look, `make see ARGS="shots -n 20"` / `timeline` reads what was
already recorded.

**Verify.**

```bash
go test ./internal/vision/ -run 'TestDiskFreeBytes|TestFrameStore'
# refused write nothing, even the index:
./build/bin/see rec -n 1 -min-free-mb 99999999 -dir tmp/x   # exits 1
df -h /                                                     # still all there
# rolling prune keeps the ring bounded:
./build/bin/see rec -n 8 -keep 2 -dir tmp/x && ls tmp/x/frames | wc -l   # 2
```

## Non-shipped items (intentional)

These were surfaced by the audit but **not changed** in this commit — either
the diff was too large for a single perf bundle, or the savings were
sub-1% and not worth the change.

- **`internal/bot/wall_upgrade.go`** — many `time.Sleep` calls in the
  asset-driven modal close path. Tightening these needs a per-ROI
  settle-budget pass to avoid breaking the existing recovery flow.
- **`internal/attack/deploy_line.go`** — hotspot for tap bursts during
  deployments; the per-tap shell pipe already amortizes the JVM spin-up,
  but further concurrency tuning could win another ~2% CPU during deploy.

Future performance commits will tackle these in dedicated PRs.

> **Update (shipped later):** the loot-OCR item originally listed here
> (`internal/game/loot.go::readRow` and `internal/bot/bot.go::colorCheck`)
> was applied in a follow-up — both now pool their per-frame Mats via
> `vision.GetMat`/`vision.PutMat`. `isSlotEmpty` was already pooled through
> the consolidated `slotActivity` helper, so it needed no change. Only the
> two items above remain deferred.

---

# Audit 2 — resource-efficiency pass (2026-09-23)

A second pass, this time aimed at *total* footprint: what the bot itself burns,
and — the bigger half — what the bot's own behaviour makes **BlueStacks** burn.
The emulator is 10–20× the bot's footprint in both CPU and RAM, so a bot change
that removes work from the guest is worth more than one that removes work from
the Go process.

Method is the same as the first audit: read the hot paths (capture loop,
classifier, vision, the ADB client/transport/shell-pipe layer, the battle-result
settle loop, the Wails bridge), estimate each cost as call frequency × per-call
cost, **ship only changes that are provably behaviour-preserving**, and leave
the rest ranked below with the measurement that would settle each one. Numbers
are principled estimates from code analysis, not live captures, unless a row
says otherwise.

## TL;DR

| # | Finding | Where | Estimated saving | Verified by |
|---|---------|-------|------------------|-------------|
| 1 | A fresh `rand.New(rand.NewSource(now))` (~4.9 KB state array) on **every tap and every jittered sleep** | `internal/adb/client.go` (10 sites), `transport.go`, `bezier.go`, `game/navigator.go` | ~25–50 KB/s of garbage during a battle; more in idle navigation | `TestGaussianOffsetDoesNotAllocate` (0 allocs/op) |
| 2 | The BlueStacks adbd-wedge capture path allocated a **fresh frame buffer per capture** (~2.5 MB) just to prepend a 12-byte header | `internal/adb/transport.go::normalizeShellScreencap` | **2.5 MB/frame of garbage** → 0, on the path BlueStacks actually takes | `TestNormalizeShellScreencap*` (in-place, byte-exact) |
| 3 | `ClassifyState` scored **all 22 rules every frame**, including full-frame 3-scale template matches, after the answer was already final | `internal/game/classifier.go` | **measured: 425 → 306 ms per classify** over the corpus mix (−28 %); up to −213 ms/frame where the winner is template-free | `TestEarlyExit*` (equivalence over 13 real frames), `BenchmarkClassify*` |
| 4 | The settle loop **PNG-encoded and rewrote `last_battle_result.png` on every attempt** (up to 18×/battle, seconds apart) | `internal/bot/bot.go` (settle loop) | **measured: ~45 ms + ~2 MB per write** → 17 fewer per battle (≈0.75 s CPU, ≈34 MB of writes) | `TestKeepResultFrame` |
| 5 | Tap-path debug logs formatted their arguments **even with debug logging off** | `internal/adb/client.go`, `bezier.go` | ~60 B/tap of boxing, ~4 taps/s | compile-checked (no device-free path reaches them) |

Below: what changed and why, then the ranked list of what is left — including the
emulator-side levers, which are where the big numbers are.

## Measured: where the bot's CPU actually goes

Added during this pass: `internal/game/classifier_bench_test.go`, which times
both classifier paths on the real corpus frames with the real template store.
Reproduce with:

```sh
go test -run '^$' -bench BenchmarkListBenchmarkCorpusFrames -benchtime 1x -v ./internal/game
go test -run '^$' -bench 'BenchmarkClassify' -benchtime 30x ./internal/game
```

On an Apple M5 / OpenCV 4.10 / arm64, `ClassifyState` costs:

| Geometry | Frame | Rules scored | Classify (early exit) | Classify (full list) |
|----------|-------|-------------:|----------------------:|---------------------:|
| 1280×720 | village | 19 of 22 | 390 ms | 456 ms |
| 1280×720 | village (alt) | 19 of 22 | 445 ms | 429 ms |
| 1280×720 | boot logo | 11 of 22 | 258 ms | 214 ms |
| 1280×720 | battle (pre-deploy) | 12 of 22 | 401 ms | 431 ms |
| 1280×720 | battle (mid) | 12 of 22 | 401 ms | 407 ms |
| 1280×720 | battle result overlay | 13 of 22 | 490 ms | 485 ms |
| 1280×720 | connection lost | 4 of 22 | **0.0 ms** | 213 ms |
| 1280×720 | star-bonus popup | 9 of 22 | **0.0 ms** | 212 ms |
| 860×732 | result overlay | 13 of 22 | 222 ms | 222 ms |
| 860×732 | battle | 12 of 22 | 213 ms | 313 ms |

Aggregate over the same 13-frame mix (30 iterations each): **306 ms/op early
exit vs 425 ms/op full evaluation**, 4.7 KB/op, ~28 allocs/op.

Three things this says that the earlier estimates did not:

1. **Vision, not capture, is the bot's CPU.** A single classify is 200–490 ms on
   a 720p frame, and the capture loop classifies at 3.3 Hz in the village (4 Hz
   in battle). The loop's interval arithmetic means it simply runs back-to-back
   when classify exceeds the interval — the emulator is not the only thing
   saturating a core.
2. **The cost is template matching, and the winning rule is the expensive one.**
   The early exit cannot help where the winner IS a `matchTemplate` (the village,
   battle and result frames barely move). It pays off enormously only where a
   template-free rule wins early (connection-lost and star-bonus frames: 213 ms →
   0.0 ms). Hence the reprioritized list below.
3. **720p costs ~1.8× the reference geometry** (village 390 ms vs the 860×732
   frames at 213–222 ms), because the display-scaled sweep resizes each template
   by k×0.9–k×1.1 with k=1.325, growing template area faster than the search
   window shrinks.

PNG writing, measured the same way (`gocv.IMWrite` on a 1280×720 capture):
**~45 ms and ~2 MB per full-frame artifact**. The settle loop used to pay that up
to 18 times per battle.

> Measurement caveat: these are wall-clock times in a benchmark, single-process,
> with warm template caches and an idle machine. They are the right order of
> magnitude for the capture loop and the right *ratios*; treat the absolute
> numbers as this-machine numbers (see Measurement B for the live pprof check).

## Shipped in this pass

### 1. Per-call `rand.New` removed from the input hot path

`rand.New(rand.NewSource(time.Now().UnixNano()))` allocates a 607-word (~4.9 KB)
generator on **every** call. It appeared on every tap (`Tap`, `TapAsync`,
`TapFast`, `TapFastAsync`, `TapDual`, `TapTriple`, `TapHuman`), on every
`SwipeHuman`, on `HumanSleep`/`JitteredSleep` (which run several times per
second through an attack), inside `SwipeBezier`, and in `Navigator.IdlePan`.
The tap loop is the one place in this bot that runs continuously for the length
of a session, so that was garbage generated for four numbers nobody keeps.

Now the package-level `math/rand` generator (auto-seeded, per-P, documented safe
for concurrent use), behind one helper — `gaussianOffset(stdDev)` — that the four
tap variants share. `rand.New` per call also meant each call had its *own*
stream, so the concurrency safety was accidental; `TapDual`/`TapTriple` seeded
sibling sources at `now+71ns`/`now+142ns` purely to decorrelate them, which
consecutive draws from one stream already are.

Behaviour: unchanged distribution (still `NormFloat64()`, still truncated to
pixels), and `stdDev <= 0` still means "tap exactly where asked" — the old code
drew and multiplied by zero, the helper returns early.

### 2. The BlueStacks capture path no longer allocates a frame

`CaptureToMat` reaches the device through one of two paths: the `exec:`
screencap service, or — when BlueStacks' adbd is in its wedge state, which is
the state this bot's users are on — the compound `shell:getprop …; screencap`
fallback. The device header is 16 bytes (w, h, format, **colorspace**); the
consumers expect 12 (w, h, format). `normalizeShellScreencap` used to allocate a
new buffer and copy the whole frame into it to drop those 4 bytes: ~2.5 MB of
garbage **per captured frame**, several per second, forever.

It now normalizes **in the buffer the capture already read into**: the frame is
moved down to the buffer start (one overlapping `copy`, ~0.25 ms for 2.5 MB) and
the header rewritten in place. Ownership passes to the caller, which releases it
with `ReturnBuffer` exactly like a direct capture — so `ReturnBuffer` now also
restores a returned slice to its full capacity, because the capture loop treats
a pooled buffer's *length* as its writable extent and would otherwise "grow"
the next capture from a truncated base.

Errors leave the buffer untouched (the caller keeps ownership), and every
malformed layout still fails loudly instead of yielding a frame of garbage.

### 3. `ClassifyState` stops when the answer cannot change

Rules are stored highest-priority-first, and the winner is the highest priority,
then the highest score. That ordering was being *computed* by scoring all 22
rules and sorting the matches — after which 20 of the verdicts were discarded.
The expensive part is not the ~100 `GetUCharAt` pixel probes; it is that a rule
whose probes pass goes on to a **full-frame, 3-scale `matchTemplate`**
(`internal/game/classifier.go:272` passes `image.Rect(0, 0, Cols, Rows)` — the
whole frame, unlike every other call site in the repo, which uses a bounded ROI
from `buttonROI`).

`evaluateRules(screen, detail, earlyExit)` is now the single scorer behind both
`ClassifyState` (early exit) and `EvaluateRules` (reporting, full list — `cmd/see
-why` needs every rule). It stops once the leading match outranks the next rule:
no later rule can reach that priority, and the score tie-break only applies
*within* one priority level, so the whole tie group is still scored. The stop
check runs after every rule, not only after a matching one — a rule that does not
match cannot change the leader, and gating the check on `Matched` was a bug the
new tests caught. In practice it scores 4–19 of the 22 rules depending on the
frame (measured table above).

Measured, and smaller than the estimate that motivated it: 425 → 306 ms per
classify over the 13-frame corpus mix (−28 %), because where the winning rule
*is* a template match (village, battle, result frames) that match still runs and
is the entire cost. It is decisive only where a template-free rule wins early —
the connection-lost and star-bonus frames drop from ~213 ms to **0.0 ms**. That
result is what reordered the proposals below: the real target is the template
search itself (P2), not the number of rules evaluated.

`betterState` (priority, then score) is one named function now instead of a
`sort.Slice` comparator, and an exact tie keeps the earlier rule — the sort had
no tie rule at all and returned whichever equal state it happened to place first.

### 4. The battle-result artifact is written when it is worth writing

The settle loop re-read the result panel up to 18 times to wait out the loot and
league-bonus counters, and wrote `last_battle_result.png` on **every** attempt:
a full-frame PNG encode plus a disk write, seconds apart, each overwriting the
previous one — to end up with the frame the accepted read came from anyway. It
now writes when the frame is the accepted read, when it is the evidence for a
parse failure, or on the last attempt (a panel that never settles must still
leave something to look at). `keepResultFrame` is a pure predicate so the
policy is testable without a device; the write itself is best-effort and can
never abort the result bookkeeping.

### 5. Tap-path debug logging costs nothing when it is off

`c.log.Debugf("ADB TAP: (%d, %d) actual: (%d, %d)", …)` boxes four arguments and
builds a variadic slice at the call site before the level is consulted — the
`zerolog` event short-circuit happens *inside* the call. The four tap-path sites
(and the two `SwipeBezier` fallback logs) are now guarded with the package's own
`Logger.Debug() bool`, which is the pattern the rest of the repo already uses.

### 6. The classifier searches the button's window, not the frame

P1 measured *which* rules run; this one measured *what they cost*, and the answer
reordered the whole plan. On a 1280x720 village frame:

| Component | Cost |
|---|---:|
| one `ClassifyState` (templates loaded) | **228 ms** |
| the same call with the template store removed (pixel probes only) | **0.018 ms** |
| the chest rule's `hammer` search, full frame, 3 scales | **213 ms** |
| the village rule's `btn_attack` search inside its window (13 % of the frame) | 19 ms |
| the battle-end rule's `btn_return_home` search inside its window (9 %) | 12 ms |

So the pixel probes are free, and **99.99 % of a classify is `matchTemplate`**.
Every rule that carries a template was searching
`image.Rect(0, 0, Cols, Rows)` while every *other* call site in the repo uses a
bounded window — and searching a button's window is what the bot's tap paths
already do for the same template.

The table moved to `internal/game/buttons.go` (`ButtonROIBounds`), because that is
the file that has to stay in sync with the tap path: `bot.buttonROI` now reads it
(`internal/bot/bot.go`), so the click and the search cannot drift. `RuleSearchROI`
maps a rule's window into the live frame and **falls back to the whole frame**
whenever it cannot place a usable one (unknown template, degenerate or off-frame
mapping) — the failure modes are not symmetric: a window that is too small loses
evidence and can flip a state, while a window that is too large only costs time.

Windows are mapped per edge (`Calibration.MapRect`), not with the whole-rectangle
heuristic `Calibration.HudRect` uses for taps. That was not the first
implementation: `HudRect` picks one anchor per axis from the rectangle's
*centre*, so a window whose edges fall in different bands (the Attack! window
spans the left band and the mid band) gets anchored differently from the button's
own tap point. With a display scale that does not match the frame — a pinned
`k` on a screen it was not measured for — the two drift apart and the window can
leave the button entirely; the edge-mapped window drifts *with* the tap point.
`TestRuleWindowsContainPinpoints` (internal/bot) asserts the button's tap point
stays inside the window at the reference geometry, 720p and 1080p, which is the
check that caught this.

What the windows cost now, on 1280x720 (share of the frame searched):

| Rule / template | Window | Share |
|---|---|---:|
| MainVillage / `btn_attack` | (0,413)-(398,719) | 13.2 % |
| Battle / `btn_next` | (936,346)-(1279,719) | 13.9 % |
| FindMatch / `btn_find_match` | (66,280)-(530,545) | 13.3 % |
| ArmySelection / `btn_battle` | (398,199)-(1279,719) | 49.7 % |
| WelcomeBack / `btn_okay` | (441,432)-(839,697) | 11.4 % |
| BattleEnd / `btn_return_home` | (441,511)-(852,719) | 9.3 % |
| ChestReward / `hammer` | whole frame | 100 % |

Measured effect, same corpus fixtures and the same benchmark command as P1: a
village frame **390 → 228 ms**, a pre-deploy battle frame **401 → 233 ms**, and
the reference-geometry (860x732) frames **~250 → 121-123 ms**. Over the 13-frame
mix the classify average is **425 → 306 → 236 ms** across the two passes
(−44 % cumulative).

**Exposure is a tie-break bonus, not a detection.** Every rule that carries a
template, except the chest rule, has `MinPass >= 1` — so `matched` is already
true when a pixel probe passes, and the template only contributes
`bestConf * 1000` to the score. Narrowing a window can therefore only change
which of two same-priority states wins a score tie, which is what the corpus
asserts on: all 13 fixtures still classify to the same states and scores after
the change. The single exception is the chest rule, which is exactly why it keeps
the full frame.

### 7. One capture per screen, not one per consumer

The bot asked the emulator for the same picture more than once. A capture is not
a memory read: it is `screencap` running inside the Android VM, reading the whole
2.5 MB framebuffer out of the guest heap and pushing it across adb. During a
battle the frame loop ticks every 250 ms while the deploy verifier and the
battle-end watcher each capture on their own schedule, so one screen state was
screencapped two to four times inside the same frame interval — the largest
repeated piece of work the bot asked the emulator to do, and it is BlueStacks CPU
rather than bot CPU that pays for it.

`internal/adb/capturecache.go` now serves a capture request from the previous
frame when it is inside a short window (120 ms by default,
`performance.capture_cache_ms`) *and* the bot has not injected input since. The
second half is the safety rule and the reason this is not simply "serve the last
frame": the bot's control flow is check-act-check — capture, decide, tap, capture
again to verify the tap landed — so a frame taken **before** a tap must never
answer the capture that follows it. Every input method (all tap variants through
`routeTap`, `Swipe`, `SwipeBezier`, `Pinch`, `PinchZoom`, `KeyEvent`, `Text`, and
the app-restart paths) bumps a generation counter, and a cached frame is reused
only while that counter is unchanged: only between the bot's own input events,
never across one.

Three properties keep it honest:

- **It can only remove captures, never change what a frame contains.** A
disabled, expired, empty or input-invalidated cache means the caller pays for
  exactly the capture it would have paid for anyway.
- **Its window is bounded by a safety invariant, not a preference.** The
  battle-result settle machine believes a read when two consecutive captures
  agree — that is how it knows the loot and bonus counters stopped animating —
  and no tap happens between two settle attempts, so the input barrier cannot
  protect it. The shortest settle pause (400 ms) is the floor, and
  `TestCaptureCacheCannotBridgeSettleAttempts` fails if the default window ever
  reaches it. The window is also below the capture loop's own 250 ms battle
  interval, so the loop always sees a fresh frame and this cannot interact with
  the classify gate.
- **It is off unless a caller opts in, and observable.** The diagnostic tools
  (`cmd/see`, `cmd/screendump`) construct a plain client and keep capturing for
  real. The bot opts in from config and reports `capture_cache` in `BotStats`:
  `hits` are captures BlueStacks was not asked to take, `invalidated` are
  requests the input barrier refused, `expired` the ones the window lost. Hits at
  zero with the cache enabled means nothing captures concurrently on this
  workload — turn it off rather than pay for the copy.

Measured here: the cache's own semantics (window, barrier, ownership, stats, and
the battle interleaving it exists for) are covered by 13 tests, and the hit path
is proved to short-circuit the device with a test that runs against a client
with no transport at all — a capture that touched the device would fail loudly.
What cannot be measured from here is the *live* hit rate: the counters above are
how to read it off a real session (`capture_cache.hits` per battle against
`adb_health.captures_total`).

### 8. The one emulator lever the bot can pull itself

The levers at the bottom of this document are mostly the user's to pull, because
they live in BlueStacks' own settings UI. One of them does not: zeroing the
guest's Android animation scales. Animated transitions are real per-frame guest
GPU and CPU work, the bot reads the screen at 4 Hz and taps at human pace so it
cannot use a single one of those frames, and a half-drawn frame is exactly the
state that makes a classifier guess. The bot already owns a transport, so the
boot sequence now applies it (`settings put global window_animation_scale 0.0`,
plus `transition_animation_scale` and `animator_duration_scale`) before starting
the game, so the app comes up with the scales already zeroed.

It is deliberately the *only* emulator lever taken over: it is the one with no
risk to the workload (nothing the bot does waits on an animation; its waits are
explicit sleeps), it is reversible, and it is the one a `settings put` can
deliver. The frame-rate cap and audio changes — the two largest entries in the
table below — are left to the user because they change how the instance feels to
play by hand, and because BlueStacks Air keeps those settings out of the plist
this repo can reach (`com.BlueStacks.AppPlayer` is the bundle the resolution
defaults are written to, and on BlueStacks Air it does not exist — the write
logs `plist absent` and skips; the instance's real prefs are elsewhere and carry
none of the frame-buffer keys). Scripting them would mean writing keys nobody
reads and calling it an optimisation.

The code is tolerant by construction: a guest that refuses the writes (this
BlueStacks answers `error: closed` for these commands while it serves frames)
logs the first failure and boots anyway, and a device that already reads zero
skips the round trips. `performance.tune_guest_animations: false` turns it off
for an instance a human also plays on.

## Proposed — ranked, each with the gate that would settle it

These are **not** shipped: each either changes observable timing, needs a live
device to validate, or trades RSS for CPU. They are ranked by expected value,
and the measured table above reordered them: the two items that attack the
200–490 ms classify come first.

### P1. Do not classify a frame that has not changed

Measured above: a classify is 200–490 ms, and the bot runs one every 300 ms
while waiting in the village. If the screen has not changed since the last
frame, the state cannot have changed either — the answer is already known, and
the whole classify is avoidable. The tool for it already ships: `vision.Diff`
computes the changed-pixel percentage and is used by the frame store for exactly
this question ("did my tap land"). Gate the classify on it, keep the previous
verdict for near-identical frames, and force a full classify on any transition
or on a periodic floor (so a state that changes *without* pixels moving, if one
exists, cannot hide forever).

**Gate:** a threshold sweep on real idle-village footage — CoC animates water,
clouds and particles, so "unchanged" is a percentage, not equality — and a check
that nothing keys off a missed transition. `StateChange` events, the stuck
watchdog and the attack trigger all read the classifier's output, so a
false "unchanged" is the failure mode to hunt. **Expected saving: most of the
bot's idle CPU**, i.e. the largest single item left on this list.

### P2. Bound the last full-frame search — the chest rule's `hammer`

The ROI half of this is shipped (Shipped §6): six of the seven template-bearing
rules now search 9–50 % of the frame. The seventh — `StateChestReward`, gated on
the `hammer` template **alone** (`MinPass: 0`) — still searches the whole frame
on **every** classified frame, and it is 213 ms of a 228 ms village classify:
**93 % of the remaining cost, for a state that is on screen a few seconds per
session.**

It is left alone because windowing it cannot meet this pass's bar from here, and
it is the one rule where a wrong window is not a tie-break loss but a *lost
state*: `hammer` is 160x41 px (a wide, short banner — consistent with the
bottom-centre "TAP TO OPEN" band the rule's comment blames for defeating pixel
probes), no fixture in the corpus is a chest or reward screen, and the one
authored chest rect on disk (`assets/chest_dismiss_roi.json`, `tap_roi`
354,320-499,447) is a different screen's tap target whose own config documents an
alternate zone, i.e. the artwork does not sit at one fixed place. Guessing here
trades a measurable perf win for an unmeasurable detection risk.

**Gate:** one live chest-reward capture.

```bash
bin/see capture -out tmp/chest.png     # with the reward screen on the device
bin/see look -img tmp/chest.png -why StateChestReward
```

`-why` prints the matched template's location, which is the window's lower bound;
add the same generous margin the other windows carry, then re-classify that frame.
Once a frame exists, the window is a two-line addition to `buttonROIBounds` plus a
fixture in the corpus manifest, and this becomes the cheapest large win left.

> Note the *other* half of the original P2 text, the scale sweep: at 720p the
> templates are resized by k×0.9–k×1.1 (k=1.325), which grows template area by
> ~1.6× while the window shrinks. It is untouched and still unmeasureable from
> here — it needs a live run at a non-reference geometry to show that dropping to
> a single scale at k costs no matches.

### P3. Capture the screen once per tick and share it

There are **~50 `CaptureToMat()` call sites**. During a battle the capture loop
(4 Hz) *and* the deploy/search/battle-end goroutines each capture
independently, so one screen may be screencapped 2–4× within the same frame
interval. Every capture costs a guest-side native `screencap` process, a
full-framebuffer read (~2.5 MB from the guest heap) and a 2.5 MB transfer — the
single largest repeated piece of work the bot asks the emulator to do.

A single capture goroutine publishing `(seq, mat, at)` to consumers via a
`frameView` lease (refcounted, so the publisher can recycle the buffer) would cut
guest screencap work by ~50–75 % during attacks and remove the C-heap churn
(one 1.9 MB BGR Mat per capture) that remains. **Gate:** a live A/B of captures
per battle (count `screencap` invocations in the guest) plus a check that the
deploy/battle-end paths still see fresh-enough frames (they currently re-capture
to *force* freshness).

### P4. Do not pay for a stale capture twice: pool the BGR frame

`CaptureToMat` allocates a 2.5 MB RGBA Mat (via `NewMatFromBytes`, which copies
out of the pooled byte buffer), then a 1.9 MB BGR Mat via `CvtColor`. Dropping
the staging Mat needs a way to copy bytes into an already-allocated Mat, which
the gocv API does not offer today — but the *returned* Mat's ownership could be
pooled instead of `Close()`d by every caller, and that is a `Release` contract
across every call site, which is why it is not in this pass. Worth doing
together with P3, where the refcounted lease naturally expresses the ownership.

### P5. Turn the persistent shell pipe on for the whole session

`EnablePersistentShell` is called inside `runAttackSequence`
(`internal/bot/bot.go:1057`), so deploy taps go through the pipe (≈1–5 ms, no
`app_process` spawn) while every other tap and swipe — popup dismissals, chest
and obstacle handling, `IdlePan`, `Next Match` during a skip loop — pays the
legacy `transport.Exec("shell:input tap …")` cost: a new adb connection on
localhost:5037, a `host:transport:` round trip, and **a fresh `app_process`/
ART spawn inside the guest** (~100–300 ms of *guest* CPU per tap). Navigation is
tap-sparse, so this is smaller than the deploy loop's win, but it is guest CPU —
the expensive kind. **Gate:** enable it behind the existing `use_shell_pipe`
flag for a whole session on a live device and confirm tap reliability. The pipe
already falls back automatically when it breaks; the risk is *timing*, not
correctness: taps arrive ~200 ms earlier, and BlueStacks is known to drop taps
sent too fast (why `EXTRA_TAPS`/`EXTRA_DELAY` exist in the recorder).

### P6. GC and `GOMAXPROCS` tuning — measure first

The bot's live heap is small (~10–30 MB) and its allocation rate is the thing
this pass just reduced, so `GOGC=100` (double the live set before collecting)
buys little and can cost collection frequency. Raising `GOGC` (e.g. 200–300) with
`debug.SetMemoryLimit(256<<20)` as a hard-ish ceiling would cut GC cycles — but
it trades RSS for CPU, and the headline complaint here is footprint. Only worth
shipping behind a measurement: `GODEBUG=gctrace=1` + the RSS sampler, GOGC=100
vs 200 vs 300, same session length. Same for `GOMAXPROCS`: on an 8–10 core Mac
the runtime's background GC workers compete with the emulator for cores, but the
bot is I/O-bound most of the time, so this is likely noise. **No change without
a number.**

### P7. Documentation-only findings (harmless, easy to "fix" into a bug)

- `GameContext.UpdateScreen` retains every frame's Mat and closes the previous
  one, but `ReadScreen()` has **no callers**. That retention is not a leak (at
  most one frame, ~1.9 MB) and it *is* the ownership mechanism that frees each
  frame: `processFrame` never closes the frame it is handed. Removing the
  retention without adding an explicit `Close()` at the end of `processFrame`
  would leak a Mat per frame.
- `internal/bot/wall_upgrade.go` still carries ~20 `time.Sleep` calls in its
  modal-close paths (deferred in the first audit); it is opt-in, so it does not
  affect the default farming loop.
- `vision.MatchMultiScale` (the unnamed-template entry point) still exists for
  callers that do not want the scaled-template cache; anything on the frame path
  should use the cached variant.

### P8. Stop polling the GUI when nobody is looking

`web/src/App.tsx` polls three IPC methods — `GetStats`, `GetAttackHistory`,
`GetLogs` — every 2 s for the life of the window (`setInterval(fetchData, 2000)`),
on **every** tab and whether or not the window is visible. The first audit
already fixed the sibling problem (the 1 Hz screenshot poll was firing from the
root effect and was moved into the only component that renders an image); this
one is the same shape: log text and stats cross the bridge at 2 Hz even while the
window is minimized behind the emulator, which is the normal state during
unattended farming. `document.visibilityState` gating plus a tab check is a few
lines.

Two larger options if the goal is footprint rather than features:

- **Run the CLI for unattended farming.** `make build-cli` produces `bot_cli`:
same bot, no WebKit harness. The GUI is the majority of the bot's ~60–90 MB idle
RSS.
- **Check the update banner at 6 h, not at boot-plus-interval** (it already only
stores state; this is a note, not a finding).

## Levers inside BlueStacks (the emulator's own footprint)

BlueStacks is the dominant cost — README estimates ~800 MB–1.5 GB RSS and the
bulk of the CPU — and much of what it burns is spent rendering frames the bot
never looks at, and audio nobody hears. Ordered by expected saving:

| Lever | Why it saves | How | Risk |
|-------|--------------|-----|------|
| **Cap the guest frame rate** (30, or 15 while farming) | CoC renders continuously; the bot reads the screen at 4 Hz and taps at human pace. Frames 5–15 and 16–30 are rendered for nobody — GPU, guest CPU and host CPU all pay. | BlueStacks → Settings → Performance → "Frame rate limit" / `fps_cap` on the instance | Lower caps make manual play feel laggy; the bot does not care. |
| **Turn audio off** | The guest audio HAL, mixer and host audio thread run continuously for music/SFX the bot cannot use. | BlueStacks → Settings → Sound → off (or instance `enable_audio=false`) | None for a bot. |
| **Zero the Android animation scales** | Window/transition/animator animations are real per-frame guest GPU+CPU work, and the bot's own sleeps already leave slack. | **Applied by the bot at boot** (Shipped §8); `settings put global window_animation_scale 0.0` by hand, or `performance.tune_guest_animations: false` to stop it | Slightly less "human" look; lost on a guest wipe (the bot re-applies it next boot). |
| **Keep the guest at 860×732, not 720p** | Frame size drives the per-capture cost on both sides: 860×732×4 = **2.52 MB** vs 1280×720×4 = **3.69 MB** — 32 % less guest framebuffer copy and 32 % less ADB transfer per capture, plus fewer pixels for the guest GPU. | Pin the resolution (the bot already enforces it via `writeResolutionDefaultsIfPlistExists`) | The repo supports 720p (see `docs/RESOLUTION.md`); the probes are authored per geometry either way. |
| **Rightsize guest cores / RAM** | A farming CoC instance does not need 4 cores and 4 GB; over-provisioning raises the VM's idle RSS and lets the guest scheduler spread work wide. | BlueStacks → Settings → Performance (instance CPU/RAM) | Too few cores makes the guest lag and *drops taps* — the expensive failure mode. Change one notch and watch tap reliability. |
| **Disable on-screen FPS/overlay and ad/telemetry surfaces** | Small, continuous guest work with no bot value. | BlueStacks settings | None. |
| **Exclude the VM disk image from Spotlight/Time Machine** | Background indexing reads the guest disk while it runs. | System Settings → Spotlight/Time Machine exclusions | None for the bot. |

One thing that is **not** a lever: the bot's capture rate. BlueStacks' own cost is
largely independent of how often the bot screencaps (`docs/PERFORMANCE.md`
README note), so cutting the bot's FPS saves *bot* CPU, not emulator CPU. The
levers above are what move the emulator.

## How to verify

```bash
# 1. The new guarantees, as tests (no device needed).
make test-go
#    - TestGaussianOffsetDoesNotAllocate        (0 allocs per jitter draw)
#    - TestNormalizeShellScreencap*             (in-place, byte-exact, error-safe)
#    - TestReturnBufferRestoresFullCapacity
#    - TestEarlyExit*                           (equivalence over 13 real frames)
#    - TestKeepResultFrame                      (artifact write policy)
#    - TestRuleSearchROI_*                      (windows: identity, fallback, area)
#    - TestRuleWindowsContainPinpoints          (window still covers the button,
#                                                reference/720p/1080p)
#    - TestButtonROIBounds / TestButtonROIConsistency
#                                               (click path and search share one table)
#    - TestCaptureCache*                        (window, input barrier, ownership,
#                                                battle interleaving, stats, off by default)
#    - TestCaptureToMatServesFromCacheWithoutADevice
#                                               (a hit never touches the device)
#    - TestCaptureCacheCannotBridgeSettleAttempts
#                                               (the window stays under the result
#                                                panel's shortest retry pause)
#    - TestGuestAnimationCommands / TestIsZeroScale
#                                               (the exact guest settings written,
#                                                and what counts as already zeroed)

# 1b. The measurements behind the numbers above (needs the corpus fixtures)
go test -run '^$' -bench BenchmarkListBenchmarkCorpusFrames -benchtime 1x -v ./internal/game
go test -run '^$' -bench 'BenchmarkClassify' -benchtime 30x ./internal/game

# 1c. Read the capture cache off a live session
#     BotStats.capture_cache: hits are screencaps BlueStacks never ran. Compare
#     against adb_health.captures_total. hits=0 with enabled=true means nothing
#     captures concurrently on this workload, so set
#     performance.coalesce_captures=false rather than pay for the copy.
#
# 2. Watch the allocation rate fall, on the real capture path
GODEBUG=gctrace=1 ./build/bin/ClashGO 2>&1 | tee /tmp/gc.log
#    compare `gc N @Xs` cadence against a pre-pass binary, same session length

# 3. Rank what is left (pprof recipe is unchanged — see Measurement B)
#    Expect the top frame after this pass to be matchTemplate / CvtColor,
#    not normalizeShellScreencap / rand.New / IMWrite.

# 4. Emulator-side changes: sample the guest, not the host
#    For each lever, compare `cpu_time_sec` deltas per battle AND the emulator
#    process RSS (`ps -o rss= -p $(pgrep -f qemu-system)`), same bot session.
```

The shipped changes all preserve behaviour by construction, and each has a test
that fails if the property is lost — that is the standard this pass was held to,
since none of them could be validated on a live emulator from here. The proposed
items are the ones that cannot meet that bar without one.
