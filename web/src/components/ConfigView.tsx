import React from 'react';
import { main } from '../../wailsjs/go/models';
import { compactNumber, exactNumber } from '../utils';
import Icon, { IconName } from './ui/Icon';
import { Switch, SwitchRow } from './ui/Switch';

interface ConfigViewProps {
  goldThreshold: number;
  setGoldThreshold: (v: number) => void;
  elixirThreshold: number;
  setElixirThreshold: (v: number) => void;
  deThreshold: number;
  setDeThreshold: (v: number) => void;
  searchEnabled: boolean;
  setSearchEnabled: (v: boolean) => void;
  upgradeWalls: boolean;
  setUpgradeWalls: (v: boolean) => void;
  stallTimer: number;
  setStallTimer: (v: number) => void;
  autoLoot: main.AutotuneStatus | null;
  autoLootBusy: boolean;
  onToggleAutoLoot: (enabled: boolean) => void;
  onApplyAutoLoot: () => void;
  onResetAutoLoot: () => void;
  haltGold: boolean;
  setHaltGold: (v: boolean) => void;
  haltElixir: boolean;
  setHaltElixir: (v: boolean) => void;
  haltDE: boolean;
  setHaltDE: (v: boolean) => void;
  haltRule: string;
  setHaltRule: (rule: string) => void;
  storageThreshold: number;
  setStorageThreshold: (v: number) => void;
  resumeThreshold: number;
  setResumeThreshold: (v: number) => void;
  dumpToWallsBeforeHalt: boolean;
  setDumpToWallsBeforeHalt: (v: boolean) => void;
  autoCollect: boolean;
  setAutoCollect: (v: boolean) => void;
  scheduleEnabled: boolean;
  setScheduleEnabled: (v: boolean) => void;
  activeHoursStart: string;
  setActiveHoursStart: (v: string) => void;
  activeHoursEnd: string;
  setActiveHoursEnd: (v: string) => void;
  sessionDuration: number;
  setSessionDuration: (v: number) => void;
  breakDuration: number;
  setBreakDuration: (v: number) => void;
  forceStopOnBreak: boolean;
  setForceStopOnBreak: (v: boolean) => void;
  maxPerHour: number;
  setMaxPerHour: (v: number) => void;
  maxPerDay: number;
  setMaxPerDay: (v: number) => void;
  autoBuilders: boolean;
  setAutoBuilders: (v: boolean) => void;
  reserveBuilders: number;
  setReserveBuilders: (v: number) => void;
  upgradeStrategy: string;
  setUpgradeStrategy: (v: string) => void;
  upgradeDefenses: boolean;
  setUpgradeDefenses: (v: boolean) => void;
  upgradeResources: boolean;
  setUpgradeResources: (v: boolean) => void;
  upgradeArmy: boolean;
  setUpgradeArmy: (v: boolean) => void;
  upgradeTraps: boolean;
  setUpgradeTraps: (v: boolean) => void;
  upgradeHeroes: boolean;
  setUpgradeHeroes: (v: boolean) => void;
  maxGoldPerUpgrade: number;
  setMaxGoldPerUpgrade: (v: number) => void;
  maxElixirPerUpgrade: number;
  setMaxElixirPerUpgrade: (v: number) => void;
  maxDEPerUpgrade: number;
  setMaxDEPerUpgrade: (v: number) => void;
  autoLab: boolean;
  setAutoLab: (v: boolean) => void;
  labStrategy: string;
  setLabStrategy: (v: string) => void;
  labAllowDE: boolean;
  setLabAllowDE: (v: boolean) => void;
  autoPets: boolean;
  setAutoPets: (v: boolean) => void;
  petPriorities: string[];
  setPetPriorities: (v: string[]) => void;
  wallResource: string;
  setWallResource: (v: string) => void;
  dryRun: boolean;
  setDryRun: (v: boolean) => void;
  onSave: () => Promise<void>;
}

type SaveStatus = 'idle' | 'saving' | 'saved' | 'error';
type ConfigSubTab = 'attack' | 'upgrades' | 'lab_pets' | 'schedule' | 'storage';

const THRESHOLD_MAX = 10_000_000;
const STALL_MAX = 600;
const invalid = (value: number, max: number): boolean =>
  !Number.isFinite(value) || value < 0 || value > max;

