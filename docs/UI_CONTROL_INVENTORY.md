# UI control inventory

Audit date: 2026-11-16
Scope: Dashboard, Army, Analytics, Accounts, Config (all tabs), Settings,
account drawer, update modal, setup wizard, plus shared shell.
Base commit: `8750874`; this audit adds the naming/focus fixes noted below.

Method: each surface was driven in the Wails browser stub, every rendered
button/link/tab/switch/radio/select/input/textarea was extracted with its
visible label and owning card, and the handler was traced in the component.
Repeated row actions (log lines, pet rows, account cards) are recorded once as
a pattern. Conditional controls are marked.

## Shared shell

| Control | Location | Action |
|---|---|---|
| Dashboard / Army / Analytics / Accounts / Config / Settings | Sidebar, PRIMARY nav | Switch the active view. |
| Collapse sidebar / Expand sidebar | Topbar, left | Toggle the sidebar between expanded and icon rail. Same action as the ⌘/Ctrl+B shortcut; the button names its state with `aria-expanded`. |
| Start bot / Stop bot | Sidebar footer | Start or stop the global engine. Label and action flip with state. |
| Update chip (`Update x.y.z`, etc.) | Topbar, right; only when an update surface is active | Open the update modal. |

## Dashboard

| Control | Location | Action |
|---|---|---|
| Attack history row | History table | Pointer shortcut that opens the raid report drawer. No longer a separate tab stop; the chevron owns keyboard/AT access. |
| Open report for `<strategy>` | History table, last column, per row | Opens the raid report drawer for that row. |
| Close | Raid report drawer footer | Close the report drawer. |
| Close panel | Raid report drawer header | Close the report drawer. |
| Auto | System console header | Toggle auto-scroll of live logs. |
| Export logs | System console header | Copy the full filtered console, ANSI-free. |
| Filter logs by text | System console toolbar | Filter visible log lines. |
| Clear log filter | Appears only when the filter is non-empty | Clear the text filter. |
| All / Debug / Info / Success / Warn / Error | System console toolbar | Set the severity filter. |
| Copy log line | Per rendered log row | Copy that line to the clipboard. |

## Army

| Control | Location | Action |
|---|---|---|
| Slot 1 / 2 / 3 / 4 | Active plan card, Saved recipe slot | Set the recipe slot armed for the active strategy. |
| Auto-end battle | Active plan card, switch | Persist the active strategy's auto-end choice. |
| Use this army | Each non-active plan card | Save that strategy as the active plan. |

The active plan also has read-only facts and phase chips; they are not controls.

## Analytics

| Control | Location | Action |
|---|---|---|
| Gold / Elixir / Dark ×40 | Loot per raid card, legend | Toggle that series in the stacked chart. All-off is allowed and reversible. |

Outcome, revenue, activity heatmap and strategy table are read-only.

## Accounts

