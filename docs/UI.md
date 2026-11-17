# ClashGO UI — Command design system

The desktop UI is a quiet command console: near-black surfaces, hairline
borders, one gold accent, dense telemetry, and mono numerals. This document is
the reference for extending it. Architecture lives in [`DESIGN.md`](DESIGN.md).

## Files

| File | Purpose |
| --- | --- |
| `web/src/styles/design.css` | Tokens, base, app frame, shared primitives |
| `web/src/styles/views.css` | Per-view layouts (dashboard, analytics, accounts, config, settings, updater) |
| `web/src/style.css` | Tailwind entrypoint (kept for tooling; views use semantic classes) |
| `web/src/components/ui/Icon.tsx` | Inline SVG icon set (no icon font) |
| `web/src/components/ui/Switch.tsx` | `Switch` control + `SwitchRow` |
| `web/src/components/LaunchSetup.tsx` | First-run army → builders → settings wizard |
| `web/src/components/ui/Drawer.tsx` | Right-side drawer with focus/ESC/scroll handling |
| `web/src/components/ui/motion.ts` | Trigger-anchored transform origin + shared overlay exit timing |
| `web/src/components/ui/StatCard.tsx` | Shared stat tile |
| `web/src/utils.ts` | `compactNumber`, `exactNumber`, `relativeTime`, `formatUptime`, log parsing |
| `web/src/components/ArmyView.tsx` | Live YAML phases, effective recipe slot, and auto-end for each strategy |
| `web/src/armyApi.ts` | Typed `GetArmyPlans` client backed by strategy YAML + `config.json` |

`main.tsx` imports `style.css` → `design.css` → `views.css` in that order, so the
design layer wins over Tailwind preflight.

## Principles

1. **One accent.** Gold marks brand, focus, selection, and primary data. Status
   colors are reserved for state (green/red/blue/amber). It is not a structural
   marker: no colored edge bars, glows, or tinted panel fills. A gold hairline
   on a selected plan or active army card is allowed because it marks state.
2. **Borders over shadows.** Cards use a 1px hairline and a near-invisible
   shadow; elevation is for overlays only.
3. **Numbers are mono.** Every figure, timestamp, port, and counter uses
   JetBrains Mono with `font-variant-numeric: tabular-nums`.
4. **Quiet labels, loud values.** Uppercase 11px labels carry context in data
   views; setup and settings use sentence case. Functional copy never drops
   below 11px, body copy is 12.5–14px, and values carry the weight through size
   and color rather than extra font sizes.
5. **No invented data.** The UI only renders fields the Go side actually sends.
6. **Anchored, quiet motion.** Feedback lands in 90–140ms, state changes in
   180ms, entrances in 240ms, and transient surfaces enter in 320ms but leave
   in 140ms. Animate transform and opacity only; overlays scale from the exact
   trigger rather than sliding across the screen, keep travel small, and never
   bounce or loop decorative elements. Reduced motion is honored.

## Tokens

Defined on `:root` (dark) and `html:not(.dark)` (light).

