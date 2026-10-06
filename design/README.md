# ClashGO UI — layout explorations

Six static mockups of alternative layouts for the ClashGO desktop GUI, plus a
shared design language that any of them can be ported onto.

> **Start with [`mockups/06-operator.html`](mockups/06-operator.html).** It is the
> recommended design — built from the bot's behaviour rather than from a visual
> reference. See [Why Operator](#why-operator) for the reasoning.
> Mockups 01–05 are earlier explorations kept for comparison; they were graded
> on looking distinctive, which is why they read worse as tools.

**Nothing here is wired to the app.** No file in `web/src`, `tailwind.config.cjs`
or the Go side was touched. These are self-contained HTML you can open, click
through, and compare. Pick one, then port it.

## Files

| File | Layout | Structure |
|---|---|---|
| [`mockups/00-index.html`](mockups/00-index.html) | Gallery | Live thumbnails, comparison matrix |
| [`mockups/06-operator.html`](mockups/06-operator.html) | **Operator** ⭐ | No navigation: status bar, live cycle rail, numbers, history, capture, console |
| [`mockups/01-ledger.html`](mockups/01-ledger.html) | **The Ledger** | Top nav, no rail; editorial report with a ranked results table |
| [`mockups/02-command-deck.html`](mockups/02-command-deck.html) | **Command Deck** | Icon rail + session tabs + card stream + slash-command bar |
| [`mockups/03-cockpit.html`](mockups/03-cockpit.html) | **Cockpit** | Nav / live run / inspector, with a persistent autonomy bar |
| [`mockups/04-telemetry-wall.html`](mockups/04-telemetry-wall.html) | **Telemetry Wall** | Dense dark instrument grid, permanent log column |
| [`mockups/05-dispatch.html`](mockups/05-dispatch.html) | **The Dispatch** | One scrolling chronological document, no tabs |

Open `00-index.html` first — the thumbnails are live iframes, so the gallery is
a real side-by-side.

## Why Operator

The bot's characteristics are the brief, and every part of the layout answers
one of them:

| The bot… | …so the design shows it |
|---|---|
| Runs unattended for hours | A permanent status pill: running/stopped, current state, elapsed time |
| **Is a state machine** (`MainVillage → Battle → BattleEnd → ArmyCamp`) | A live cycle rail — Search → Deploy → Battle → Result → Retrain — with the current bead, time in state, and a one-line “Now:” readout |
| Skips bases against loot floors | `137 skipped` sits in the Search step and in the cycle count; the floors are in *Next up* |
| Emits numbers, not prose | Four large tabular figures; no explanatory sentences anywhere on screen |
| Sees a screen you can't | A live 860×732 capture with the HUD, troop bar and the **computed red line** — and the overlay only appears during `BATTLE`, because showing a battle overlay on the home screen contradicts the state being reported |
| Fails in known ways (ADB, capture, stall, red line) | Health chip with latency + a reason-driven popover; per-row `Clean / Stall / Failed` flags with an expandable row showing undeployed slots and the deploy error |
| Is configured rarely | Config is a **sheet**, not a fourth destination — so the screen needs **zero navigation** (`nav, aside` → 0 elements) |

What it deliberately does *better than the current app*: no hover-expanding
sidebar (the current one shifts the whole layout on mouse-over), no `text-5xl`
page title eating ~120px, Start/Stop in one fixed corner instead of a sidebar
foot that moves, and the live game state — which the current UI never shows.

## How to view them

The gallery is a real HTML page, so just open it:

```bash
open design/mockups/00-index.html          # macOS
```

or double-click it in your file browser. Each mockup is a self-contained
file with no build step and no dependencies — the only network request is
Google Fonts (Newsreader / Inter / IBM Plex Mono / Spectral). Opening them
straight off disk works, and all five work that way.

To make the gallery's **live thumbnails** work (they load their siblings as
iframes), serve the folder rather than opening the single file:

```bash
cd design/mockups && python3 -m http.server 8000
# → http://localhost:8000
```

If a preview environment serves one file at a time, the gallery detects the
broken thumbnails and shows a notice instead of five empty boxes — the
"Open full size →" links on each card still tell you what to open.

## Two reference-driven directions