| Control | Location | Action |
|---|---|---|
| Refresh accounts | Page head | Reload the account list from the engine. |
| Retry | Error banner, conditional on a load/action failure | Reload the account list; same handler as Refresh accounts. |
| Add account | Page head | Open the account drawer in create mode. |
| Add account | Empty state, only when no accounts exist | Open the account drawer in create mode; contextual duplicate of the page-head button. |
| Enable automatic account rotation | Fleet rotation card | Turn fleet rotation on/off. |
| Order | Fleet rotation card | Choose round-robin or priority order. |
| Attacks / account | Fleet rotation card | Set per-account attacks per rotation session. |
| Auto-start the bot after switching accounts | Fleet rotation card | Choose whether rotation starts the bot after a switch. |
| Start bot / Stop bot | Active account card | Start or stop the bot for the active account. |
| Switch & start | Non-active account card | Switch to that account and start the bot. |
| Switch | Non-active account card | Switch to that account without starting. |
| Edit `<name>` | Account card | Open the drawer in edit mode. |
| Reset today's usage for `<name>` | Account card | Reset today's attack/switch counters. Disabled at zero with a title reason. |
| Delete `<name>` | Account card, two-click confirm | Delete the account. Disabled for the active or only account. |
| Close panel | Drawer header | Close without saving. |
| Village name | Drawer | Edit account name. |
| Player tag | Drawer | Edit player tag. |
| Town hall | Drawer | Edit town hall level. |
| Login hint (never a password) | Drawer | Edit non-secret login hint. |
| Rotation priority | Drawer | Edit rotation priority. |
| Cooldown (min) | Drawer | Edit account cooldown. |
| ADB host | Drawer, Emulator binding | Edit ADB host. |
| ADB server port | Drawer, Emulator binding | Edit ADB server port. |
| Device serial | Drawer, Emulator binding | Edit the device serial/id. |
| Daily attack cap (0 = ∞) | Drawer | Edit daily cap. |
| Session cap (0 = default) | Drawer | Edit session cap. |
| Notes | Drawer | Edit free-form account notes. |
| Include in rotation | Drawer | Choose whether this account participates in rotation. |
| Cancel | Drawer footer | Close without saving. |
| Add account / Save changes | Drawer footer | Validate and submit the account. |

## Config

| Control | Location | Action |
|---|---|---|
| Search & loot / Builders / Lab & pets / Schedule / Storage | Config subnav tabs | Switch configuration section. |
| Save settings | Config subnav, sticky | Persist all Config values, including sections whose feature switch is off. |

### Search & loot

| Control | Location | Action |
|---|---|---|
| Min gold / Min elixir / Min dark elixir | Threshold grid | Edit matchmaking floors. |
| Auto loot tuning | Card switch | Enable adaptive floors learned from search history. |
| Apply suggestion | Learning panel | Apply the learned floors. Disabled while learning/busy, with visible progress. |
| Reset history | Learning panel | Delete learned samples and start over. |
| Stall timer seconds | Card row | Set the stalled-battle abort (0 disables). |
| Enable search | Card switch | Gate loot-floor enforcement. |

### Builders

| Control | Location | Action |
|---|---|---|
| Enable automatic builders | Section header | Gate builder automation. |
| Upgrade priority | Form | Choose strategic rush/max/heroes/cheapest/etc. |
| Reserve builders for walls | Form | Keep 0–5 builders free for walls. |
| Heroes / Defenses / Resources / Army buildings / Traps | Allowed categories | Include/exclude upgrade categories. |
| Gold / Elixir / Dark elixir | Max cost per upgrade | Per-resource upgrade ceilings; 0 = unlimited. |
| Dry run / simulation | Card row | Inspect upgrade dialogs without spending. |
| Auto upgrade walls | Walls block | Gate wall upgrades, including pre-halt dumping. |
| Wall resource | Walls block | Choose gold, elixir, or both for walls. |

Walls appear only here; Storage has no wall control.

### Lab & pets

| Control | Location | Action |
|---|---|---|
| Enable automatic lab research | Lab section header | Gate lab automation. |
| Research priority | Lab form | Choose lab order. |
| Allow dark elixir research | Lab form | Allow DE lab spending. |
| Enable automatic pet upgrades | Pets section header | Gate pet automation. |
| Move `<pet>` up / down | Each pet row | Reorder the pet queue. Bounds are disabled. |
| Remove `<pet>` | Each pet row | Remove the pet from the queue. |
| `<pet>` chips | Add pet to queue | Add that pet to the queue. |

### Schedule

| Control | Location | Action |
|---|---|---|
| Enable 24/7 schedule | Section header | Gate schedule enforcement. |
| Start / End | Active hours | Set the active window. |
| Session (min) / Break (min) | Session / break | Set session and break lengths. |
| Max attacks per hour | Card row | Hard hourly ceiling. Named for AT (`Max attacks per hour`). |
| Max attacks per day | Card row | Daily ceiling. Named for AT (`Max attacks per day`). |
| Offline simulation | Card row | Stop the game during breaks. |