| Token | Dark | Light | Use |
| --- | --- | --- | --- |
| `--bg` | `#0A0A0B` | `#F6F6F7` | App background, inputs |
| `--surface` | `#111113` | `#FFFFFF` | Cards, sidebar, top bar |
| `--raised` | `#17171A` | `#F1F1F3` | Hover fills, icon tiles |
| `--overlay` | `#202024` | `#E8E8EC` | Active rows, switch off, chips |
| `--border` | `rgba(255,255,255,.075)` | `rgba(0,0,0,.08)` | Hairlines |
| `--border-strong` | `rgba(255,255,255,.145)` | `rgba(0,0,0,.16)` | Hover, drawer edge |
| `--text` | `#EDEDEF` | `#151517` | Primary text |
| `--text-2` | `#A1A1A8` | `#52525B` | Secondary text |
| `--text-3` | `#8A8A93` | `#6B6B74` | Meta labels (AA on surface) |
| `--text-faint` | `#7F7F89` | `#6C6C76` | Placeholders and tertiary meta (AA on bg) |
| `--accent` | `#E3B341` | `#B88413` | Brand fill, focus ring |
| `--accent-ink` | `#E3B341` | `#7A5700` | Accent text on surface |
| `--accent-soft` | `rgba(227,179,65,.12)` | `rgba(184,132,19,.12)` | Accent tint |
| `--on-accent` | `#14120B` | `#1B1404` | Ink on accent fill (light gold needs dark ink) |
| `--gold` / `--elixir` / `--de` | `#E3B341` / `#C86BDD` / `#8F7FE8` | `#8F6710` / `#9C3FB4` / `#6351C9` | Resource series |
| `--green` / `--red` / `--blue` | `#4CC38A` / `#E5484D` / `#5CB3F0` | `#187A4B` / `#C62F35` / `#1E6EA8` | Status |
| `--scrollbar` / `--scrollbar-hover` | `#2A2A30` / `#45454E` | `#C7C7CE` / `#A8A8B2` | Browser scrollbars |
| `--r` / `--r-sm` / `--r-xs` | `14px` / `9px` / `6px` | same | Card-like surfaces / controls / chips |
| `--sidebar-w` / `--rail-w` / `--topbar-h` | `232px` / `84px` / `56px` | same | Frame |
| `--font` / `--mono` | Inter / JetBrains Mono | same | Typography |

Type scale: body 14/1.55; page title 24/600; card title 14/600; micro-label
11/600 uppercase `.04–.07em`; display figure 24–29/600; stat figure 19/600.

Spacing is 4px based (`--space-1`…`--space-8`) with 16/24/32px for layout
rhythm; page padding 32px; content max 1500px including padding. Use weight and
color for hierarchy before adding another size.

Every card-like surface, including tiles and panels nested inside a card, uses
`--r`. `--r-sm` and `--r-xs` are for controls, badges, inputs and rows, so a
control never inherits a card corner by accident.

Views share one content shell. Do not add page-specific `max-width` wrappers
around a view: cards that switch between tabs must land on the same left and
right edge. Nested tiles align to their parent card's content box, and a
grid's outer cards align to the group's content box.

### Motion

