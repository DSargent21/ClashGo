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
| `web/src/components/ui/StatCard.tsx` | Shared stat tile |
| `web/src/utils.ts` | `compactNumber`, `exactNumber`, `relativeTime`, `formatUptime`, log parsing |
| `web/src/components/ArmyView.tsx` | Live YAML phases, effective recipe slot, and auto-end for each strategy |
| `web/src/armyApi.ts` | Typed `GetArmyPlans` client backed by strategy YAML + `config.json` |

`main.tsx` imports `style.css` → `design.css` → `views.css` in that order, so the
design layer wins over Tailwind preflight.

## Principles

1. **One accent.** Gold marks brand, focus, selection, and primary data. Status
   colors are reserved for state (green/red/blue/amber). It is not a structural
   marker: no colored edge bars, glows, or tinted panel borders.
2. **Borders over shadows.** Cards use a 1px hairline and a near-invisible
   shadow; elevation is for overlays only.
3. **Numbers are mono.** Every figure, timestamp, port, and counter uses
   JetBrains Mono with `font-variant-numeric: tabular-nums`.
4. **Quiet labels, loud values.** Uppercase 10.5px labels carry context in data
   views; setup and settings use sentence case. Values carry the weight.
5. **No invented data.** The UI only renders fields the Go side actually sends.
6. **Fast, quiet motion.** Feedback lands in 90–140ms, state changes in 180ms,
   entrances in 240ms, overlays in 280ms. Animate transform and opacity,
   decelerate with ease-out, keep travel at 4–6px, and never bounce or loop
   decorative elements. Reduced motion is honored.

## Tokens

Defined on `:root` (dark) and `html:not(.dark)` (light).

| Token | Dark | Light | Use |
| --- | --- | --- | --- |
| `--bg` | `#0B0B0C` | `#F6F6F7` | App background, inputs |
| `--surface` | `#121214` | `#FFFFFF` | Cards, sidebar, top bar |
| `--raised` | `#1A1A1D` | `#F1F1F3` | Hover fills, icon tiles |
| `--overlay` | `#222226` | `#E8E8EC` | Active rows, switch off, chips |
| `--border` | `rgba(255,255,255,.07)` | `rgba(0,0,0,.08)` | Hairlines |
| `--border-strong` | `rgba(255,255,255,.14)` | `rgba(0,0,0,.16)` | Hover, drawer edge |
| `--text` | `#EDEDEF` | `#151517` | Primary text |
| `--text-2` | `#A1A1A8` | `#52525B` | Secondary text |
| `--text-3` | `#8A8A93` | `#6B6B74` | Meta labels (AA on surface) |
| `--text-faint` | `#63636B` | `#9A9AA3` | Decorative only |
| `--accent` | `#E3B341` | `#B88413` | Brand fill, focus ring |
| `--accent-ink` | `#E3B341` | `#7A5700` | Accent text on surface |
| `--accent-soft` | `rgba(227,179,65,.12)` | `rgba(184,132,19,.12)` | Accent tint |
| `--on-accent` | `#14120B` | `#FFFFFF` | Text on accent fill |
| `--gold` / `--elixir` / `--de` | `#E3B341` / `#C86BDD` / `#8F7FE8` | `#A9791C` / `#9C3FB4` / `#6351C9` | Resource series |
| `--green` / `--red` / `--blue` | `#4CC38A` / `#E5484D` / `#5CB3F0` | `#1E8E5A` / `#C62F35` / `#2277B8` | Status |
| `--r` / `--r-sm` / `--r-xs` | `12px` / `8px` / `6px` | same | Cards / controls / chips |
| `--sidebar-w` / `--rail-w` / `--topbar-h` | `232px` / `84px` / `56px` | same | Frame |
| `--font` / `--mono` | Inter / JetBrains Mono | same | Typography |

Type scale: body 14/1.55; page title 22/600; card title 14/600; micro-label
10.5/600 uppercase `.07em`; display figure 27/600; stat figure 19/600.

Spacing is 4px based with 8/12/16/24 gaps; page padding 28px; content max
1500px including padding.

### Motion

