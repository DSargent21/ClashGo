import React from 'react';
import type { ArmyPlan } from '../armyApi';
import Icon from './ui/Icon';
import UnitIcon from './ui/UnitIcon';
import { SwitchRow } from './ui/Switch';
import { compactNumber, formatWindow } from '../utils';

interface ArmyViewProps {
  plans: ArmyPlan[];
  loading: boolean;
  error: string | null;
  onSetSlot: (file: string, slot: number) => void;
  onSetAutoEnd: (file: string, enabled: boolean) => void;
  onActivate: (file: string) => Promise<void>;
}

const NO_RAIDS = 'No raids yet';

const rateSource = (plan: ArmyPlan) =>
  plan.measured ? 'Measured rate' : 'No estimate yet';

const measuredWindow = (plan: ArmyPlan) =>
  plan.window_minutes > 0
    ? `${plan.raids} raids · ${formatWindow(plan.window_minutes)}`
    : '';

const unitLines = (plan: ArmyPlan) =>
  plan.phases.reduce((n, phase) => n + phase.units.length, 0);

const StatStrip: React.FC<{ plan: ArmyPlan }> = ({ plan }) => (
  <div className="army-stat-strip">
    <div className="army-stat">
      <span>Loot / hour</span>
      {plan.loot_per_hour > 0 ? (
        <>
          <b>{compactNumber(plan.loot_per_hour)}<small>/h</small></b>
          {measuredWindow(plan) ? <span className="army-stat-note">{measuredWindow(plan)}</span> : null}
        </>
      ) : (
        <b className="army-stat-empty">{NO_RAIDS}</b>
      )}
    </div>
    <div className="army-stat">
      <span>Recipe slot</span>
      <b>Slot {plan.army_slot}</b>
    </div>
    <div className="army-stat">
      <span>Target edge</span>
      <b>{plan.target_edge || '—'}</b>
    </div>
    <div className="army-stat">
      <span>Auto end</span>
      <b>{plan.end_at_percent > 0 ? `${plan.end_at_percent}%` : 'Off'}</b>
    </div>
  </div>
);

const SlotControl: React.FC<{
  plan: ArmyPlan;
  onSetSlot: (file: string, slot: number) => void;
}> = ({ plan, onSetSlot }) => (
  <>
    <div className="army-control-label">
      <b>Saved recipe slot</b>
      <span>The in-game Saved Recipes entry the bot arms for this plan.</span>
    </div>
    <div className="army-slot-picker" role="group" aria-label="Saved recipe slot">
      {[1, 2, 3, 4].map((slot) => (
        <button
          key={slot}
          type="button"
          className={`army-slot-btn ${plan.army_slot === slot ? 'active' : ''}`.trim()}
          aria-label={`Slot ${slot}`}
          aria-pressed={plan.army_slot === slot}
          onClick={() => onSetSlot(plan.file, slot)}
        >
          {plan.army_slot === slot ? <Icon name="check" size={11} /> : null}
          {slot}
        </button>
      ))}
    </div>
  </>
);

const AutoEndControl: React.FC<{
  plan: ArmyPlan;
  onSetAutoEnd: (file: string, enabled: boolean) => void;
}> = ({ plan, onSetAutoEnd }) => (
  <div className="army-toggle-row">
    <SwitchRow
      checked={plan.end_at_percent > 0}
      onChange={(enabled) => onSetAutoEnd(plan.file, enabled)}
      label="Auto-end battle"
      description={
        plan.end_at_percent > 0
          ? `Surrender once the win condition is secured (${plan.end_at_percent}%).`
          : 'Surrender once the win condition is secured.'
      }
    />
  </div>
);

const UnitChips: React.FC<{ units: ArmyPlan['phases'][number]['units'] }> = ({ units }) => (
  <div className="army-unit-chips">
    {units.map((unit, unitIndex) => (
      <span
        key={`${unit.name}-${unitIndex}`}
        className="army-unit-chip"
        title={`${unit.name} · ${unit.amount === 'All' ? 'all available' : unit.amount}`}
      >
        <UnitIcon name={unit.name} size={13} />
        <span>{unit.name}</span>
        <b>{unit.amount === 'All' ? 'all' : `×${unit.amount}`}</b>
      </span>
    ))}
  </div>
);

const SequenceLines: React.FC<{ plan: ArmyPlan }> = ({ plan }) =>
  plan.phases.length === 0 ? (
    <p className="army-empty-note">This strategy file declares no deploy phases.</p>
  ) : (
    <div className="army-seq-lines">
      {plan.phases.map((phase, phaseIndex) => (
        <div key={`${phase.name}-${phaseIndex}`} className="army-seq-line">
          <div className="army-seq-head">
            <span className="phase-num">{phaseIndex + 1}</span>
            <b>{phase.name}</b>
            <span className="army-seq-count mono">{phase.units.length} units</span>
          </div>
          <UnitChips units={phase.units} />
        </div>
      ))}
    </div>
  );