### Storage

| Control | Location | Action |
|---|---|---|
| Halt on gold / elixir / dark | Threshold rows | Choose which full storages halt. |
| Stop if any full / Stop only if all three | Rule choice | Choose halt rule. |
| Full storage threshold percent | Threshold grid | Fullness that triggers a halt. |
| Resume attacks threshold percent | Threshold grid | Fullness that allows resuming. |
| Auto-collect resources | Card row | Collect resources automatically. |

## Settings

| Control | Location | Action |
|---|---|---|
| Dark theme | Appearance | Toggle dark/light palette. |
| Compact density | Appearance | Toggle compact spacing. |
| Reduce motion | Appearance | Disable decorative motion. |
| 24-hour clock | Appearance | Toggle clock format. |
| Default page | Behavior | Choose the view opened on launch. |
| Confirm before stopping | Behavior | Require confirmation before Stop. |
| Open wizard | Behavior | Replay the setup wizard. Disabled while running, with a visible reason. |
| Export diagnostics | System | Download a JSON diagnostic snapshot. |
| Check now | Updates | Check the release feed; opens the update modal when available. |
| Review update | Updates, conditional | Open the update modal. |
| Resume | Updates, conditional on a skipped version | Clear the skipped version. |
| Open builder menu / Open filters / Close menu | Testing | Tap the in-game builder UI. Disabled while running, with a visible hint. |
| Reset all statistics | Danger zone, two-click confirm | Wipe attack/loot/star/search/uptime totals. |
| Reset interface preferences | Danger zone, two-click confirm | Restore UI defaults only. |

## Update modal

| Control | Location | Action |
|---|---|---|
| Close update dialog | Header | Close the modal without dismissing the update. |
| Update to v`<version>` | Footer primary | One-click download, verify, install, restart. |
| Download only | Footer | Download and verify without installing. |
| Open in Finder | Footer | Reveal the downloaded bundle. Disabled until ready. |
| Re-check | Footer | Re-query the release feed. |
| Skip version | Footer | Mute this version and close. |
| Later | Footer | Close the modal. |
| Resume notifications for v`<version>` | Footer, conditional | Clear the muted version. |

## Setup wizard

| Control | Location | Action |
|---|---|---|
| 1 Army / 2 Builders / 3 Settings | Wizard rail | Jump to a step. |
| Set up manually | Wizard rail footer | Close the wizard and go to Config. |
| Plan card radio | Step 1 | Choose the army plan. |
| Slot 1 / 2 / 3 / 4 | Step 1, saved recipe slot | Choose the recipe slot. |
| Continue to builders | Step 1 footer | Advance to Builders. |
| Enable auto upgrades | Step 2 header | Gate all builder/lab/pet automation in this setup. |
| Strategic rush / Max everything | Step 2 upgrade plan | Choose upgrade mode. |
| Heroes / Army / Defenses / Resources / Traps | Step 2 type chips | Include/exclude upgrade types; the last active chip locks with a title reason. |
| Keep 1 builder for walls | Step 2 switch | Preset that enables wall upgrades and reserves one builder. |
| Automate laboratory research | Step 2 switch | Enable lab setup. |
| Laboratory research order | Step 2 select | Choose lab order. |
| Automate hero pets | Step 2 switch | Enable pet setup. |
| Back | Step 2 footer | Return to Army. |
| Continue to settings | Step 2 footer | Advance to Settings. |
| Minimum gold / elixir / dark elixir loot | Step 3 | Set manual search floors. |
| Adaptive loot | Step 3 switch | Use learned floors. |
| Back | Step 3 footer | Return to Builders. |
| Start with `<plan>` | Step 3 footer | Apply the army, slot, upgrade and loot choices, then return to Dashboard. It does not start the bot. |

