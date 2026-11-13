import React from 'react';
import { main } from '../../wailsjs/go/models';
import { compactNumber } from '../utils';
import Icon from './ui/Icon';
import { Switch } from './ui/Switch';

export interface LaunchTuning {
  adaptive: boolean;
  minGold: number;
  minElixir: number;
  minDark: number;
  dryRun: boolean;
}

export interface LaunchUpgrades {
  enabled: boolean;
  mode: 'strategic_rush' | 'max';
  heroes: boolean;
  army: boolean;
  defenses: boolean;
  resources: boolean;
  traps: boolean;
  wallBuilder: boolean;
}

interface LaunchSetupProps {
  options: main.LaunchOptions | null;
  busy: boolean;
  onChoose: (file: string, armySlot: number, upgrades: LaunchUpgrades, tuning: LaunchTuning) => void;
  onManual: () => void;
}

const STEPS = [
  { id: 'army', label: 'Army', title: 'Pick your army', blurb: 'One choice and you are attacking. The bot arms the saved recipe, picks the target side, and tunes loot for you.' },
  { id: 'builders', label: 'Builders', title: 'Set your builders', blurb: 'Tell the bot what to upgrade while you raid. Strategic rush is the guide order; Max takes the biggest upgrade.' },
  { id: 'loot', label: 'Settings', title: 'Tune the hunt', blurb: 'Last step: how the bot searches, and a summary of everything it will do when you press Start.' },
] as const;

/**
 * Launch wizard: army → builders → settings.
 *
 * Each step owns only its own decision; the Start button lives on the last
 * step and sends all three together. Keeping the modal focused avoids the
 * "wall of toggles" the old single screen turned into once upgrades joined it.
 */
