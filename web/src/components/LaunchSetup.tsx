import React from 'react';
import { main } from '../../wailsjs/go/models';
import { compactNumber } from '../utils';
import Icon from './ui/Icon';
import UnitIcon from './ui/UnitIcon';
import { Switch } from './ui/Switch';

export interface LaunchTuning {
  adaptive: boolean;
  minGold: number;
  minElixir: number;
  minDark: number;
}

export interface LaunchUpgrades {
  enabled: boolean;
  mode: 'strategic_rush' | 'max';
  lab: boolean;
  labMode: 'troops_first' | 'spells_first' | 'siege_first' | 'cheapest_first';
  pets: boolean;
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
  {
    id: 'army',
    label: 'Army',
    meta: 'Choose a raid plan',
    title: 'Choose your raid',
    blurb: 'Pick the plan the bot arms before every attack. The recipe, hero timing, and end condition come with it.',
  },
  {
    id: 'builders',
    label: 'Builders',
    meta: 'Automate upgrades',
    title: 'Put builders to work',
    blurb: 'Builders, research, and pets spend loot between raids. Pick the order and what is fair game.',
  },
  {
    id: 'loot',
    label: 'Settings',
    meta: 'Loot and review',
    title: 'Set the search rules',
    blurb: 'Decide what a base has to be worth. Then review the full launch brief before the first raid.',
  },
] as const;

const ARMY_GLYPHS: Record<string, string> = {
  edrag: 'Electro Dragon',
  valk: 'Valkyrie',
};

const DEFAULT_PET_ORDER = [
  'Phoenix', 'Spirit Fox', 'Sneezy', 'Angry Jelly', 'Frosty', 'Diggy',
  'Greedy Raven', 'Unicorn', 'Electro Owl', 'L.A.S.S.I', 'Mighty Yak', 'Poison Lizard',
];

/**
 * Launch wizard: army → builders → settings.
 *
 * The rail is orientation only; the content pane carries one decision per
 * step. Start lives on the last step and writes all three together.
 */