## Duplicate and placement conclusions

**Fixed: report rows had two keyboard targets.** Dashboard raid rows were
focusable (`tabIndex={0}` plus Enter/Space handling) while the row's chevron
button performed the same open-report action. The row keeps its pointer click
as a broad hit target; the chevron is now the single keyboard/AT path, so each
report adds one focus stop instead of two.

**Fixed: schedule ceilings had no accessible name.** The max/hour and max/day
inputs were announced as bare spinbuttons. Both now carry explicit labels.

**Kept: Start/Stop in the sidebar and on the active account card.** These are
the same global engine actions, but the account card is the contextual place
to start/stop that account and the sidebar is the persistent global control.
This repetition is an explicit product decision and the labels are identical.

**Kept: Settings > Check now and update modal > Re-check.** The modal is a
focused update surface; Re-check saves a round trip while reading release
notes. Both call the same updater check binding.

**Kept: topbar update chip and Settings > Review update.** The chip advertises
state from anywhere; the Settings row is the stable home. Both open the same
modal, so there is no second update flow.

**Kept: setup wizard vs Army/Config.** The wizard is a guided first-run/replay
shortcut for three decisions that remain editable later; its footer says
`You can change all of this later`. It writes the same config fields, but the
wizard is a preset, not a competing settings surface. Its wall switch is a
one-builder preset; Config exposes the granular wall switch, resource and
reserve count. Labels differ because the wizard promises a preset rather than
the full control set.

**Kept: Refresh accounts and the error-banner Retry.** Retry appears only after
a load/action failure and calls the same `loadAccounts` handler from beside
the error; keeping it there saves a trip back to the page head. It cannot
become a second independent refresh (the banner disappears on success).

**Kept: drawer/modal dismissal pairs.** Close (X), Cancel, Later and the scrim
are standard alternate dismissals, not duplicate settings. `Add account` on
the page opens the drawer; `Add account` in the drawer submits it; the label
is contextual and the page control is inert behind the scrim. The empty-state
`Add account` is the same open-drawer action as the page-head button, kept
only because the head button is visually far from the empty card; both call
one `setEditor` state and cannot diverge.

**No relocation made.** Plan strategy/slot/auto-end stay on Army; walls stay
under Builders; storage has no wall controls; rotation stays on Accounts;
updates, diagnostics and device tests stay on Settings. No control was moved
on taste.

## Crash fallback

| Control | Location | Action |
|---|---|---|
| Reload ClashGO | Error boundary card, only after a render crash | Reload the window and remount the app. |

## Size matrix

Measured in the Wails stub with the real layout at:

- `1024x768` (Wails minimum)
- `1024x600` (short)
- `1024x1440` (tall)

Every main view and every Config tab reported `pageOverflowX = 0`,
`contentOverflowX = 0`. A second pass scrolled every rendered
button/input/select/textarea/tab/switch into view and required its rect to sit
inside its own scroll container: 0 failures at all three heights on Dashboard
(9–12 controls depending on whether logs/reports were present), Army (6),
Analytics (3), Accounts (15), Config (14 / 20 / 32 / 14 / 14 across Search,
Builders, Lab & pets, Schedule, Storage) and Settings (14 rendered; the
conditional Review update/Resume rows add one or two when active). Drawer
fields (15), all 7 update-modal buttons rendered in the stub (the conditional
Resume adds an eighth), and all three wizard steps (11 / 18 / 10) also passed,
with their bodies scrolling under fixed headers/footers at 1024×600.

At `600px` wide (below the Wails minimum) all views still report zero page
overflow; the Config tab strip scrolls horizontally to reach Storage. At
`480px`, the expanded sidebar leaves only 248px of content and Dashboard's
console toolbar clips; this is below the enforced `1024x768` window minimum and
was intentionally not restyled. Collapsing the sidebar still makes the content
usable in browser-only development.
