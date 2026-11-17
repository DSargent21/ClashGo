import React from 'react';
import type { ArmyPlan } from '../armyApi';
import { compactNumber } from '../utils';
import Icon from './ui/Icon';
import UnitIcon from './ui/UnitIcon';
import { SwitchRow } from './ui/Switch';

interface ArmyViewProps {
  plans: ArmyPlan[];
  loading: boolean;
  error: string | null;
  armySlot: number;
  autoEnd: boolean;
  onSetSlot: (slot: number) => void;
  onSetAutoEnd: (enabled: boolean) => void;
  onActivate: (file: string) => Promise<void>;
}

// The same four tones the loadout uses in the setup wizard, so a unit reads
// as a troop/spell/siege/hero on every surface.
const unitKind = (name: string): 'troop' | 'spell' | 'siege' | 'hero' => {
  const lower = name.toLowerCase();
  if (/(spell|freeze|poison|haste|recall|invisibility|overgrowth|revive)/.test(lower)) return 'spell';
  if (/(slammer|blimp|barracks|workshop|log launcher|battle drill|flame flinger|siege)/.test(lower)) return 'siege';
  if (/(king|queen|warden|champion|prince|duke|minion prince)/.test(lower)) return 'hero';
  return 'troop';
};

const estimateLabel = (plan: ArmyPlan): string => {
  if (plan.measured) return `Measured · ${plan.raids} raids`;
  if (plan.loot_per_hour > 0) return 'First-launch estimate';
  return 'No history yet';
};

const ArmyPlanCard: React.FC<{
  plan: ArmyPlan;
  active: boolean;
  busy: boolean;
  armySlot: number;
  autoEnd: boolean;
  onSetSlot: (slot: number) => void;
  onSetAutoEnd: (enabled: boolean) => void;
  onActivate: (file: string) => void;
}> = ({ plan, active, busy, armySlot, autoEnd, onSetSlot, onSetAutoEnd, onActivate }) => (
  <article className={`card army-plan ${active ? 'active' : ''}`.trim()}>
    <div className="army-plan-head">
      <span className="army-emblem"><UnitIcon name={plan.emblem} size={21} /></span>
      <div className="army-plan-title">
        <h3>{plan.name}</h3>
        <span>{plan.phases.length} phases · {plan.target_edge || 'no target edge'}</span>
      </div>
      <span className={`tag ${active ? 'gold' : ''}`.trim()}>{active ? 'Active' : 'Reference'}</span>
    </div>

    <div className="army-plan-rate">
      <div className="apr-main">
        <span>Estimated loot / hour</span>
        <b>{plan.loot_per_hour > 0 ? compactNumber(plan.loot_per_hour) : '—'}</b>
      </div>
      <span className={`apr-basis ${plan.measured ? 'measured' : ''}`.trim()}>
        {plan.measured ? <Icon name="activity" size={11} /> : <Icon name="info" size={11} />}
        {estimateLabel(plan)}
      </span>
    </div>

    <p className="army-plan-summary" title={plan.description}>{plan.description}</p>

    <div className="army-plan-facts">
      <div><span>Recipe slot</span><b>Slot {active ? armySlot : plan.army_slot}</b></div>
      <div><span>Target edge</span><b>{plan.target_edge || '—'}</b></div>
      <div>
        <span>Auto end</span>
        <b>{(active ? autoEnd : plan.end_at_percent > 0) ? (plan.end_at_percent > 0 ? `${plan.end_at_percent}%` : 'On') : 'Off'}</b>
      </div>
    </div>

    <div className="army-phases">
      {plan.phases.map((phase, phaseIndex) => (
        <div key={`${phase.name}-${phaseIndex}`} className="army-phase">
          <div className="army-phase-title">
            <span className="phase-num">{phaseIndex + 1}</span>
            <b>{phase.name}</b>
            <span className="phase-count">{phase.units.length} {phase.units.length === 1 ? 'unit' : 'units'}</span>
          </div>
          <div className="army-chips">
            {phase.units.map((unit, unitIndex) => {
              const kind = unitKind(unit.name);
              return (
                <span
                  key={`${unit.name}-${unitIndex}`}
                  className={`army-chip ${kind}`}
                  title={`${unit.name} · ${unit.amount === 'All' ? 'all available' : unit.amount}`}
                >
                  <UnitIcon name={unit.name} size={13} />
                  <span>{unit.name}</span>
                  <b>{unit.amount === 'All' ? 'all' : `×${unit.amount}`}</b>
                </span>
              );
            })}
          </div>
        </div>
      ))}
    </div>

    {active ? (
      <div className="army-plan-slot">
        <span className="label">Saved recipe slot</span>
        <div className="slot-grid">
          {[1, 2, 3, 4].map((candidate) => (
            <button
              key={candidate}
              type="button"
              onClick={() => onSetSlot(candidate)}
              className={`slot-btn ${armySlot === candidate ? 'active' : ''}`.trim()}
              aria-pressed={armySlot === candidate}
            >
              {armySlot === candidate ? <Icon name="check" size={12} /> : <Icon name="layers" size={12} />}
              Slot {candidate}
            </button>
          ))}
        </div>
        <SwitchRow
          checked={autoEnd}
          onChange={onSetAutoEnd}
          label="Auto-end battle"
          description="Surrender once the plan's win condition is secured, saving time and troops."
        />
        <p className="hint">Stored in config.json and armed before this strategy starts.</p>
      </div>
    ) : (
      <div className="army-plan-action">
        <button
          type="button"
          className="btn block"
          onClick={() => onActivate(plan.file)}
          disabled={busy}
        >
          <Icon name="play" size={12} />
          {busy ? 'Switching…' : 'Use this army'}
        </button>
      </div>
    )}
  </article>
);

const ArmyView: React.FC<ArmyViewProps> = React.memo(({
  plans,
  loading,
  error,
  armySlot,
  autoEnd,
  onSetSlot,
  onSetAutoEnd,
  onActivate,
}) => {
  const [activating, setActivating] = React.useState<string | null>(null);

  const activate = async (file: string) => {
    setActivating(file);
    try {
      await onActivate(file);
    } catch {
      // The parent stores the exact save/refresh error for the banner.
    } finally {
      setActivating(null);
    }
  };

  return (
    <div className="army-page">
      <div className="page-head">
        <div>
          <h1>Army</h1>
          <p>Live deployment plans from assets/strategies, with the slots the bot will actually arm.</p>
        </div>
        <div className="page-side">
          <span className="tag"><Icon name="target" size={11} />Active plan drives Config</span>
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
        <>
          <div className="army-deck">
            {plans.map((plan) => (
              <ArmyPlanCard
                key={plan.file}
                plan={plan}
                active={plan.active}
                busy={activating === plan.file}
                armySlot={armySlot}
                autoEnd={autoEnd}
                onSetSlot={onSetSlot}
                onSetAutoEnd={onSetAutoEnd}
                onActivate={(file) => void activate(file)}
              />
            ))}
          </div>
          <p className="army-estimate-note">
            <Icon name="info" size={12} />
            Loot per hour comes from your own raid history once 8 matching raids exist; until then it is the
            first-launch estimate. It is the same number the setup wizard ranks armies with.
          </p>
        </>
      )}
    </div>
  );
});

ArmyView.displayName = 'ArmyView';

export default ArmyView;