The timings follow the consensus in [Apple's HIG](https://developer.apple.com/design/human-interface-guidelines/motion),
[Vercel's Web Interface Guidelines](https://vercel.com/design/guidelines),
[Atlassian's motion system](https://atlassian.design/foundations/motion), and
[Nielsen Norman Group's duration research](https://www.nngroup.com/articles/animation-duration/):
hover and press feedback under 150ms, small state changes around 180ms,
overlays under 300ms, and deceleration rather than spring or bounce.

| Token | Value | Use |
| --- | --- | --- |
| `--motion-press` | `90ms` | Button and control press scale |
| `--motion-fast` | `140ms` | Hover fills, borders, color, focus |
| `--motion-base` | `180ms` | Toggles, tabs, status, small state |
| `--motion-enter` | `240ms` | View and card entrances |
| `--motion-exit` | `160ms` | Drawer and scrim exits |
| `--motion-overlay` | `280ms` | Drawers, modals, setup panel |
| `--ease-out` | `cubic-bezier(.16,1,.3,1)` | Entrances and hover feedback |
| `--ease-in-out` | `cubic-bezier(.65,0,.35,1)` | Exits and reversible state |

Entrances are opacity plus 4–6px of travel; press feedback is `.98` scale.
Nothing moves while the user is typing, and status pulses are the only infinite
animation besides loading spinners. Data viz does not get entrance animation:
the Wails webview can freeze CSS animations while its window is not painting,
which once left the sparkline drawn only part-way across its card.

## Frame

- Fixed sidebar: 38px draggable strip reserved for macOS traffic lights, brand,
  grouped nav, and one Start/Stop control. Starts as an 84px rail
  below 1150px on first run; the top-bar toggle or Cmd/Ctrl+B collapses and
  expands it at any width. The preference persists.
- Sticky 56px top bar: breadcrumb, ADB status pill, updater chip, theme toggle,
  live clock. Interactive children need `no-drag`.
- `.content` is the only scroll container. Top bar stays outside it.

Wails rules: any new draggable region gets `draggable`; buttons inside it get
`no-drag`. Do not wrap the shell in a transform — it breaks fixed overlays.

## Launch setup

`LaunchSetup.tsx` is the first-run wizard: army → builders → settings. It is a
two-pane overlay, up to 1000×670, with a 228px orientation rail and one decision
per step. Start lives on the last step and writes army, builders, and search floors
together.

- The active rail step is `--overlay` with neutral text; no accent bar, glow,
  or gradient. Completed steps use a small check.
- Sections are separated by rules and whitespace, not nested cards. Only plan
  choices and inputs get a fill.
- Gold is limited to the primary CTA, selected radio dot, switches, the
  recommended label, and status. Everything else is neutral.
- Builders covers the full progression loop: plan, types, wall reserve, lab
  research order, and pet order. Lab and pets follow the engine switch.
- Plan choices are stacked radio rows, not equal marketing cards. The rate is
  the loudest value; the recommendation is plain accent text.
- Adaptive loot owns the floors: while it is on, the three floor inputs are
  disabled and dimmed. Turning it off restores manual editing.
- Dry run is a Config → Upgrades control, not part of setup.

## Component recipes

```tsx
<article className="card">
  <div className="card-head">
    <div><h3>Title</h3><p className="card-sub">Context</p></div>
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
- Switches: `<Switch checked onChange ariaLabel />` in a control slot;
  `<SwitchRow checked onChange label description />` when the label is the row.
- Segments: `.seg` + `.seg-btn.active`.
- Tables: `.table-wrap` > `.data-table`; stars via `.stars` + `Icon name="star"`.
- Status: `.status-pill` with `running|warn|error|info|green`; `.tag` for
  compact metadata.
- Empty states: `.empty` + `.empty-ico` + `<b>` + `<p>`.
- Drawers: use `components/ui/Drawer.tsx`; never rebuild scrim/ESC/focus logic.
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
5. Verify both themes at 1440×900 and 1024 width, and check keyboard flow.

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
- Don't turn a two-option choice into an equal card grid with badges and
  gradients; use a radio list.
- Don't add decorative loops, parallax, spring/bounce easing, or entrance
  animation on data viz; use the motion tokens above and keep it stateful.
