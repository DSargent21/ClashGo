import React from 'react';
import Icon, { IconName } from './Icon';

export type StatTone = 'neutral' | 'gold' | 'elixir' | 'de' | 'green' | 'blue';

export interface StatCardProps {
  icon: IconName;
  label: React.ReactNode;
  value: React.ReactNode;
  detail?: React.ReactNode;
  tone?: StatTone;
}

const StatCard: React.FC<StatCardProps> = React.memo(({
  icon,
  label,
  value,
  detail,
  tone = 'neutral',
}) => (
  <article className="card stat-card">
    <span className={`stat-ico ${tone === 'neutral' ? '' : tone}`.trim()}>
      <Icon name={icon} size={16} />
    </span>
    <div className="stat-body">
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {detail ? <div className="stat-detail">{detail}</div> : null}
    </div>
  </article>
));

StatCard.displayName = 'StatCard';

export default StatCard;
