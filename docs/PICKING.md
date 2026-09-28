# Picking deploy points by hand (`cmd/pick_coords`)

Automatic deployment geometry kept failing in ways the bot could not see. It
tried to detect a "red line" and sometimes read the base as the boundary; it
clamped points into a band that was still inside the base; it pushed points
radially out of a zone it had guessed wrong; and it substituted the strategy
formula over any point you had pinned, so pinning appeared to do nothing. Two
live battles on 2026-09-27 showed the cost: every hero drop was refused for the
whole hero phase — the game said so on screen, *"You cannot deploy troops on the
Red area!"* — and the same points were accepted twelve seconds later only
because the base had been destroyed back off them. Zero abilities fired in
either battle.

`pick_coords` replaces the guesswork with a decision only a human can make well:
**click the ground you want, per side, on the live frame.** The bot then uses
exactly those points.

```
make pick-coords              # serves a picker on http://127.0.0.1:8791/
make pick-coords ARGS="-open" # same, and opens your browser
```

Open the URL (or the Freebuff Preview tab), and click the frame.

## Quick start — the 30-second version

You do **not** have to place anything by hand to get a working attack.

1. `make pick-coords`
2. Open <http://127.0.0.1:8791/> (the terminal prints it).
3. Press **Fill standard ring**. This writes a safe plan for all four sides:
   every point sits 110 px inside the screen edge, in the ring that is
   deployable against any base, with the two spell lines stepped further in.
4. Press **Save to config**. The four side tabs now show a green dot. Done —
   the bot's next battle taps those points, on whichever side it attacks.
5. Refine later, one side at a time, if you want a different path: click a side
   tab, press `1`–`5`, click the frame where you want that thing. Drag the
   handles to adjust. Save again.

That is the whole loop. Everything below is detail for when you want it.

### You do not need to be in a battle

The ring is *screen-relative* — it is about how close to the border you are, not
about this base — so any CoC frame works as a background. A battle frame is
nicer for judging the base, and the live view keeps refreshing until you arm a
tool, so you can watch a battle and freeze it with **Refresh frame**.

## What you place, for each of the four sides

| Tool | Key | What it controls |
|------|-----|------------------|
| Troop line | `1` | p1 → p2. Every troop tap (Balloons, EDrags, …) lands on this line |
| Spell line A | `2` | p1 → p2 for the first spell line (rage) |
| Spell line B | `3` | p1 → p2 for the second spell (ice) |
| Hero point | `4` | Where heroes drop; the bot snaps it onto the troop line |
| Spell point | `5` | Single-point spell target |

Pick a side tab (TopLeft / TopRight / BottomLeft / BottomRight), press a number
to arm a tool, click the frame — twice for a line, once for a point. Drag any
handle to fine-tune. `Esc` cancels, `⌘/Ctrl+S` saves. **Clear this side**
empties the side you are on; **Reload saved** throws away unsaved edits and
re-reads the config.

The overlay draws the line plus **the 15 taps the bot will actually fire** along
it, so what you see is the geometry that runs.

## The Checks box — "does it look good?" without a battle

The tool answers that itself. Above the side tabs is a one-line verdict, each tab
carries a red badge with the number of points on that side that need a look, and
the **Checks** panel lists them in plain words. Flagged points get a red dashed
ring on the frame, so you can just drag them.

What it flags, and why:

| Flag | Meaning |
|---|---|
| *in the top HUD* | The point is under the loot counters / battle clock. A tap there is eaten by the UI, so that unit never deploys. Move it down to **y ≥ 130**. |
| *on the troop bar* | The point is under the card bar at the bottom. Same result, opposite direction. |
| *inland* | Only for things you **deploy** (troop line, hero point): the point is far from every border, which is where the base's no-deploy pocket lives — the ground the game refuses with *"You cannot deploy troops on the Red area!"*. Spells are **not** flagged for this: a spell line is supposed to reach into the base. |
| *only Npx long* | A line too short to spread an army is usually a misclick. |