const Section: React.FC<{
  title: string;
  sub?: string;
  action?: React.ReactNode;
  /** Visual de-emphasis only: controls stay editable and Save still writes them. */
  off?: boolean;
  offNote?: string;
  children: React.ReactNode;
}> = ({ title, sub, action, off = false, offNote, children }) => (
  <section className="card">
    <div className="card-head">
      <div>
        <h3>{title}</h3>
        {sub ? <p className="card-sub">{sub}</p> : null}
      </div>
      {action ? <div className="card-tools">{action}</div> : null}
    </div>
    <div className="card-body">
      {off && offNote ? (
        <p className="off-note">
          <Icon name="info" size={11} />
          <span>{offNote}</span>
        </p>
      ) : null}
      <div className={off ? 'off-block' : undefined}>{children}</div>
    </div>
  </section>
);

/** Dimmed sub-form for switches that gate one part of a card. */
const OffBlock: React.FC<{
  off: boolean;
  note: string;
  children: React.ReactNode;
}> = ({ off, note, children }) => {
  if (!off) return <>{children}</>;
  return (
    <div className="off-block">
      <p className="off-note">
        <Icon name="info" size={11} />
        <span>{note}</span>
      </p>
      {children}
    </div>
  );
};

const ConfigView: React.FC<ConfigViewProps> = React.memo(({
  goldThreshold, setGoldThreshold,
  elixirThreshold, setElixirThreshold,
  deThreshold, setDeThreshold,
  searchEnabled, setSearchEnabled,
  upgradeWalls, setUpgradeWalls,
  stallTimer, setStallTimer,
  autoLoot, autoLootBusy, onToggleAutoLoot, onApplyAutoLoot, onResetAutoLoot,
  haltGold, setHaltGold,
  haltElixir, setHaltElixir,
  haltDE, setHaltDE,
  haltRule, setHaltRule,
  storageThreshold, setStorageThreshold,
  resumeThreshold, setResumeThreshold,
  dumpToWallsBeforeHalt, setDumpToWallsBeforeHalt,
  autoCollect, setAutoCollect,
  scheduleEnabled, setScheduleEnabled,
  activeHoursStart, setActiveHoursStart,
  activeHoursEnd, setActiveHoursEnd,
  sessionDuration, setSessionDuration,
  breakDuration, setBreakDuration,
  forceStopOnBreak, setForceStopOnBreak,
  maxPerHour, setMaxPerHour,
  maxPerDay, setMaxPerDay,
  autoBuilders, setAutoBuilders,
  reserveBuilders, setReserveBuilders,
  upgradeStrategy, setUpgradeStrategy,
  upgradeDefenses, setUpgradeDefenses,
  upgradeResources, setUpgradeResources,
  upgradeArmy, setUpgradeArmy,
  upgradeTraps, setUpgradeTraps,
  upgradeHeroes, setUpgradeHeroes,
  maxGoldPerUpgrade, setMaxGoldPerUpgrade,
  maxElixirPerUpgrade, setMaxElixirPerUpgrade,
  maxDEPerUpgrade, setMaxDEPerUpgrade,
  autoLab, setAutoLab,
  labStrategy, setLabStrategy,
  labAllowDE, setLabAllowDE,
  autoPets, setAutoPets,
  petPriorities, setPetPriorities,
  wallResource, setWallResource,
  dryRun, setDryRun,
  onSave,
}) => {
  const [activeTab, setActiveTab] = React.useState<ConfigSubTab>('attack');
  const [saveStatus, setSaveStatus] = React.useState<SaveStatus>('idle');
  const [lastSaveError, setLastSaveError] = React.useState<string | null>(null);
  const savedTimerRef = React.useRef<number | null>(null);
  const errorTimerRef = React.useRef<number | null>(null);

  const allKnownPets = React.useMemo(() => [
    'Phoenix', 'Spirit Fox', 'Sneezy', 'Angry Jelly', 'Frosty', 'Diggy',
    'Greedy Raven', 'Unicorn', 'Electro Owl', 'L.A.S.S.I', 'Mighty Yak', 'Poison Lizard',
  ], []);

  const movePet = (index: number, direction: 'up' | 'down') => {
    const target = direction === 'up' ? index - 1 : index + 1;
    if (target < 0 || target >= petPriorities.length) return;
    const next = [...petPriorities];
    [next[index], next[target]] = [next[target], next[index]];
    setPetPriorities(next);
  };

  const removePet = (pet: string) => setPetPriorities(petPriorities.filter((name) => name !== pet));
  const addPet = (pet: string) => {
    if (!petPriorities.includes(pet)) setPetPriorities([...petPriorities, pet]);
  };

  React.useEffect(() => () => {
    if (savedTimerRef.current) window.clearTimeout(savedTimerRef.current);
    if (errorTimerRef.current) window.clearTimeout(errorTimerRef.current);
  }, []);

  const handleSubmit = async (event?: React.FormEvent) => {
    event?.preventDefault();
    if (saveStatus === 'saving') return;
    if (savedTimerRef.current) { window.clearTimeout(savedTimerRef.current); savedTimerRef.current = null; }
    if (errorTimerRef.current) { window.clearTimeout(errorTimerRef.current); errorTimerRef.current = null; }
    setSaveStatus('saving');
    setLastSaveError(null);
    try {
      await onSave();
      setSaveStatus('saved');
      savedTimerRef.current = window.setTimeout(() => {
        setSaveStatus('idle');
        savedTimerRef.current = null;
      }, 1800);
    } catch (err) {
      setSaveStatus('error');
      setLastSaveError(err instanceof Error ? err.message : String(err));
      errorTimerRef.current = window.setTimeout(() => {
        setSaveStatus('idle');
        setLastSaveError(null);
        errorTimerRef.current = null;
      }, 3000);
    }
  };

  const saveButtonLabel =
    saveStatus === 'saving' ? 'Saving…'
      : saveStatus === 'saved' ? 'Saved'
        : saveStatus === 'error' ? 'Save failed'
          : 'Save settings';

  const saveIcon: IconName =
    saveStatus === 'saving' ? 'refresh'
      : saveStatus === 'saved' ? 'check'
        : saveStatus === 'error' ? 'alert'
          : 'check';

  const saveClass =
    saveStatus === 'saved' ? 'saved'
      : saveStatus === 'error' ? 'failed'
        : '';

  const thresholdItems: {
    label: string;
    value: number;
    setter: (v: number) => void;
    icon: IconName;
    tone: 'gold' | 'elixir' | 'de';
  }[] = [
    { label: 'Min gold', value: goldThreshold, setter: setGoldThreshold, icon: 'coin', tone: 'gold' },
    { label: 'Min elixir', value: elixirThreshold, setter: setElixirThreshold, icon: 'drop', tone: 'elixir' },
    { label: 'Min dark elixir', value: deThreshold, setter: setDeThreshold, icon: 'drop', tone: 'de' },
  ];

  // The engine accepts 0 (disabled) or 10–600; 1–9 would abort instantly.
  const stallInvalid = !Number.isFinite(stallTimer) || stallTimer < 0 || stallTimer > STALL_MAX
    || (stallTimer > 0 && stallTimer < 10);
  const anyInvalid = stallInvalid || thresholdItems.some((item) => invalid(item.value, THRESHOLD_MAX));
  const disabled = saveStatus === 'saving' || anyInvalid;

  const tabs: { id: ConfigSubTab; label: string }[] = [
    { id: 'attack', label: 'Search & loot' },
    { id: 'upgrades', label: 'Builders' },
    { id: 'lab_pets', label: 'Lab & pets' },
    { id: 'schedule', label: 'Schedule' },
    { id: 'storage', label: 'Storage' },
  ];

  return (
    <div className="config-page">
      <div className="page-head">
        <div>
          <h1>Config</h1>
          <p>Search thresholds, builder automation, and pacing for the active profile.</p>
        </div>
      </div>

      <div className="subnav">
        <div className="seg" role="tablist" aria-label="Configuration sections">
          {tabs.map((tab) => (
            <button
              key={tab.id}
              type="button"
              role="tab"
              aria-selected={activeTab === tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={`seg-btn ${activeTab === tab.id ? 'active' : ''}`.trim()}
            >
              {tab.label}
            </button>
          ))}
        </div>
        <button
          type="button"
          onClick={() => void handleSubmit()}
          disabled={disabled}
          aria-label="Save settings"
          data-testid="config-save-btn"
          data-save-state={saveStatus}
          className={`btn primary save-btn ${saveClass}`.trim()}
        >
          {saveButtonLabel}
          <Icon name={saveIcon} size={13} className={saveStatus === 'saving' ? 'spin' : undefined} />
        </button>
      </div>

      <form onSubmit={handleSubmit}>
        <div className="config-stack">
          {activeTab === 'attack' ? (
            <>
              <Section
                title="Search & loot"
                sub="Matchmaking acceptance thresholds and the strategy to execute"
                action={(
                  <Switch
                    checked={searchEnabled}
                    onChange={setSearchEnabled}
                    ariaLabel="Enable search"
                  />
                )}
              >
                <OffBlock
                  off={!searchEnabled}
                  note="Search matching is off — these thresholds are saved but not applied until it is on."
                >
                <div className="threshold-grid">
                  {thresholdItems.map((item) => {
                    const itemInvalid = invalid(item.value, THRESHOLD_MAX);
                    return (
                      <div key={item.label}>
                        <label className="label" htmlFor={`threshold-${item.tone}`}>{item.label}</label>
                        <div className="threshold">
                          <Icon name={item.icon} size={14} className="th-ico" />
                          <input
                            id={`threshold-${item.tone}`}
                            type="number"
                            inputMode="numeric"
                            min={0}
                            max={THRESHOLD_MAX}
                            value={item.value}
                            onChange={(event) => item.setter(parseInt(event.target.value, 10) || 0)}
                            aria-invalid={itemInvalid}
                            className="field"
                          />
                        </div>
                        {itemInvalid
                          ? <p className="hint error" role="alert">Max {THRESHOLD_MAX.toLocaleString()}</p>
                          : <p className="hint">Skip bases below this offer.</p>}
                      </div>
                    );
                  })}
                </div>

                <div className="set-row">
                  <div className="set-label">
                    <b>Auto loot tuning</b>
                    <span>
                      Derive the minimums above from the loot the bot reads on every searched base,
                      optimising loot per hour. Falls back to your numbers until it has enough data.
                    </span>
                  </div>
                  <div className="set-ctl">
                    <Switch
                      checked={autoLoot?.enabled ?? false}
                      onChange={(v) => void onToggleAutoLoot(v)}
                      ariaLabel="Auto loot tuning"
                    />
                  </div>
                </div>

                {autoLoot ? (
                  <div className="autotune" aria-live="polite">
                    <div className="autotune-head">
                      <span className={`status-pill ${autoLoot.ready ? 'running' : ''}`.trim()}>
                        <i />{autoLoot.ready ? 'Tuned' : 'Learning'}
                      </span>
                      <span className="muted mono">
                        {autoLoot.samples}/{autoLoot.recorded} bases read
                      </span>
                    </div>
                    <p className="hint">{autoLoot.reason}</p>
                    {autoLoot.ready ? (
                      <div className="autotune-rows">
                        {autoLoot.advice.map((row) => {
                          const gain = row.per_hour_now > 0
                            ? Math.round((row.per_hour_new / row.per_hour_now - 1) * 100)
                            : 0;
                          return (
                            <div key={row.key} className="autotune-row">
                              <span className="at-label">
                                {row.label}
                                {row.changed && gain > 0 ? <em className="at-gain">+{gain}%</em> : null}
                              </span>
                              <span className="at-values mono">
                                <b>{exactNumber(row.suggested)}</b>
                                <s>{exactNumber(row.current)}</s>
                              </span>
                              {row.per_hour_now > 0 ? (
                                <span className="at-rate mono">
                                  {compactNumber(Math.round(row.per_hour_new))}/h
                                </span>
                              ) : <span className="at-rate" />}
                            </div>
                          );
                        })}
                      </div>
                    ) : null}
                    <div className="autotune-actions">
                      <button
                        type="button"
                        className="btn sm primary"
                        onClick={onApplyAutoLoot}
                        disabled={!autoLoot.ready || autoLootBusy}
                      >
                        Apply suggestion
                      </button>
                      <button
                        type="button"
                        className="btn sm ghost"
                        onClick={onResetAutoLoot}
                        disabled={autoLootBusy}
                        title="Delete the search history and learn again from the current trophy range"
                      >
                        Reset history
                      </button>
                    </div>
                  </div>
                ) : null}
                </OffBlock>

                <div className="set-row">
                  <div className="set-label">
                    <b>Stall timer</b>
                    <span>Abort a stalled battle after 10–600 seconds (0 disables).</span>
                  </div>
                  <div className="set-ctl">
                    <input
                      type="number"
                      inputMode="numeric"
                      min={0}
                      max={STALL_MAX}
                      className="field mono"
                      value={stallTimer}
                      onChange={(event) => setStallTimer(parseInt(event.target.value, 10) || 0)}
                      aria-invalid={stallInvalid}
                      aria-label="Stall timer seconds"
                    />
                  </div>
                </div>
                {stallInvalid ? <p className="hint error" role="alert">Use 0 or 10–{STALL_MAX}s</p> : null}
              </Section>
            </>
          ) : null}

          {activeTab === 'storage' ? (
            <Section
              title="Storage protection"
              sub="Halt attacks before loot overflows; wall dumping is configured under Builders"
            >
              <div className="halt-grid">
                {[
                  { label: 'Halt on gold', detail: 'Stop when gold is full', checked: haltGold, set: setHaltGold },
                  { label: 'Halt on elixir', detail: 'Stop when elixir is full', checked: haltElixir, set: setHaltElixir },
                  { label: 'Halt on dark', detail: 'Stop when DE is full', checked: haltDE, set: setHaltDE },
                ].map((tile) => (
                  <button
                    key={tile.label}
                    type="button"
                    role="switch"
                    aria-checked={tile.checked}
                    onClick={() => tile.set(!tile.checked)}
                    className="halt-tile"
                  >
                    <span>
                      <b>{tile.label}</b>
                      <span>{tile.detail}</span>
                    </span>
                    <span className={`switch ${tile.checked ? 'on' : ''}`.trim()} aria-hidden="true" />
                  </button>
                ))}
              </div>

              <div style={{ marginTop: 14 }}>
                <span className="label">Stop condition</span>
                <div className="option-grid">
                  <button
                    type="button"
                    onClick={() => setHaltRule('ANY')}
                    className={`option-card ${haltRule !== 'ALL' ? 'on' : ''}`.trim()}
                    aria-pressed={haltRule !== 'ALL'}
                  >
                    <div className="oc-top">
                      <b>Stop if any full</b>
                      {haltRule !== 'ALL' ? <Icon name="check" size={13} /> : null}
                    </div>
                    <p>Halt when any enabled storage reaches full capacity.</p>
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setHaltRule('ALL');
                      setHaltGold(true);
                      setHaltElixir(true);
                      setHaltDE(true);
                    }}
                    className={`option-card ${haltRule === 'ALL' ? 'on' : ''}`.trim()}
                    aria-pressed={haltRule === 'ALL'}
                  >
                    <div className="oc-top">
                      <b>Stop only if all three</b>
                      {haltRule === 'ALL' ? <Icon name="check" size={13} /> : null}
                    </div>
                    <p>Keep attacking until gold, elixir, and dark are all full.</p>
                  </button>
                </div>
              </div>

              <div className="set-row" style={{ marginTop: 8 }}>
                <div className="set-label">
                  <b>Full storage threshold</b>
                  <span>Trigger halt when fill level reaches this percentage.</span>
                </div>
                <div className="set-ctl">
                  <input
                    type="number"
                    min={50}
                    max={100}
                    className="field mono"
                    value={storageThreshold}
                    onChange={(event) => {
                      const next = Math.max(50, Math.min(100, parseInt(event.target.value, 10) || 95));
                      setStorageThreshold(next);
                      if (resumeThreshold >= next) setResumeThreshold(Math.max(0, next - 1));
                    }}
                    aria-label="Full storage threshold percent"
                  />
                </div>
              </div>

              <div className="set-row">
                <div className="set-label">
                  <b>Resume threshold</b>
                  <span>Resume attacks once fill drops below this percentage. Must stay under the full threshold.</span>
                </div>
                <div className="set-ctl">
                  <input
                    type="number"
                    min={0}
                    max={Math.max(0, storageThreshold - 1)}
                    className="field mono"
                    value={resumeThreshold}
                    onChange={(event) => {
                      const next = parseInt(event.target.value, 10) || 0;
                      setResumeThreshold(Math.max(0, Math.min(storageThreshold - 1, next)));
                    }}
                    aria-label="Resume attacks threshold percent"
                  />
                </div>
              </div>

              <div className="set-row">
                <div className="set-label">
                  <b>Auto-collect resources</b>
                  <span>Tap mines, collectors, drills, and the cart regularly.</span>
                </div>
                <div className="set-ctl">
                  <Switch checked={autoCollect} onChange={setAutoCollect} ariaLabel="Auto-collect resources" />
                </div>
              </div>

            </Section>
          ) : null}

          {activeTab === 'schedule' ? (
            <Section
              title="Schedule & anti-ban"
              sub="Humanized sessions, natural breaks, and offline simulation"
              off={!scheduleEnabled}
              offNote="The schedule is off — the bot runs continuously. Every value below is saved and applies when you turn it on."
              action={(
                <Switch
                  checked={scheduleEnabled}
                  onChange={setScheduleEnabled}
                  ariaLabel="Enable 24/7 schedule"
                />
              )}
            >
                <div className="form-grid">
                  <div className="form-section" style={{ marginTop: 0 }}>
                    <div className="form-section-title"><Icon name="clock" size={13} />Active hours</div>
                    <div className="form-grid">
                      <div className="f-field">
                        <label className="label" htmlFor="hours-start">Start</label>
                        <input
                          id="hours-start"
                          type="time"
                          className="field"
                          value={activeHoursStart}
                          onChange={(event) => setActiveHoursStart(event.target.value)}
                        />
                      </div>
                      <div className="f-field">
                        <label className="label" htmlFor="hours-end">End</label>
                        <input
                          id="hours-end"
                          type="time"
                          className="field"
                          value={activeHoursEnd}
                          onChange={(event) => setActiveHoursEnd(event.target.value)}
                        />
                      </div>
                    </div>
                  </div>

                  <div className="form-section" style={{ marginTop: 0 }}>
                    <div className="form-section-title"><Icon name="timer" size={13} />Session / break</div>
                    <div className="form-grid">
                      <div className="f-field">
                        <label className="label" htmlFor="session-duration">Session (min)</label>
                        <input
                          id="session-duration"
                          type="number"
                          min={10}
                          max={360}
                          className="field mono"
                          value={sessionDuration}
                          onChange={(event) => setSessionDuration(parseInt(event.target.value, 10) || 60)}
                        />
                      </div>
                      <div className="f-field">
                        <label className="label" htmlFor="break-duration">Break (min)</label>
                        <input
                          id="break-duration"
                          type="number"
                          min={5}
                          max={180}
                          className="field mono"
                          value={breakDuration}
                          onChange={(event) => setBreakDuration(parseInt(event.target.value, 10) || 20)}
                        />
                      </div>
                    </div>
                  </div>
                </div>

                <div className="set-row">
                  <div className="set-label">
                    <b>Max attacks per hour</b>
                    <span>Hard ceiling on search-and-attack cycles.</span>
                  </div>
                  <div className="set-ctl">
                    <input
                      type="number"
                      min={1}
                      max={60}
                      className="field mono"
                      value={maxPerHour}
                      aria-label="Max attacks per hour"
                      onChange={(event) => setMaxPerHour(parseInt(event.target.value, 10) || 6)}
                    />
                  </div>
                </div>

                <div className="set-row">
                  <div className="set-label">
                    <b>Max attacks per day</b>
                    <span>Daily ceiling across the active profile.</span>
                  </div>
                  <div className="set-ctl">
                    <input
                      type="number"
                      min={1}
                      max={300}
                      className="field mono"
                      value={maxPerDay}
                      aria-label="Max attacks per day"
                      onChange={(event) => setMaxPerDay(parseInt(event.target.value, 10) || 40)}
                    />
                  </div>
                </div>

                <div className="set-row">
                  <div className="set-label">
                    <b>Offline simulation</b>
                    <span>Stop the game during breaks so the account looks human.</span>
                  </div>
                  <div className="set-ctl">
                    <Switch
                      checked={forceStopOnBreak}
                      onChange={setForceStopOnBreak}
                      ariaLabel="Offline simulation"
                    />
                  </div>
                </div>
            </Section>
          ) : null}

          {activeTab === 'upgrades' ? (
            <Section
              title="Village builders & upgrades"
              sub="Builder assignment, hero priority, spend limits, and walls"
              off={!autoBuilders}
              offNote="Automatic builders are off — nothing below runs, but every value is saved for when you enable it."
              action={(
                <Switch
                  checked={autoBuilders}
                  onChange={setAutoBuilders}
                  ariaLabel="Enable automatic builders"
                />
              )}
            >
                <div className="form-grid">
                  <div className="f-field">
                    <label className="label" htmlFor="upgrade-strategy">Upgrade priority</label>
                    <select
                      id="upgrade-strategy"
                      className="field select"
                      value={upgradeStrategy}
                      onChange={(event) => setUpgradeStrategy(event.target.value)}
                    >
                      <option value="strategic_rush">Strategic rush (recommended)</option>
                      <option value="max">Max everything</option>
                      <option value="heroes_first">Heroes first</option>
                      <option value="cheapest_first">Cheapest first</option>
                      <option value="most_expensive_first">Most expensive first</option>
                      <option value="balanced">Balanced / in-game suggestions</option>
                    </select>
                    <p className="hint">Strategic rush: heroes → army capacity → resources → defenses. Max: biggest upgrade first.</p>
                  </div>
                  <div className="f-field">
                    <label className="label" htmlFor="reserve-builders">Reserve builders for walls</label>
                    <input
                      id="reserve-builders"
                      type="number"
                      min={0}
                      max={5}
                      className="field mono"
                      value={reserveBuilders}
                      onChange={(event) => setReserveBuilders(Math.max(0, Math.min(5, parseInt(event.target.value, 10) || 0)))}
                    />
                    <p className="hint">Always keeps {reserveBuilders} builder{reserveBuilders === 1 ? '' : 's'} free.</p>
                  </div>
                </div>

                <div style={{ marginTop: 14 }}>
                  <span className="label">Allowed building categories</span>
                  <div className="cat-grid">
                    {[
                      { label: 'Heroes', val: upgradeHeroes, set: setUpgradeHeroes },
                      { label: 'Defenses', val: upgradeDefenses, set: setUpgradeDefenses },
                      { label: 'Resources', val: upgradeResources, set: setUpgradeResources },
                      { label: 'Army buildings', val: upgradeArmy, set: setUpgradeArmy },
                      { label: 'Traps', val: upgradeTraps, set: setUpgradeTraps },
                    ].map((category) => (
                      <button
                        key={category.label}
                        type="button"
                        onClick={() => category.set(!category.val)}
                        className={`cat-btn ${category.val ? 'on' : ''}`.trim()}
                        aria-pressed={category.val}
                      >
                        <Icon name={category.val ? 'check' : 'plus'} size={11} />
                        {category.label}
                      </button>
                    ))}
                  </div>
                </div>

                <div style={{ marginTop: 16 }}>
                  <span className="label">Max cost per upgrade (0 = unlimited)</span>
                  <div className="threshold-grid">
                    <div>
                      <label className="label" htmlFor="max-gold">Gold</label>
                      <input
                        id="max-gold"
                        type="number"
                        className="field mono"
                        value={maxGoldPerUpgrade}
                        onChange={(event) => setMaxGoldPerUpgrade(parseInt(event.target.value, 10) || 0)}
                      />
                    </div>
                    <div>
                      <label className="label" htmlFor="max-elixir">Elixir</label>
                      <input
                        id="max-elixir"
                        type="number"
                        className="field mono"
                        value={maxElixirPerUpgrade}
                        onChange={(event) => setMaxElixirPerUpgrade(parseInt(event.target.value, 10) || 0)}
                      />
                    </div>
                    <div>
                      <label className="label" htmlFor="max-de">Dark elixir</label>
                      <input
                        id="max-de"
                        type="number"
                        className="field mono"
                        value={maxDEPerUpgrade}
                        onChange={(event) => setMaxDEPerUpgrade(parseInt(event.target.value, 10) || 0)}
                      />
                    </div>
                  </div>
                </div>

                <div className="set-row" style={{ marginTop: 8 }}>
                  <div className="set-label">
                    <b>Dry run / simulation</b>
                    <span>Inspect upgrade dialogs without spending resources or gems.</span>
                  </div>
                  <div className="set-ctl">
                    <Switch checked={dryRun} onChange={setDryRun} ariaLabel="Dry run" />
                  </div>
                </div>

              <div className="form-section">
                <div className="form-section-title"><Icon name="layers" size={13} />Walls</div>
                <div className="set-row" style={{ paddingTop: 0 }}>
                  <div className="set-label">
                    <b>Auto upgrade walls</b>
                    <span>Spend excess gold and elixir on walls, including before a storage halt.</span>
                  </div>
                  <div className="set-ctl">
                    <Switch
                      checked={upgradeWalls}
                      onChange={(next) => {
                        setUpgradeWalls(next);
                        setDumpToWallsBeforeHalt(next);
                      }}
                      ariaLabel="Auto upgrade walls"
                    />
                  </div>
                </div>
                <div className="set-row" style={{ borderBottom: 0 }}>
                  <div className="set-label">
                    <label className="label" htmlFor="wall-resource">Wall resource</label>
                    <span>Which loot the wall loop spends.</span>
                  </div>
                  <div className="set-ctl">
                    <select
                      id="wall-resource"
                      className="field select"
                      value={wallResource}
                      onChange={(event) => setWallResource(event.target.value)}
                    >
                      <option value="both">Gold &amp; elixir</option>
                      <option value="gold">Gold only</option>
                      <option value="elixir">Elixir only</option>
                    </select>
                  </div>
                </div>
                <p className="hint">
                  Reserve builders for walls above; the wall loop uses them instead of starting another building.
                </p>
              </div>
            </Section>
          ) : null}

          {activeTab === 'lab_pets' ? (
            <>
              <Section
                title="Laboratory research"
                sub="Automatic troop, spell, and siege upgrades"
                off={!autoLab}
                offNote="Automatic research is off — the order and DE permission below are saved for when you enable it."
                action={<Switch checked={autoLab} onChange={setAutoLab} ariaLabel="Enable automatic lab research" />}
              >
                  <div className="form-grid">
                    <div className="f-field">
                      <label className="label" htmlFor="lab-strategy">Research priority</label>
                      <select
                        id="lab-strategy"
                        className="field select"
                        value={labStrategy}
                        onChange={(event) => setLabStrategy(event.target.value)}
                      >
                        <option value="recommended">Recommended (TH19 army-first)</option>
                        <option value="cheapest_first">Cheapest first</option>
                        <option value="spells_first">Spells first</option>
                        <option value="troops_first">Troops first</option>
                        <option value="siege_first">Siege machines first</option>
                      </select>
                    </div>
                    <div className="f-field">
                      <label className="label">Dark elixir permission</label>
                      <div className="set-row" style={{ padding: 0, border: 0 }}>
                        <div className="set-label">
                          <b>Allow DE research</b>
                          <span>Spend dark elixir on lab upgrades.</span>
                        </div>
                        <Switch checked={labAllowDE} onChange={setLabAllowDE} ariaLabel="Allow dark elixir research" />
                      </div>
                    </div>
                  </div>
              </Section>

              <Section
                title="Hero pets"
                sub="Upgrade order in the pet house"
                off={!autoPets}
                offNote="Automatic pet upgrades are off — this queue is saved and resumes when you enable it."
                action={<Switch checked={autoPets} onChange={setAutoPets} ariaLabel="Enable automatic pet upgrades" />}
              >
                  <span className="label">Priority order (top to bottom)</span>
                  {petPriorities.length === 0 ? (
                    <p className="hint">No pets in the queue. Add one below.</p>
                  ) : (
                    petPriorities.map((pet, index) => (
                      <div key={pet} className="pet-row">
                        <span className="pet-rank">#{index + 1}</span>
                        <span className="pet-name">{pet}</span>
                        <div className="pet-actions">
                          <button
                            type="button"
                            disabled={index === 0}
                            onClick={() => movePet(index, 'up')}
                            className="icon-btn sm"
                            aria-label={`Move ${pet} up`}
                            title="Move up"
                          >
                            <Icon name="arrowUp" size={12} />
                          </button>
                          <button
                            type="button"
                            disabled={index === petPriorities.length - 1}
                            onClick={() => movePet(index, 'down')}
                            className="icon-btn sm"
                            aria-label={`Move ${pet} down`}
                            title="Move down"
                          >
                            <Icon name="arrowDown" size={12} />
                          </button>
                          <button
                            type="button"
                            onClick={() => removePet(pet)}
                            className="icon-btn sm"
                            aria-label={`Remove ${pet}`}
                            title="Remove"
                          >
                            <Icon name="x" size={12} />
                          </button>
                        </div>
                      </div>
                    ))
                  )}

                  <div style={{ marginTop: 14 }}>
                    <span className="label">Add pet to queue</span>
                    <div className="pet-add">
                      {allKnownPets.filter((pet) => !petPriorities.includes(pet)).map((pet) => (
                        <button
                          key={pet}
                          type="button"
                          onClick={() => addPet(pet)}
                          className="cat-btn"
                        >
                          <Icon name="plus" size={11} />
                          {pet}
                        </button>
                      ))}
                      {allKnownPets.every((pet) => petPriorities.includes(pet)) ? (
                        <span className="muted">All pets are already queued.</span>
                      ) : null}
                    </div>
                  </div>
              </Section>
            </>
          ) : null}
        </div>

        {(anyInvalid || (lastSaveError && saveStatus === 'error')) ? (
          <div className="config-foot" style={{ marginTop: 16 }}>
            {anyInvalid ? (
              <div className="form-warn" role="alert">
                Some values are out of range — fix them before saving.
              </div>
            ) : null}
            {lastSaveError && saveStatus === 'error' ? (
              <div className="form-alert" role="status" aria-live="polite" title={lastSaveError}>
                Save failed: {lastSaveError.length > 90 ? `${lastSaveError.slice(0, 87)}…` : lastSaveError}
              </div>
            ) : null}
          </div>
        ) : null}
      </form>
    </div>
  );
});

ConfigView.displayName = 'ConfigView';

export default ConfigView;