The timings follow the consensus in [Apple's HIG](https://developer.apple.com/design/human-interface-guidelines/motion),
[Vercel's Web Interface Guidelines](https://vercel.com/design/guidelines),
[Atlassian's motion system](https://atlassian.design/foundations/motion), and
[Nielsen Norman Group's duration research](https://www.nngroup.com/articles/animation-duration/):
hover and press feedback under 150ms, small state changes around 180ms,
transient overlays around 320ms, and deceleration rather than spring or bounce.

| Token | Value | Use |
| --- | --- | --- |
| `--motion-press` | `90ms` | Button and control press scale |
| `--motion-fast` | `140ms` | Hover fills, borders, color, focus |
| `--motion-base` | `180ms` | Toggles, tabs, status, small state |
| `--motion-enter` | `240ms` | View and card entrances |
| `--motion-exit` | `140ms` | Transient surface and scrim exits |
| `--motion-overlay` | `320ms` | Drawer, modal, setup panel entry |
| `--ease-out` | `cubic-bezier(.16,1,.3,1)` | Entrances and hover feedback |
| `--ease-in-out` | `cubic-bezier(.65,0,.35,1)` | Reversible state |

Overlay entry is opacity plus scale (`.94` drawer/modal, `.97` setup panel)
from a `transform-origin` set to the trigger's center by
`components/ui/motion.ts`; exit uses the same geometry, faster (140ms) and
slightly smaller. Standard content entrances retain their small travel; press
feedback is `.98` scale. Only the appended log row enters, so existing rows
displace instead of every row re-fading. Nothing moves while the user is
typing, and status pulses are the only infinite animation besides loading
spinners. Data viz does not get entrance animation:
the Wails webview can freeze CSS animations while its window is not painting,
which once left the sparkline drawn only part-way across its card.

## Frame

- Fixed sidebar: 38px draggable strip reserved for macOS traffic lights, brand,
  grouped nav, and one Start/Stop control. Starts as an 84px rail
  below 1150px on first run; the top-bar toggle or Cmd/Ctrl+B collapses and
  expands it at any width. The preference persists. Below 900px the rail is
  forced regardless of the stored preference; below 760px it narrows to 68px
  and the top-bar toggle and breadcrumb trail are hidden.
- Sticky 56px top bar: sidebar toggle, breadcrumb, a session-status pill
  (Idle/Farming + active account), updater banner when an update exists, and
  the live clock. ADB health lives on Dashboard/Settings; the theme switch
  lives in Settings. Interactive children need `no-drag`. The active-account
  label and clock hide below 900px; the whole status cluster hides below
  760px. The bar is a solid `--bg` surface; overlays keep blur, the persistent
  frame does not.
- `.content` is the only scroll container. Top bar stays outside it. A main-tab
  change scrolls it to the top, and a Config sub-tab change does the same (the
  subnav is sticky on desktop and static on narrow windows).

Responsive frame:

- `<=1150px`: Settings drops to one column and the first-run sidebar starts as
  a rail.
- `<=1024px`: Analytics KPIs and fleet stats become two columns; the Dashboard
  resource strip stays three-up at the Wails minimum so its cards never leave
  an orphan.
- `<=900px`: sidebar is always the icon rail, the top-bar toggle is hidden,
  the active-account label and clock drop out, and the Dashboard resource strip
  stacks so labels and six-to-nine digit values never wrap or truncate.
- `<=760px`: 68px rail, one-column page grids, stacked page heads and setting
  rows, 16px content padding, and heatmap month labels hidden (52 weekly
  columns cannot fit them; the card subtitle still names the 12-month range).
- `<=520px`: Config subnav becomes a two-column tab grid.

Wails rules: any new draggable region gets `draggable`; buttons inside it get
`no-drag`. Do not wrap the shell in a transform — it breaks fixed overlays.
The Wails window is `MinWidth 1024 / MinHeight 768`; narrower layouts are for
browser preview and do not imply a 390px desktop window. The macOS native
window chrome is configured `NSAppearanceNameDarkAqua`; only the content
palette follows `html.dark`.

## Launch setup

`LaunchSetup.tsx` is the first-run wizard: army → builders → settings. It is a
two-pane overlay, up to 1000×670, with a 228px orientation rail and one decision
per step. Start lives on the last step and writes army, builders, and search floors
together.

- The active rail step is `--overlay` with neutral text; no accent bar, glow,
  or gradient. Completed steps use a small check.
- Sections are separated by rules and whitespace, not nested cards. Only plan
  choices and inputs get a fill.
- Gold is limited to the primary CTA, the selected plan's radio and hairline,
  the selected recipe slot, switches, the recommended label, and status.
  Everything else is neutral.
- Builders covers the full progression loop: plan, types, wall reserve, lab
  research order, and pet order. Lab and pets follow the engine switch.
- Plan choices are a two-up comparison on wide windows and one column when
  the panel is narrow. The rate is the loudest value, the recommendation is
  plain accent text, and the selected plan takes the gold radio.
- Below 880px the left rail becomes a horizontal step strip. The rail footer
  is hidden there, so the content header carries a `Set up manually` exit;
  never trap a narrow user inside the wizard. Step names stay visible down to
  350px; below that the strip becomes three dots with a gold active dot, and
  plan names wrap instead of truncating. Builder lab/pet columns stack below
  680px.
- Adaptive loot owns the floors: while it is on, the three floor inputs are
  disabled and dimmed. Turning it off restores manual editing.
- Dry run is a Config → Builders control, not part of setup.

## Control ownership

One setting has one home. Duplicated controls drift out of sync and read as
optional when they are not.

- **Army** owns per-plan choices: which strategy is active, its recipe slot,
  and its auto-end switch. Config carries no plan controls. Each plan card
  shows the same loot/hour estimate as the launch wizard (`loot_per_hour`,
  `measured`, `raids`), so a roster comparison cannot disagree with setup.
- **Config** owns engine-wide settings across five tabs: Search & loot,
  Builders, Lab & pets, Schedule, Storage. Wall upgrading and the wall
  resource live with Builders, next to the reserved-builder count; Storage
  owns halt/hysteresis and auto-collect only.
- **Settings** owns machine-local UI preferences, diagnostics, updates, and
  the device test tools.
- **Master switches gate the engine, not the form.** Schedule, Builders, Lab
  and Pets stay editable while their switch is off; Save persists every value
  and the switch alone decides whether the engine acts on it. Wrap dependent
  blocks in `.off-block` at 62% dark / 72% light opacity and add a one-line
  `.off-note` that says the values still save; focus inside restores opacity
  to 90/95. Inline dependent panels such as Auto loot tuning use `.is-off`.
  Use `MasterToggle` in card heads so the ON/OFF state stays visible
  and stays on the title row at narrow widths. Never hide a form behind
  `pointer-events: none`. If a block genuinely cannot be edited,
  use native `disabled` controls plus a visible reason. Allowed cases: the
  setup wizard's upgrade block and adaptive-loot floors, Settings device
  tests while the bot runs, and replaying the wizard mid-run.
- **Full inventory:** `docs/UI_CONTROL_INVENTORY.md` records every actionable
  control, its location/action, and the deliberate shortcut decisions
  (sidebar Start/Stop, update checks, wizard preset).
- **Analytics names its window on every surface.** Session metrics reset at
  launch (KPI strip, loot chart, outcomes, revenue, strategy table); attack
  activity reads saved raid history. Never leave a number or chart without
  its window in the subtitle or label.
- Start/Stop appears in the sidebar and, contextually, on the active account
  card. The sidebar control is the single global primary; page-level Add
  account and per-account start actions are secondary. Ticket-style row
  actions stay inside their own view.

## Component recipes

```tsx
<article className="card">
  <div className="card-head">
    <div><h2>Title</h2><p className="card-sub">Context</p></div>
    <div className="card-tools">…</div>
  </div>
  <div className="card-body">…</div>
</article>
```

- Buttons: `.btn` default, `.btn.primary`, `.btn.danger`, `.btn.ghost`, `.btn.sm`.
  `.icon-btn` for 32px icon actions (`.plain` removes the border, `.sm` shrinks).
- Forms: `.label` + `.field` (`.select` adds the chevron, `.mono` for figures),
  `.hint`, `.hint.error`. Invalid inputs use `aria-invalid="true"`.
- Rows: `.set-row` (label/description left, `.set-ctl` right) for config and
  settings; `.kv` for read-only key/value pairs.
- Off states: `.off-block` dims a dependent form without disabling it;
  `.off-note` carries the reason and the save promise. Use both together.
- Switches: `<Switch checked onChange ariaLabel />` in a control slot;
  `<SwitchRow checked onChange label description />` when the label is the row;
  `<MasterToggle>` for a card's gating switch (adds visible ON/OFF text).
- Segments: `.seg` + `.seg-btn.active`. Config subnav buttons share the row
  width, then switch to a two-column grid at 520px; do not leave a long empty
  track beside content-sized tabs.
- Tables: `.table-wrap` > `.data-table`; stars via `.stars` + `Icon name="star"`.
- Status: `.status-pill` with `running|warn|error|info|green`; `.tag` for
  compact metadata.
- Headings: page title is `<h1>`; every card title in that page is `<h2>`.
  Do not skip from `<h1>` to `<h3>`; the setup wizard's step `<h1>` and section
  `<h2>` follow the same rule.
- Empty states: `.empty` + `.empty-ico` + `<b>` + `<p>`.
- Drawers: use `components/ui/Drawer.tsx`; never rebuild scrim/ESC/focus logic.
  Pass the opening control as `anchor` so the panel grows from it, not from the
  screen edge. Long drawer forms keep their `.form-actions` sticky to the
  drawer foot so the commit action is reachable without scrolling to the end.
- Browser surfaces: scrollbars, caret, selection, and focus ring are themed
  from the palette. Keep numerals tabular; never hard-code a native control
  color.
- Overlays: portal modals and splashes to `document.body`. A `position: fixed`
  overlay rendered inside the top bar is clipped because the bar's backdrop
  filter creates a containing block.
- Charts: inline SVG or CSS only. No chart dependency. Decorative graphics get
  `aria-hidden`; expose the same data as text nearby.

Icons: add the name to `IconName` and a 24×24 path to `paths` in
`ui/Icon.tsx`. Stroke icons inherit `currentColor`; do not hard-code colors.

## Accessibility checklist

- Keyboard: focus ring is the gold `:focus-visible` outline; drawers trap Tab
  and restore focus; ESC closes overlays; clickable table rows also expose a
  real button.
- Switches use `role="switch"` + `aria-checked`; toggles never rely on color
  alone (thumb position + text state).
- Text contrast: use `--text-2`/`--text-3` for readable copy; `--text-faint` is
  decorative. Do not put essential labels in `--text-faint`.
- Charts and sparklines are `aria-hidden`; the numbers they visualize are
  rendered as text.
- Respect `prefers-reduced-motion`; never add autoplaying motion that blocks use.
  Drawers, modals and the setup panel drop keyframes and transforms outright;
  their closing state still clears so dismissal is immediate.
- Icon-only buttons require `aria-label` and usually `title`.

## Mockup reference

`docs/mockups/command-reference.html` captures the supplied Command reference:
bar-based analytics, stacked loot, denser settings, and motion cues. The live
product is the React implementation under `web/src`; the reference is not wired
to Wails or runtime data.

## Adding a view

1. Add the tab id to `TabType` and `TAB_LABELS` in `App.tsx`, plus a nav item in
   `Sidebar.tsx` with an existing icon.
2. Render it inside `.content`; start with `.page-head` (h1 + one-line purpose).
3. Compose from `.card`, `.stat-strip`, `.set-row`, and the table primitives.
4. Put layout-only rules in `views.css`, shared primitives in `design.css`.
5. Verify both themes at 1440×900, 1024×768, and 390×844; check keyboard
   flow and horizontal overflow. The 390px pass is browser-preview only.

## Theme and preferences

Dark is the default (`index.html` bootstrap + `getInitialDarkMode` in `App.tsx`).
The Settings toggle writes `localStorage.darkMode`; the `html.dark` class drives
all tokens. Never hard-code a hex in a component — add or use a token.

Appearance and behavior preferences live in `localStorage`: `compactMode`
(`html.compact` density), `reduceMotion` (`html.reduce-motion`), `clock24h`,
`defaultTab`, and `confirmStop`. Keep them client-side; they are not bot config.
Settings layouts use explicit grid areas, not CSS columns, so cards stay packed
and no column is left with a tall blank gap.

## Do / don't

- Do use `exactNumber` for auditable loot and `compactNumber` for summaries.
- Do show a real empty state instead of zeros dressed up as data.
- Don't add Material Symbols, Geist, or another icon font.
- Don't put Tailwind utility soup in new markup; use semantic classes.
- Don't invent metrics (base names, win rate, session totals) the Go side does
  not send.
- Don't introduce a chart, router, or component library for this UI.
- Don't mark active navigation with a colored edge, glow, or accent fill.
- Don't turn a plain two-option choice into an equal card grid with badges and
  gradients; use a radio list. The launch plan picker is the exception: its
  two choices carry a comparable loot rate, so they share one card grid.
- Don't add decorative loops, parallax, spring/bounce easing, or entrance
  animation on data viz; use the motion tokens above and keep it stateful.