These are warnings, not rules — the tool saves whatever you insist on. But the
bot has a backstop for the first two: pinned points under the chrome are clamped
back onto the field at load (it logs `pinned points sat under the game's HUD`),
because a tap on UI can never be what you meant. Anything the Checks box calls
safe is left exactly where you put it.

## Rules of thumb

- **Stay in the outer ring.** The base never reaches the screen edge, so ground
  within roughly 130 px of a border is deployable against every base. The shaded
  band on the frame shows that ring; the dashed inner rectangle is where the base
  usually lives.
- **A refused tap tells you the answer.** If the game shows *"You cannot deploy
  troops on the Red area!"*, that point sits inside the base's no-deploy pocket.
  Move it outward and save again.
- **Lines, not single points, for troops.** A line spreads the army; a point
  stacks it on one tile and a refused tile loses the whole deployment.
- **Hero points belong in the outer ring too** — closer to the border than a
  spell target. A hero dropped inland is the one placement this project has
  repeatedly watched the game refuse; keeping the point near the edge is what
  lets the hero land on its **first** tap, which leaves the second card tap free
  for its ability.

## What the bot does when the game refuses one of your points

A refused deploy is a *region*, not a stray pixel: the game rejects the whole
no-deploy pocket around the spot. So when a point of yours is refused, the bot
steps **outward along your own line** — toward the screen border — and tries
again on your line rather than somewhere of its own choosing. Your point is
always tried first; the outward step is what stops one refusal from losing the
unit (and, for a hero, its ability).

Heroes get one placement and one ability. A refused placement also *deselects*
the hero's card in CoC, so the bot re-arms it exactly once before retrying
(`maxHeroRearms`); a re-arm is not a second placement, so the ability's tap is
never spent on it.

## Why saving is not just writing your click

`precision_config.json` declares a reference geometry (`width: 860`,
`height: 732`) and the bot scales every pin per axis into the live frame
(`x_live = x_ref · 1280/860`, `y_live = y_ref · 720/732`). Hand-editing the file
means doing that arithmetic yourself, which is where "the pin looks right but the
taps go somewhere else" came from. The tool does the conversion in both
directions: you click live pixels, it stores reference pixels, and it shows the
saved plan back on the live frame.

Saving merges per side — pinning TopLeft does not disturb the other three.

## Pins are authoritative

For a side you have pinned, the bot:

- does **not** substitute the strategy formula's troop ground,
- does **not** apply the strategy `offset` (which pushed a pinned line up to 40%
  of the way toward the middle of the base — into the no-deploy area),
- uses the same pinned ground for troops, heroes, spells and the final sweep.

You will see it in the log:

```
using the USER-PINNED deploy line for every troop, hero and sweep tap (strategy formula not substituted for this side)
deploying troop ... src=pin p1={150,200} p2={150,560}
```

Sides with no pins keep the old formula/red-zone behaviour, so you can convert
one side at a time.

## Suggested workflow

1. Pin the side you are about to be attacked from. With
   `target_edge: "Random"` the side changes every battle, so either catch one
   battle per side, or temporarily set `target_edge` to the side you are pinning
   (see `assets/strategies/auto_edrag_rush.yaml`) to make it deterministic.
2. Start a battle, then run `make pick-coords` and click **Refresh frame**
   during the deploy phase. The frame you click only needs the battle view —
   placing points after the army has left the bar is fine, since the ground does
   not move.
3. Save, and watch the next battle's log for `src=pin` on the troop deploy and
   for `hero ability activated`, which is the signal that a hero was confirmed
   placed in its phase (and therefore got its second card tap).

## Troubleshooting

| Symptom | Cause |
|---------|-------|
| `capture failed … is the emulator up and CoC on screen?` | ADB device down, or the wrong `-device` |
| A side shows `—` in the panel | Nothing pinned for that side yet |
| Points look right but the game refuses the tap | The point is inside the base's pocket — move it outward into the ring |
| The bot ignored a pin | The pin's side string must match the attacked corner exactly (`TopLeft`, `TopRight`, `BottomLeft`, `BottomRight`) |

The tool never writes frames to disk and captures only when the page asks for a
frame, so it cannot turn into the runaway capture loop that once filled this
machine's disk.