const LaunchSetup: React.FC<LaunchSetupProps> = React.memo(({ options, busy, onChoose, onManual }) => {
  const [step, setStep] = React.useState(0);
  const [selected, setSelected] = React.useState('');
  const [adaptive, setAdaptive] = React.useState(true);
  const [minGold, setMinGold] = React.useState(0);
  const [minElixir, setMinElixir] = React.useState(0);
  const [minDark, setMinDark] = React.useState(0);
  const [dryRun, setDryRun] = React.useState(false);
  // Recipe slot the player will save the army into. Seeded from the selected
  // option, overridable, and written to config on Start.
  const [slot, setSlot] = React.useState(0);
  const [upgradesOn, setUpgradesOn] = React.useState(true);
  const [upgradeMode, setUpgradeMode] = React.useState<'strategic_rush' | 'max'>('strategic_rush');
  const [categories, setCategories] = React.useState({
    heroes: true,
    army: true,
    defenses: true,
    resources: true,
    traps: true,
  });
  const [wallBuilder, setWallBuilder] = React.useState(true);
  const cardRefs = React.useRef<(HTMLButtonElement | null)[]>([]);
  const panelRef = React.useRef<HTMLDivElement | null>(null);

  // A modal must own the keyboard: focus starts on the selected step's first
  // control, Tab cycles inside the panel (the sidebar behind it is not
  // reachable), and the page behind stops scrolling. Restored on close.
  React.useEffect(() => {
    const panel = panelRef.current;
    if (!panel || !options) return;
    const focusables = () => Array.from(
      panel.querySelectorAll<HTMLElement>('button:not([disabled]), [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'),
    ).filter((el) => el.offsetParent !== null);

    if (!panel.contains(document.activeElement)) {
      (panel.querySelector<HTMLElement>('.launch-card.active') ?? focusables()[0])?.focus();
    }

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Tab') return;
      const items = focusables();
      if (items.length === 0) return;
      const first = items[0];
      const last = items[items.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    panel.addEventListener('keydown', onKeyDown);
    return () => panel.removeEventListener('keydown', onKeyDown);
  }, [options]);

  // Preselect the recommendation the moment the options arrive. Adaptive loot
  // starts on: it degrades to the current thresholds until it has data, so
  // there is nothing to lose by having it on from the first raid.
  React.useEffect(() => {
    if (!options) return;
    setAdaptive(options.adaptive_loot);
    setMinGold(options.min_gold ?? 0);
    setMinElixir(options.min_elixir ?? 0);
    setMinDark(options.min_dark ?? 0);
    setDryRun(Boolean(options.dry_run));
  }, [options]);

  React.useEffect(() => {
    if (!options || options.options.length === 0) return;
    setSelected((prev) => {
      if (prev) return prev;
      const recommended = options.options.find((option) => option.recommended);
      return recommended?.file ?? options.options[0].file;
    });
  }, [options]);

  const selectedOption = options?.options.find((option) => option.file === selected);
  const canStart = Boolean(selected) && !busy;
  const maxSlots = options?.max_army_slots ?? 4;

  // Follow the card: each army lives in its own recipe, so switching cards
  // reseeds the slot instead of carrying the previous army's answer over.
  React.useEffect(() => {
    if (selectedOption) setSlot(selectedOption.army_slot);
  }, [selectedOption]);

  const loadout = selectedOption?.loadout;
  const slotsUsed = (units: main.LaunchLoadoutUnit[] | undefined) =>
    (units ?? []).reduce((total, unit) => total + unit.count * (unit.slots || 0), 0);
  // Space a group takes / how much it has: the totals have to add up to the
  // capacity the game shows, so the meter doubles as a check on the recipe.
  const usage = (group: { units: main.LaunchLoadoutUnit[]; capacity: number; used: number }) => {
    const total = group.used || group.units.reduce((sum, unit) => sum + unit.count, 0);
    if (group.capacity <= 0) return null;
    return { total, capacity: group.capacity, over: total > group.capacity };
  };
  // One group per army-editor tab, so the numbers read in the same order the
  // player taps them in-game.
  const groups = loadout
    ? [
        { id: 'troops', label: 'Troops', units: loadout.troops ?? [], capacity: loadout.army_camp, used: slotsUsed(loadout.troops) },
        { id: 'spells', label: 'Spells', units: loadout.spells ?? [], capacity: loadout.spell_capacity, used: slotsUsed(loadout.spells) },
        { id: 'siege', label: 'Siege', units: loadout.siege ?? [], capacity: loadout.siege_capacity, used: slotsUsed(loadout.siege) },
      ].filter((group) => group.units.length > 0)
    : [];

  // Arrow keys move between cards, as a real radio group should. Without this
  // the group is only reachable by Tab and the arrow keys do nothing.
  const handleKeyDown = (event: React.KeyboardEvent) => {
    if (!options || options.options.length === 0) return;
    const current = options.options.findIndex((option) => option.file === selected);
    let next = current;
    if (event.key === 'ArrowRight' || event.key === 'ArrowDown') next = Math.min(options.options.length - 1, current + 1);
    else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') next = Math.max(0, current - 1);
    else return;
    event.preventDefault();
    const option = options.options[next];
    if (!option) return;
    setSelected(option.file);
    cardRefs.current[next]?.focus();
  };

  const upgrades: LaunchUpgrades = {
    enabled: upgradesOn,
    mode: upgradeMode,
    ...categories,
    wallBuilder,
  };
  const stepInfo = STEPS[step];
  const next = () => setStep((prev) => Math.min(STEPS.length - 1, prev + 1));
  const back = () => setStep((prev) => Math.max(0, prev - 1));

  return (
    <div
      className="launch-scrim"
      role="dialog"
      aria-modal="true"
      aria-labelledby="launch-title"
      aria-describedby="launch-desc"
    >
      <div className="launch-panel" ref={panelRef}>
        <header className="launch-head">
          <span className="launch-kicker">Quick setup</span>
          <h1 id="launch-title">{stepInfo.title}</h1>
          <p id="launch-desc">{stepInfo.blurb}</p>
        </header>

        <nav className="launch-steps" aria-label="Setup steps">
          {STEPS.map((item, index) => (
            <button
              key={item.id}
              type="button"
              className={`launch-step ${index === step ? 'active' : ''} ${index < step ? 'done' : ''}`.trim()}
              aria-current={index === step ? 'step' : undefined}
              onClick={() => setStep(index)}
              disabled={Boolean(busy)}
            >
              <span className="launch-step-num">{index < step ? <Icon name="check" size={10} /> : index + 1}</span>
              <span className="launch-step-name">{item.label}</span>
            </button>
          ))}
        </nav>

        {!options ? (
          <div className="launch-loading">Reading your strategies…</div>
        ) : (
          <div className="launch-body" key={step}>
            {step === 0 ? (
              <>
                <div className="launch-grid" role="radiogroup" aria-label="Army" onKeyDown={handleKeyDown}>
                  {options.options.map((option, index) => {
                    const active = selected === option.file;
                    return (
                      <button
                        key={option.key}
                        ref={(el) => { cardRefs.current[index] = el; }}
                        type="button"
                        role="radio"
                        aria-checked={active}
                        tabIndex={active ? 0 : -1}
                        className={`launch-card ${active ? 'active' : ''}`.trim()}
                        onClick={() => setSelected(option.file)}
                      >
                        <span className="launch-card-top">
                          <span className="launch-name">{option.name}</span>
                          {option.recommended ? <span className="launch-badge">Recommended</span> : null}
                        </span>
                        <span className="launch-rate">
                          <b className="mono">{compactNumber(option.loot_per_hour)}</b>
                          <span className="launch-rate-unit">loot / hr</span>
                        </span>
                        <span className="launch-rate-note">
                          {option.measured
                            ? `measured · ${option.raids} raids`
                            : 'estimate · replaced by your own numbers'}
                        </span>
                        <span className="launch-pitch">{option.pitch}</span>
                        <span className="launch-meta">
                          <span className="tag">Recipe {option.army_slot}</span>
                          {option.end_at_percent > 0 ? <span className="tag">Ends at {option.end_at_percent}%</span> : null}
                          <span className="launch-check" aria-hidden={!active}>
                            {active ? <Icon name="check" size={10} /> : null}
                          </span>
                        </span>
                      </button>
                    );
                  })}
                </div>

                {selectedOption && loadout && groups.length > 0 ? (
                  <section className="launch-setup" aria-label="Army setup">
                    <div className="ls-top">
                      <span className="ls-kicker">Army setup</span>
                      <b className="ls-headline">Build this army, then save it as a recipe</b>
                      <span className="ls-caps mono">
                        {loadout.army_camp} camp · {loadout.spell_capacity} spell slots · {loadout.siege_capacity} siege
                      </span>
                    </div>

                    <div className="ls-lines">
                      {groups.map((group) => {
                        const use = usage(group);
                        return (
                          <div className={`ls-line${use?.over ? ' over' : ''}`} key={group.id}>
                            <span className="ls-label">{group.label}</span>
                            <span className="ls-units">
                              {group.units.map((unit) => (
                                <span className="ls-unit" key={`${group.id}-${unit.name}`}>
                                  <b className="mono">{unit.count}×</b>
                                  {unit.name}
                                  {unit.slots > 0 ? (
                                    <i className="ls-cost mono">{unit.slots} space</i>
                                  ) : null}
                                </span>
                              ))}
                            </span>
                            {use ? (
                              <span className="ls-usage">
                                <span className="ls-meter" aria-hidden="true">
                                  <i style={{ width: `${Math.min(100, (use.total / use.capacity) * 100)}%` }} />
                                </span>
                                <span className="ls-usage-text mono">
                                  {use.total}/{use.capacity}
                                </span>
                                {use.over ? (
                                  <span className="ls-usage-warn">
                                    {use.total - use.capacity} over
                                  </span>
                                ) : null}
                              </span>
                            ) : null}
                          </div>
                        );
                      })}
                    </div>

                    <div className="ls-slots">
                      <span className="ls-label">Saved recipe</span>
                      <div className="ls-slots-chips" role="radiogroup" aria-label="Saved recipe slot">
                        {Array.from({ length: maxSlots }, (_, index) => index + 1).map((value) => (
                          <button
                            key={value}
                            type="button"
                            role="radio"
                            aria-checked={slot === value}
                            className={`slot-chip ${slot === value ? 'active' : ''}`.trim()}
                            onClick={() => setSlot(value)}
                          >
                            {value}
                          </button>
                        ))}
                      </div>
                      <span className="ls-slots-hint">
                        Save it in slot {slot || selectedOption.army_slot} — the bot arms it before every raid
                      </span>
                    </div>
                  </section>
                ) : null}
              </>
            ) : null}

            {step === 1 ? (
              <section className="launch-setup launch-upgrades" aria-label="Auto upgrades">
                <div className="ls-top">
                  <span className="ls-kicker">Auto upgrades</span>
                  <b className="ls-headline">Builders work while you raid</b>
                  <Switch checked={upgradesOn} onChange={setUpgradesOn} ariaLabel="Enable auto upgrades" />
                </div>

                <div className={`ls-upgrade-body${upgradesOn ? '' : ' is-disabled'}`}>
                  <div className="ls-modes" role="radiogroup" aria-label="Upgrade plan">
                    <button
                      type="button"
                      role="radio"
                      aria-checked={upgradeMode === 'strategic_rush'}
                      className={`ls-mode ${upgradeMode === 'strategic_rush' ? 'active' : ''}`.trim()}
                      onClick={() => setUpgradeMode('strategic_rush')}
                    >
                      <b>Strategic rush</b>
                      <span className="ls-mode-badge">Recommended</span>
                      <span className="ls-mode-copy">Heroes, then army capacity, then resources. Defenses last.</span>
                    </button>
                    <button
                      type="button"
                      role="radio"
                      aria-checked={upgradeMode === 'max'}
                      className={`ls-mode ${upgradeMode === 'max' ? 'active' : ''}`.trim()}
                      onClick={() => setUpgradeMode('max')}
                    >
                      <b>Max</b>
                      <span className="ls-mode-copy">Everything is fair game. Biggest upgrade first.</span>
                    </button>
                  </div>

                  <div className="ls-cats">
                    <span className="ls-label">Types</span>
                    <div className="ls-cats-chips">
                      {([
                        ['heroes', 'Heroes'],
                        ['army', 'Army'],
                        ['defenses', 'Defenses'],
                        ['resources', 'Resources'],
                        ['traps', 'Traps'],
                      ] as const).map(([key, label]) => (
                        <button
                          key={key}
                          type="button"
                          aria-pressed={categories[key]}
                          className={`slot-chip ${categories[key] ? 'active' : ''}`.trim()}
                          onClick={() => setCategories((prev) => ({ ...prev, [key]: !prev[key] }))}
                        >
                          {label}
                        </button>
                      ))}
                    </div>
                  </div>

                  <div className="ls-wall">
                    <div>
                      <b>Keep 1 builder for walls</b>
                      <span>Excess loot goes into walls instead of idling a builder.</span>
                    </div>
                    <Switch checked={wallBuilder} onChange={setWallBuilder} ariaLabel="Keep 1 builder for walls" />
                  </div>
                </div>

                <p className="launch-note">
                  The bot scans the whole builder list, scrolling to the bottom and back, then taps the row this plan chooses.
                </p>
              </section>
            ) : null}

            {step === 2 ? (
              <>
                <section className="launch-setup" aria-label="Loot floors">
                  <div className="ls-top">
                    <span className="ls-kicker">Loot floors</span>
                    <b className="ls-headline">Skip bases below these totals</b>
                    <span className="ls-caps">0 = accept any base</span>
                  </div>
                  <div className="launch-loot-grid">
                    <label className="launch-loot">
                      <span>Gold</span>
                      <input
                        type="number"
                        min={0}
                        step={100000}
                        className="field mono"
                        value={minGold}
                        onChange={(event) => setMinGold(Math.max(0, parseInt(event.target.value, 10) || 0))}
                      />
                    </label>
                    <label className="launch-loot">
                      <span>Elixir</span>
                      <input
                        type="number"
                        min={0}
                        step={100000}
                        className="field mono"
                        value={minElixir}
                        onChange={(event) => setMinElixir(Math.max(0, parseInt(event.target.value, 10) || 0))}
                      />
                    </label>
                    <label className="launch-loot">
                      <span>Dark elixir</span>
                      <input
                        type="number"
                        min={0}
                        step={1000}
                        className="field mono"
                        value={minDark}
                        onChange={(event) => setMinDark(Math.max(0, parseInt(event.target.value, 10) || 0))}
                      />
                    </label>
                  </div>
                </section>

                <div className="launch-adaptive">
                  <div className="launch-adaptive-label">
                    <b>Adaptive loot</b>
                    <span>
                      Tunes the search minimum from what you actually see. Needs about {options?.min_samples ?? 40} bases;
                      your floors hold until then.
                    </span>
                  </div>
                  <Switch checked={adaptive} onChange={setAdaptive} ariaLabel="Adaptive loot" />
                </div>

                <div className="launch-adaptive">
                  <div className="launch-adaptive-label">
                    <b>Dry run</b>
                    <span>Inspect builder upgrades without confirming or spending resources.</span>
                  </div>
                  <Switch checked={dryRun} onChange={setDryRun} ariaLabel="Dry run" />
                </div>

                <section className="launch-summary" aria-label="Setup summary">
                  <span className="ls-kicker">Ready</span>
                  <div className="launch-summary-row">
                    <span>Army</span>
                    <b>{selectedOption?.name ?? 'None selected'}</b>
                  </div>
                  <div className="launch-summary-row">
                    <span>Saved recipe</span>
                    <b>Slot {slot || selectedOption?.army_slot || 1}</b>
                  </div>
                  <div className="launch-summary-row">
                    <span>Builders</span>
                    <b>
                      {upgradesOn
                        ? `${upgradeMode === 'strategic_rush' ? 'Strategic rush' : 'Max'} · ${Object.values(categories).filter(Boolean).length} types${wallBuilder ? ' · walls reserved' : ''}`
                        : 'Off'}
                    </b>
                  </div>
                  <div className="launch-summary-row">
                    <span>Loot search</span>
                    <b>{adaptive ? 'Adaptive' : 'Manual floors'}</b>
                  </div>
                  <div className="launch-summary-row">
                    <span>Floors</span>
                    <b>
                      {compactNumber(minGold)} gold · {compactNumber(minElixir)} elixir · {compactNumber(minDark)} DE
                    </b>
                  </div>
                  <div className="launch-summary-row">
                    <span>Mode</span>
                    <b>{dryRun ? 'Dry run (no upgrades confirmed)' : 'Live'}</b>
                  </div>
                </section>

                <p className="launch-note">
                  Loot per hour is an estimate on a fresh profile. It is replaced automatically by your measured
                  averages once an army has a few raids behind it.
                </p>
              </>
            ) : null}
          </div>
        )}

        <div className="launch-foot">
          <div className="launch-foot-left">
            {step > 0 ? (
              <button type="button" className="btn ghost" onClick={back} disabled={busy}>
                Back
              </button>
            ) : (
              <button type="button" className="btn ghost" onClick={onManual} disabled={busy}>
                Set up manually
              </button>
            )}
          </div>
          {step < STEPS.length - 1 ? (
            <button
              type="button"
              className="btn primary launch-cta launch-next"
              onClick={next}
              disabled={!selected || busy}
            >
              Next
              <Icon name="chevronRight" size={13} />
            </button>
          ) : (
            <button
              type="button"
              className="btn primary launch-cta launch-next"
              disabled={!canStart}
              onClick={() => onChoose(selected, slot, upgrades, { adaptive, minGold, minElixir, minDark, dryRun })}
            >
              {busy ? 'Applying…' : selectedOption ? `Start with ${selectedOption.name}` : 'Start with this army'}
            </button>
          )}
        </div>
      </div>
    </div>
  );
});

LaunchSetup.displayName = 'LaunchSetup';

export default LaunchSetup;