const LaunchSetup: React.FC<LaunchSetupProps> = React.memo(({ options, busy, onChoose, onManual }) => {
  const [step, setStep] = React.useState(0);
  const [selected, setSelected] = React.useState('');
  const [adaptive, setAdaptive] = React.useState(true);
  const [minGold, setMinGold] = React.useState(0);
  const [minElixir, setMinElixir] = React.useState(0);
  const [minDark, setMinDark] = React.useState(0);
  const [slot, setSlot] = React.useState(0);
  const [upgradesOn, setUpgradesOn] = React.useState(true);
  const [upgradeMode, setUpgradeMode] = React.useState<'strategic_rush' | 'max'>('strategic_rush');
  const [labOn, setLabOn] = React.useState(true);
  const [labMode, setLabMode] = React.useState<'troops_first' | 'spells_first' | 'siege_first' | 'cheapest_first'>('troops_first');
  const [petsOn, setPetsOn] = React.useState(true);
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
  // control, Tab cycles inside the panel, and the page behind stays inert.
  React.useEffect(() => {
    const panel = panelRef.current;
    if (!panel || !options) return;
    const focusables = () => Array.from(
      panel.querySelectorAll<HTMLElement>('button:not([disabled]), [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'),
    ).filter((el) => el.offsetParent !== null);

    if (!panel.contains(document.activeElement)) {
      (panel.querySelector<HTMLElement>('.launch-plan.active, .launch-choice.active') ?? focusables()[0])?.focus();
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

  React.useEffect(() => {
    if (!options) return;
    setAdaptive(options.adaptive_loot);
    setMinGold(options.min_gold ?? 0);
    setMinElixir(options.min_elixir ?? 0);
    setMinDark(options.min_dark ?? 0);
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

  React.useEffect(() => {
    if (selectedOption) setSlot(selectedOption.army_slot);
  }, [selectedOption]);

  const loadout = selectedOption?.loadout;
  const petOrder = options?.pet_priorities?.length ? options.pet_priorities : DEFAULT_PET_ORDER;
  const slotsUsed = (units: main.LaunchLoadoutUnit[] | undefined) =>
    (units ?? []).reduce((total, unit) => total + unit.count * (unit.slots || 0), 0);
  const usage = (group: { units: main.LaunchLoadoutUnit[]; capacity: number; used: number }) => {
    const total = group.used || group.units.reduce((sum, unit) => sum + unit.count, 0);
    if (group.capacity <= 0) return null;
    return { total, capacity: group.capacity, over: total > group.capacity };
  };
  const groups = loadout
    ? [
        { id: 'troops', label: 'Troops', units: loadout.troops ?? [], capacity: loadout.army_camp, used: slotsUsed(loadout.troops) },
        { id: 'spells', label: 'Spells', units: loadout.spells ?? [], capacity: loadout.spell_capacity, used: slotsUsed(loadout.spells) },
        { id: 'siege', label: 'Siege', units: loadout.siege ?? [], capacity: loadout.siege_capacity, used: slotsUsed(loadout.siege) },
      ].filter((group) => group.units.length > 0)
    : [];

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
    lab: labOn,
    labMode,
    pets: petsOn,
  };
  const stepInfo = STEPS[step];
  const next = () => setStep((prev) => Math.min(STEPS.length - 1, prev + 1));
  const back = () => setStep((prev) => Math.max(0, prev - 1));
  const enabledTypes = Object.values(categories).filter(Boolean).length;

  return (
    <div
      className="launch-scrim"
      role="dialog"
      aria-modal="true"
      aria-labelledby="launch-title"
      aria-describedby="launch-desc"
    >
      <div className="launch-panel" ref={panelRef}>
        <aside className="launch-rail">
          <div className="launch-brand">
            <Icon name="bolt" size={15} />
            <div className="launch-brand-copy">
              <b>Setup</b>
              <span>ClashGO</span>
            </div>
          </div>

          <nav className="launch-nav" aria-label="Setup steps">
            {STEPS.map((item, index) => (
              <button
                key={item.id}
                type="button"
                className={`launch-nav-item ${index === step ? 'active' : ''} ${index < step ? 'done' : ''}`.trim()}
                aria-current={index === step ? 'step' : undefined}
                onClick={() => setStep(index)}
                disabled={Boolean(busy)}
              >
                <span className="ln-dot">{index < step ? <Icon name="check" size={10} /> : index + 1}</span>
                <span className="ln-copy">
                  <b>{item.label}</b>
                  <em>{item.meta}</em>
                </span>
              </button>
            ))}
          </nav>

          <div className="launch-rail-foot">
            <button type="button" className="launch-manual" onClick={onManual} disabled={busy}>
              Set up manually
              <Icon name="chevronRight" size={12} />
            </button>
          </div>
        </aside>

        <div className="launch-main">
          <header className="launch-head">
            <span className="launch-step-label">Step {step + 1} of {STEPS.length}</span>
            <h1 id="launch-title">{stepInfo.title}</h1>
            <p id="launch-desc">{stepInfo.blurb}</p>
          </header>

          {!options ? (
            <div className="launch-body">
              <div className="launch-loading">
                <span className="launch-loading-dot" />
                Reading your strategies…
              </div>
            </div>
          ) : (
            <div className="launch-body" key={step}>
              {step === 0 ? (
                <>
                  <div className="launch-army-choice" role="radiogroup" aria-label="Army" onKeyDown={handleKeyDown}>
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
                          className={`launch-plan ${active ? 'active' : ''}`.trim()}
                          onClick={() => setSelected(option.file)}
                        >
                          <span className="plan-head">
                            <span className="plan-emblem">
                              <UnitIcon name={ARMY_GLYPHS[option.key] ?? 'Valkyrie'} size={20} />
                            </span>
                            <span className="plan-title">
                              <b>{option.name}</b>
                              <span>{option.measured ? `${option.raids} measured raids` : 'Estimated rate'}</span>
                            </span>
                            <span className="plan-select" aria-hidden="true">
                              {active ? <Icon name="check" size={10} /> : null}
                            </span>
                          </span>

                          <span className="plan-rate">
                            <b className="mono">{compactNumber(option.loot_per_hour)}</b>
                            <span>loot / hour</span>
                            {option.recommended ? <em className="plan-recommended">Recommended</em> : null}
                          </span>
                          <span className="plan-pitch">{option.pitch}</span>

                          <span className="plan-foot">
                            <span>Recipe {option.army_slot}</span>
                            {option.end_at_percent > 0 ? <span>Ends at {option.end_at_percent}%</span> : null}
                          </span>
                        </button>
                      );
                    })}
                  </div>

                  {selectedOption && loadout && groups.length > 0 ? (
                    <section className="launch-loadout" aria-label="Army setup">
                      <header className="section-head">
                        <div>
                          <h2>Army loadout</h2>
                          <p>Build this in-game before starting.</p>
                        </div>
                        <span className="section-meta mono">
                          {loadout.army_camp} camp · {loadout.spell_capacity} spells · {loadout.siege_capacity} siege
                        </span>
                      </header>

                      <div className="loadout-lines">
                        {groups.map((group) => {
                          const use = usage(group);
                          return (
                            <div className={`loadout-line${use?.over ? ' over' : ''}`} key={group.id}>
                              <span className="loadout-label">{group.label}</span>
                              <span className="loadout-units">
                                {group.units.map((unit) => (
                                  <span className="loadout-unit" key={`${group.id}-${unit.name}`}>
                                    <UnitIcon name={unit.name} size={13} />
                                    <b>{unit.count}×</b>
                                    {unit.name}
                                  </span>
                                ))}
                              </span>
                              {use ? (
                                <span className="loadout-usage">
                                  <span className="loadout-meter" aria-hidden="true">
                                    <i style={{ width: `${Math.min(100, (use.total / use.capacity) * 100)}%` }} />
                                  </span>
                                  <span className="mono">{use.total}/{use.capacity}</span>
                                  {use.over ? <span className="loadout-warn">{use.total - use.capacity} over</span> : null}
                                </span>
                              ) : null}
                            </div>
                          );
                        })}
                      </div>

                      <div className="loadout-slots">
                        <span className="loadout-label">Recipe slot</span>
                        <div className="loadout-slot-chips" role="radiogroup" aria-label="Saved recipe slot">
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
                        <span className="loadout-slot-note">Armed automatically before each raid</span>
                      </div>
                    </section>
                  ) : null}
                </>
              ) : null}

              {step === 1 ? (
                <section className="launch-section">
                  <header className="section-head">
                    <div>
                      <h2>Upgrade engine</h2>
                      <p>Runs after every return home, using the loot you just earned.</p>
                    </div>
                    <Switch checked={upgradesOn} onChange={setUpgradesOn} ariaLabel="Enable auto upgrades" />
                  </header>

                  <div className={`launch-block${upgradesOn ? '' : ' is-disabled'}`}>
                    <div className="launch-choices" role="radiogroup" aria-label="Upgrade plan">
                      <button
                        type="button"
                        role="radio"
                        aria-checked={upgradeMode === 'strategic_rush'}
                        className={`launch-choice ${upgradeMode === 'strategic_rush' ? 'active' : ''}`.trim()}
                        onClick={() => setUpgradeMode('strategic_rush')}
                      >
                        <span className="choice-radio" aria-hidden="true"><i /></span>
                        <span className="choice-copy">
                          <span className="choice-title">
                            <b>Strategic rush</b>
                            <em>Recommended</em>
                          </span>
                          <span>Heroes first, then army capacity, lab, and pets. Defenses wait until the core is done.</span>
                        </span>
                      </button>

                      <button
                        type="button"
                        role="radio"
                        aria-checked={upgradeMode === 'max'}
                        className={`launch-choice ${upgradeMode === 'max' ? 'active' : ''}`.trim()}
                        onClick={() => setUpgradeMode('max')}
                      >
                        <span className="choice-radio" aria-hidden="true"><i /></span>
                        <span className="choice-copy">
                          <span className="choice-title"><b>Max everything</b></span>
                          <span>Every building is fair game. The most expensive affordable upgrade wins.</span>
                        </span>
                      </button>
                    </div>

                    <div className="launch-types">
                      <div className="field-head">
                        <span>Upgrade types</span>
                        <span className="mono">{enabledTypes} of 5 active</span>
                      </div>
                      <div className="launch-type-chips">
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
                            className={`type-chip ${categories[key] ? 'active' : ''}`.trim()}
                            title={`${categories[key] ? 'Skip' : 'Include'} ${label.toLowerCase()} upgrades`}
                            onClick={() => setCategories((prev) => {
                              const nextCategories = { ...prev, [key]: !prev[key] };
                              return Object.values(nextCategories).some(Boolean) ? nextCategories : prev;
                            })}
                          >
                            <span className="type-check" aria-hidden="true">{categories[key] ? <Icon name="check" size={9} /> : null}</span>
                            {label}
                          </button>
                        ))}
                      </div>
                      <p className="field-hint">At least one type stays active. Click a type to include or skip it.</p>
                    </div>

                    <div className="launch-toggle-row">
                      <div>
                        <b>Keep 1 builder for walls</b>
                        <span>Excess loot goes into walls instead of idling a builder.</span>
                      </div>
                      <Switch checked={wallBuilder} onChange={setWallBuilder} ariaLabel="Keep 1 builder for walls" />
                    </div>

                    <div className="launch-automation">
                      <div className="automation-item">
                        <div className="automation-head">
                          <b>Laboratory</b>
                          <Switch checked={labOn} onChange={setLabOn} ariaLabel="Automate laboratory research" />
                        </div>
                        <select
                          className="field select launch-order-select"
                          value={labMode}
                          disabled={!labOn}
                          onChange={(event) => setLabMode(event.target.value as typeof labMode)}
                          aria-label="Laboratory research order"
                        >
                          <option value="troops_first">Troops first</option>
                          <option value="spells_first">Spells first</option>
                          <option value="siege_first">Siege first</option>
                          <option value="cheapest_first">Cheapest first</option>
                        </select>
                        <span className="automation-note">Research runs between builder upgrades.</span>
                      </div>

                      <div className="automation-item">
                        <div className="automation-head">
                          <b>Hero pets</b>
                          <Switch checked={petsOn} onChange={setPetsOn} ariaLabel="Automate hero pets" />
                        </div>
                        <p className={`automation-order${petsOn ? '' : ' is-off'}`}>{petOrder.join(' → ')}</p>
                        <span className="automation-note">Upgrades follow this order when a pet is free.</span>
                      </div>
                    </div>

                  </div>
                </section>
              ) : null}

              {step === 2 ? (
                <>
                  <section className="launch-section">
                    <header className="section-head">
                      <div>
                        <h2>Loot floors</h2>
                        <p>
                          {adaptive
                            ? 'Adaptive loot sets these floors as it learns. Turn it off to edit them.'
                            : 'Skip bases below these totals. Set 0 to accept any base.'}
                        </p>
                      </div>
                    </header>

                    <div className={`loot-cards${adaptive ? ' is-locked' : ''}`} aria-disabled={adaptive}>
                      <label className="loot-card">
                        <span className="loot-card-head">
                          <Icon name="coin" size={13} />
                          <span>Gold</span>
                        </span>
                        <span className="loot-input">
                          <input
                            type="number"
                            min={0}
                            step={100000}
                            className="mono"
                            aria-label="Minimum gold loot"
                            disabled={adaptive}
                            value={minGold}
                            onChange={(event) => setMinGold(Math.max(0, parseInt(event.target.value, 10) || 0))}
                          />
                        </span>
                      </label>

                      <label className="loot-card">
                        <span className="loot-card-head">
                          <Icon name="drop" size={13} />
                          <span>Elixir</span>
                        </span>
                        <span className="loot-input">
                          <input
                            type="number"
                            min={0}
                            step={100000}
                            className="mono"
                            aria-label="Minimum elixir loot"
                            disabled={adaptive}
                            value={minElixir}
                            onChange={(event) => setMinElixir(Math.max(0, parseInt(event.target.value, 10) || 0))}
                          />
                        </span>
                      </label>

                      <label className="loot-card">
                        <span className="loot-card-head">
                          <Icon name="bolt" size={13} />
                          <span>Dark elixir</span>
                        </span>
                        <span className="loot-input">
                          <input
                            type="number"
                            min={0}
                            step={1000}
                            className="mono"
                            aria-label="Minimum dark elixir loot"
                            disabled={adaptive}
                            value={minDark}
                            onChange={(event) => setMinDark(Math.max(0, parseInt(event.target.value, 10) || 0))}
                          />
                        </span>
                      </label>
                    </div>

                    <div className="launch-toggles">
                      <div className="launch-toggle-row">
                        <div>
                          <b>Adaptive loot</b>
                          <span>
                            Sets the floors above from what you actually see. Needs about {options?.min_samples ?? 40} bases;
                            your numbers seed it until then.
                          </span>
                        </div>
                        <Switch checked={adaptive} onChange={setAdaptive} ariaLabel="Adaptive loot" />
                      </div>
                    </div>
                  </section>

                  <section className="launch-brief" aria-label="Setup summary">
                    <header>
                      <h2>Summary</h2>
                      <span className="brief-status"><i />Ready</span>
                    </header>

                    <div className="brief-grid">
                      <div><span>Army</span><b>{selectedOption?.name ?? 'None selected'}</b></div>
                      <div><span>Recipe slot</span><b>{slot || selectedOption?.army_slot || 1}</b></div>
                      <div>
                        <span>Builders</span>
                        <b>
                          {upgradesOn
                            ? `${upgradeMode === 'strategic_rush' ? 'Strategic rush' : 'Max'} · ${enabledTypes} types${labOn ? ' · lab' : ''}${petsOn ? ' · pets' : ''}`
                            : 'Off'}
                        </b>
                      </div>
                      <div><span>Loot search</span><b>{adaptive ? 'Adaptive' : 'Manual floors'}</b></div>
                      <div>
                        <span>Wall builder</span>
                        <b>{upgradesOn && wallBuilder ? '1 reserved' : 'Off'}</b>
                      </div>
                      <div>
                        <span>Floors</span>
                        <b className="mono">G {compactNumber(minGold)} · E {compactNumber(minElixir)} · DE {compactNumber(minDark)}</b>
                      </div>
                    </div>
                  </section>
                </>
              ) : null}
            </div>
          )}

          <footer className="launch-foot">
            <div className="launch-foot-info">
              <Icon name="shield" size={12} />
              <span>{step === 2 ? 'Writes to config.json when you start' : step === 1 ? 'Builders, lab, and pets run between raids' : 'You can change all of this later'}</span>
            </div>
            <div className="launch-foot-actions">
              {step > 0 ? (
                <button type="button" className="btn ghost launch-back" onClick={back} disabled={busy}>
                  Back
                </button>
              ) : null}
              {step < STEPS.length - 1 ? (
                <button
                  type="button"
                  className="btn primary launch-cta"
                  onClick={next}
                  disabled={!selected || busy}
                >
                  {step === 0 ? 'Continue to builders' : 'Continue to settings'}
                  <Icon name="chevronRight" size={13} />
                </button>
              ) : (
                <button
                  type="button"
                  className="btn primary launch-cta"
                  disabled={!canStart}
                  onClick={() => onChoose(selected, slot, upgrades, { adaptive, minGold, minElixir, minDark })}
                >
                  {busy ? 'Applying…' : selectedOption ? `Start with ${selectedOption.name}` : 'Start with this army'}
                  <Icon name="play" size={12} />
                </button>
              )}
            </div>
          </footer>
        </div>
      </div>
    </div>
  );
});

LaunchSetup.displayName = 'LaunchSetup';

export default LaunchSetup;