const ArmyView: React.FC<ArmyViewProps> = React.memo(({
  plans,
  loading,
  error,
  onSetSlot,
  onSetAutoEnd,
  onActivate,
}) => {
  const activePlan = plans.find((plan) => plan.active) ?? null;
  const [selectedFile, setSelectedFile] = React.useState<string | null>(null);
  const [activating, setActivating] = React.useState<string | null>(null);

  React.useEffect(() => {
    setSelectedFile((prev) => {
      if (prev && plans.some((plan) => plan.file === prev)) return prev;
      return activePlan?.file ?? plans[0]?.file ?? null;
    });
  }, [plans, activePlan]);

  const selected = plans.find((plan) => plan.file === selectedFile) ?? null;

  const activate = async (file: string) => {
    setActivating(file);
    try {
      await onActivate(file);
      setSelectedFile(file);
    } catch {
      // The parent stores the exact save/refresh error for the banner.
    } finally {
      setActivating(null);
    }
  };

  return (
    <div className="army-page">
      <div className="page-head army-head">
        <div>
          <span className="army-eyebrow">Armed loadouts</span>
          <h1>Army</h1>
          <p>The plan the bot arms and deploys, plus the saved recipe it loads before every attack.</p>
        </div>
        <div className="page-side army-head-side">
          <div className="army-head-stat">
            <span>Active plan</span>
            <b>{activePlan?.name ?? 'None'}</b>
          </div>
        </div>
      </div>

      {error ? (
        <div className="error-banner" style={{ marginBottom: 16 }}>
          <Icon name="alert" size={14} />
          <span>{error}</span>
        </div>
      ) : null}

      {loading && plans.length === 0 ? (
        <div className="empty">
          <span className="empty-ico"><Icon name="layers" size={20} /></span>
          <b>Loading army plans</b>
          <p>Reading the strategy files and active config.</p>
        </div>
      ) : plans.length === 0 ? (
        <div className="empty">
          <span className="empty-ico"><Icon name="layers" size={20} /></span>
          <b>No strategy files found</b>
          <p>Add a YAML strategy under assets/strategies, then reopen this page.</p>
        </div>
      ) : (
        <div className="army-gallery-design">
          <div className="army-gallery-head">
            <div>
              <h2>Choose a plan</h2>
              <p>The active plan is armed automatically. Preview any plan, then make it active.</p>
            </div>
            <span className="section-meta mono">{plans.length} available</span>
          </div>

          <div className="army-gallery" role="radiogroup" aria-label="Army plans">
            {plans.map((plan) => {
              const isSelected = plan.file === selected?.file;
              return (
                <button
                  key={plan.file}
                  type="button"
                  role="radio"
                  aria-checked={isSelected}
                  className={`army-card ${isSelected ? 'selected' : ''}`.trim()}
                  onClick={() => setSelectedFile(plan.file)}
                >
                  <span className="plan-head">
                    <span className="plan-emblem"><UnitIcon name={plan.emblem} size={20} /></span>
                    <span className="plan-title">
                      <b>{plan.name}</b>
                      <span>{rateSource(plan)}</span>
                    </span>
                    {plan.active ? <span className="army-tab-live">LIVE</span> : null}
                    <span className="plan-select" aria-hidden="true">
                      {isSelected ? <Icon name="check" size={10} /> : null}
                    </span>
                  </span>
                  {plan.loot_per_hour > 0 ? (
                    <span className="plan-rate">
                      <b className="mono">{compactNumber(plan.loot_per_hour)}</b>
                      <span>loot / hour</span>
                      {measuredWindow(plan) ? <span className="plan-window">{measuredWindow(plan)}</span> : null}
                    </span>
                  ) : (
                    <span className="plan-rate plan-rate-empty">{NO_RAIDS}</span>
                  )}
                  <span className="plan-pitch">{plan.description}</span>
                  <span className="plan-foot">
                    <span>Slot {plan.army_slot}</span>
                    <span>{plan.phases.length} phases</span>
                    {plan.end_at_percent > 0 ? <span>Ends at {plan.end_at_percent}%</span> : null}
                  </span>
                </button>
              );
            })}
          </div>

          {selected ? (
            <section className="army-focus" aria-label={`${selected.name} details`}>
              <header className="army-focus-head">
                <div className="army-focus-title">
                  <div className="army-detail-name">
                    <h2>{selected.name}</h2>
                    {selected.active
                      ? <span className="army-live-pill"><i />Active</span>
                      : <span className="army-ref-pill">Reference</span>}
                  </div>
                  {!selected.active ? (
                    <button
                      type="button"
                      className="btn primary army-use-btn"
                      onClick={() => void activate(selected.file)}
                      disabled={activating !== null}
                    >
                      <Icon name="play" size={12} />
                      {activating === selected.file ? 'Switching…' : 'Use this army'}
                    </button>
                  ) : null}
                </div>
                <StatStrip plan={selected} />
              </header>

              <div className="army-arming-bar">
                <div className="army-arming-cell">
                  <SlotControl plan={selected} onSetSlot={onSetSlot} />
                </div>
                <div className="army-arming-cell">
                  <AutoEndControl plan={selected} onSetAutoEnd={onSetAutoEnd} />
                </div>
              </div>

              <div className="army-focus-body">
                <header className="section-head">
                  <div>
                    <h2>Deployment sequence</h2>
                    <p>Phases run top to bottom on the target edge.</p>
                  </div>
                  <span className="section-meta mono">
                    {selected.phases.length} phases · {unitLines(selected)} unit lines
                  </span>
                </header>
                <SequenceLines plan={selected} />
              </div>
            </section>
          ) : null}
        </div>
      )}
    </div>
  );
});

ArmyView.displayName = 'ArmyView';

export default ArmyView;