**01 — The Ledger**, from [vals.ai](https://www.vals.ai/home). The reference is
an editorial lab report: a serif headline that states the *finding* rather than
the page name, hairline rules in place of shadows, uppercase mono micro-labels,
tabular numerals with explicit variance, and — the part that matters — data as
a **ranked table with inline horizontal bar meters**, where each row is a small
report of its own. The adaptation swaps the benchmark leaderboard for an attack
history, and demotes the live console to a footnote.

**02 — Command Deck**, from the Freebuff app itself. The reference is a dark
`#0a0a0a` agent shell: a narrow icon rail, a strip of open session tabs, a
centered card stream, and a composer that is the app's spine rather than a
form. The adaptation keeps that skeleton and makes Start/Stop a *command*;
Config, which the reference demotes, gets recovered as a slash command
(`/strategy`, `/loot`) plus a dedicated rail item.

## Comparison

| Layout | Live monitoring | Reviewing history | Tuning config | Distinctiveness | Build cost | Fits 1200px |
|---|---|---|---|---|---|---|
| 01 The Ledger | Fair | **Best** | Good | High | Low | Fair |
| 02 Command Deck | Good | Good | Weak | High | Medium | **Best** |
| 03 Cockpit | **Best** | Good | **Best** | Medium | Medium | Fair |
| 04 Telemetry Wall | **Best** | Fair | Good | High | High | **Best** |
| 05 The Dispatch | Weak | **Best** | Fair | High | Low | **Best** |

## Shared design language

These decisions hold across all five and should survive whichever layout wins.

### Type

The current app is `Geist` for headings and `Inter` for body
([`tailwind.config.cjs`](../web/tailwind.config.cjs)) with Material Symbols for
icons and no icon dependency. The explorations keep Inter for all UI chrome and
use a serif **only** for document-level headlines — never for labels, never for
numbers. Numbers are always monospace with `font-variant-numeric: tabular-nums`
so columns align as values change.

| Role | Size | Weight | Face |
|---|---|---|---|
| Document headline | 30–40px | 500 | Serif, `-0.015em` tracking |
| Section heading | 18–22px | 500–600 | Serif or Inter 600 |
| Body / prose | 14–15px | 400 | Inter 400 (or serif 400, Ledger/Dispatch only) |
| UI label | 12–13px | 450–500 | Inter |
| Micro-label | 9.5–10px | 500–600 | Mono, `0.19em` tracking, uppercase |
| Numeric | 12–20px | 400–500 | Mono, tabular |

### Color

The existing zinc base plus the two resource accents carries over. Each layout
re-maps it onto its own surface:

- **Resource identity is non-negotiable.** Gold `#f59e0b`, elixir `#d946ef`, dark
  elixir zinc. These three always mean the same thing, in every layout.
- **Star rating is a second, independent scale** (emerald → gold → rose in 04,
  a single gold tone in 01, pips in 02). Pick one per layout and never let it
  collide with the resource colors — the 01 error is picking rose for both.
- **Status colors are reserved**: emerald = healthy/running, amber = degraded or
  a stall warning, rose = error or an unrecovered failure. Nothing else uses them.
- **Muted and accent text must clear WCAG AA (4.5:1) on the surface it sits on.**
  Every one of these mockups was machine-audited and the token values below are
  post-audit, so reuse them rather than picking a tint that merely "looks right"
  against the palette — several of the original picks passed a eyeball test and
  failed at 2.6–4.4:1. Note that a tint's background matters: the same gray that
  clears AA on `#faf8f4` paper can fail on a tinted `#f0ece3` header band. Two
  traps this run hit and fixed: log timestamps are the single easiest element to
  leave dim (9 failures across two layouts), and accent greens/golds are nearly
  always fine as *bar fills* but fail as *text* — give text its own darker token
  instead of reusing the fill color.
- Dark layouts (02, 04) sit at `#0a0a0a`/`#08090a` with surfaces no more than two
  steps lighter. Note the app has previously shipped a "black screen" bug from
  dark-on-dark layering under `WebviewIsTransparent: true`
  ([`web/src/App.tsx`](../web/src/App.tsx)) — any dark layout must keep a
  visible border or surface step between every region.

### Space and shape

- 4px base grid. Dense layouts (04) use 8–12px gaps; editorial layouts (01, 05)
  use 26–40px of margin and let rules do the separating.
- 7–12px radii. The current app's `rounded-2xl` (16px) is generous for a data
  tool at 1200px.
- **1px hairline rules beat shadows** for separation. Only floating elements
  (the 05 run pill, the 04 slide-over) earn a shadow.
- The 1200×850 window is the binding constraint
  ([`main.go:24-28`](../main.go#L24-L28), min 1024×768). A three-pane layout
  gets ~660px in the center; a 5-column table gets tight; a rail + single
  column gets the most room.

### Text budget

This is a control panel for a game bot, not an article. The rule these mockups
are built to: **every visible sentence is under 14 words, and prose never
outweighs a number on screen.** Measured across the five, visible text lands at
211–386 words per page and most of that is labels, timestamps and loot values —
which is the content, not decoration.

Practical consequences that came out of this pass:

- One line of explanation per control, not two. `Off halts at the home screen`,
  not `When off, the bot halts at the home screen and will not start a match.`
- The Dispatch log runs **one short paragraph per event** (16–20 words). It reads
  like a bot's output because it is the length a bot would emit.
- Don't state the same fact twice on one screen — 01's run header, the skip
  divider and the caption each carried the `137 skipped` figure.
- A command list belongs in one place: 02 showed its slash commands both in the
  prompt line and a hint bar underneath.
- Findings go in the headline, the analysis in the numbers. `3★ reached only
  33.3%` is one line; the table beneath it is the argument.

### Motion

180ms for state changes, 220ms for sheets, with `cubic-bezier(.4,0,.2,1)`. No
motion on data — values change, they don't animate. `prefers-reduced-motion` is
honored in the existing app and must be kept.

## What every layout has to keep

1. **Start/Stop is never more than one click away**, and the current run state
   is visible without navigating anywhere.
2. **ADB is a first-class citizen**, not a settings-page line — it's the thing
   that actually breaks.
3. **Loot thresholds sit next to the data they filter**, not on a separate tab.
4. **Every attack row carries strategy + stars + undeployed slot count.** That
   triad is what you actually debug with; layout 05 shows it as prose because
   the prose reads better, not because it's optional.
