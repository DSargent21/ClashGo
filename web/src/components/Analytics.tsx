import React from 'react';
import { AttackReport, BotStats } from '../types';
import { compactNumber, exactNumber, strategyLabel } from '../utils';
import Icon from './ui/Icon';

interface AnalyticsProps {
  stats: BotStats;
  history: AttackReport[];
  activityHistory: AttackReport[];
}

const ACTIVITY_WEEKS = 52;

const pad2 = (value: number): string => String(value).padStart(2, '0');

const dayKey = (date: Date): string =>
  `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}`;

const dateLabel = (date: Date): string =>
  date.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });

interface ActivityDay {
  date: Date;
  key: string;
  count: number;
  level: number;
  future: boolean;
}

interface ActivityData {
  days: ActivityDay[];
  total: number;
  activeDays: number;
  best: number;
  streak: number;
  monthLabels: string[];
}

const buildActivity = (history: AttackReport[]): ActivityData => {
  const counts = new Map<string, number>();
  history.forEach((report) => {
    const date = new Date(report.timestamp);
    if (Number.isNaN(date.getTime())) return;
    const key = dayKey(date);
    counts.set(key, (counts.get(key) ?? 0) + 1);
  });

  const today = new Date();
  today.setHours(0, 0, 0, 0);
  const weekStart = new Date(today);
  weekStart.setDate(today.getDate() - today.getDay());
  const start = new Date(weekStart);
  start.setDate(weekStart.getDate() - (ACTIVITY_WEEKS - 1) * 7);

  const raw: { date: Date; key: string; count: number; future: boolean }[] = [];
  for (let index = 0; index < ACTIVITY_WEEKS * 7; index += 1) {
    const date = new Date(start);
    date.setDate(start.getDate() + index);
    raw.push({
      date,
      key: dayKey(date),
      count: counts.get(dayKey(date)) ?? 0,
      future: date.getTime() > today.getTime(),
    });
  }

  const max = Math.max(1, ...raw.map((day) => day.count));
  const days: ActivityDay[] = raw.map((day) => ({
    ...day,
    level: day.future || day.count === 0 ? 0 : Math.min(4, Math.ceil((day.count / max) * 4)),
  }));
  const past = days.filter((day) => !day.future);

  let streak = 0;
  for (let index = past.length - 1; index >= 0; index -= 1) {
    if (past[index].count === 0) break;
    streak += 1;
  }

  const monthLabels: string[] = [];
  let lastMonth = -1;
  for (let week = 0; week < ACTIVITY_WEEKS; week += 1) {
    const date = new Date(start);
    date.setDate(start.getDate() + week * 7);
    const month = date.getMonth();
    monthLabels.push(month !== lastMonth ? date.toLocaleDateString('en-US', { month: 'short' }) : '');
    lastMonth = month;
  }

  return {
    days,
    total: past.reduce((sum, day) => sum + day.count, 0),
    activeDays: past.filter((day) => day.count > 0).length,
    best: Math.max(0, ...past.map((day) => day.count)),
    streak,
    monthLabels,
  };
};

interface StrategyRow {
  label: string;
  attacks: number;
  avgStars: number;
  threeStarRate: number;
  lootPerAttack: number;
  deployRate: number;
}

const CHART_SERIES = [
  { key: 'gold' as const, label: 'Gold', color: 'var(--gold)', scale: 1 },
  { key: 'elixir' as const, label: 'Elixir', color: 'var(--elixir)', scale: 1 },
  { key: 'de' as const, label: 'Dark ×40', color: 'var(--de)', scale: 40 },
] as const;

type ChartSeriesKey = typeof CHART_SERIES[number]['key'];

const LootChart: React.FC<{ history: AttackReport[] }> = ({ history }) => {
  const [visible, setVisible] = React.useState<Record<ChartSeriesKey, boolean>>({
    gold: true,
    elixir: true,
    de: true,
  });

  const reports = React.useMemo(() => history.slice(0, 16).reverse(), [history]);
  const width = 760;
  const height = 110;
  const left = 46;
  const right = 12;
  const top = 8;
  const bottom = 20;
  const plotWidth = width - left - right;
  const plotHeight = height - top - bottom;
  const totals = reports.map((report) => CHART_SERIES.reduce(
    (sum, series) => sum + (visible[series.key] ? report[
      series.key === 'gold' ? 'gold_stolen' : series.key === 'elixir' ? 'elixir_stolen' : 'dark_elixir_stolen'
    ] * series.scale : 0),
    0,
  ));
  const max = Math.max(1, ...totals);
  const slot = plotWidth / Math.max(1, reports.length);
  const barWidth = Math.min(26, slot * 0.54);
  const yFor = (value: number) => top + plotHeight - (value / max) * plotHeight;

  const toggleSeries = (key: ChartSeriesKey) => {
    setVisible((current) => {
      if (current[key] && Object.values(current).filter(Boolean).length === 1) return current;
      return { ...current, [key]: !current[key] };
    });
  };

  return (
    <section className="card chart-card">
      <div className="card-head">
        <div>
          <h3>Loot per raid</h3>
          <p className="card-sub">Stacked resource yield · last 16 reported raids</p>
        </div>
        <div className="chart-legend" role="group" aria-label="Chart series">
          {CHART_SERIES.map((series) => (
            <button
              key={series.key}
              type="button"
              className={`chart-legend-chip ${visible[series.key] ? '' : 'off'}`.trim()}
              onClick={() => toggleSeries(series.key)}
              aria-pressed={visible[series.key]}
            >
              <i style={{ background: series.color }} />
              {series.label}
            </button>
          ))}
        </div>
      </div>

      {reports.length === 0 ? (
        <div className="empty">
          <span className="empty-ico"><Icon name="chart" size={20} /></span>
          <b>No raid yield yet</b>
          <p>Completed attacks will draw the last 16 loot stacks here.</p>
        </div>
      ) : (
        <div className="chart-wrap">
          <svg
            className="loot-chart"
            viewBox={`0 0 ${width} ${height}`}
            role="img"
            aria-label={`Stacked loot chart for ${reports.length} raids`}
          >
            {[0, 0.25, 0.5, 0.75, 1].map((fraction) => {
              const y = top + plotHeight * (1 - fraction);
              return (
                <g key={fraction}>
                  <line x1={left} y1={y} x2={width - right} y2={y} className="chart-grid" />
                  <text x={left - 9} y={y + 4} textAnchor="end" className="chart-axis">
                    {compactNumber(max * fraction)}
                  </text>
                </g>
              );
            })}

            {reports.map((report, index) => {
              const x = left + index * slot + (slot - barWidth) / 2;
              let cursor = top + plotHeight;
              return (
                <g key={`${report.timestamp}-${index}`}>
                  {CHART_SERIES.map((series) => {
                    if (!visible[series.key]) return null;
                    const value = report[
                      series.key === 'gold' ? 'gold_stolen' : series.key === 'elixir' ? 'elixir_stolen' : 'dark_elixir_stolen'
                    ] * series.scale;
                    const segment = (value / max) * plotHeight;
                    if (segment <= 0) return null;
                    cursor -= segment;
                    return (
                      <rect
                        key={series.key}
                        className="chart-bar"
                        x={x}
                        y={cursor}
                        width={barWidth}
                        height={Math.max(segment, 1)}
                        fill={series.color}
                        opacity="0.9"
                        style={{ animationDelay: `${index * 18}ms` }}
                      >
                        <title>{`${series.label}: ${exactNumber(report[
                          series.key === 'gold' ? 'gold_stolen' : series.key === 'elixir' ? 'elixir_stolen' : 'dark_elixir_stolen'
                        ])}`}</title>
                      </rect>
                    );
                  })}
                  {(index % Math.max(1, Math.ceil(reports.length / 7)) === 0 || index === reports.length - 1) ? (
                    <text x={x + barWidth / 2} y={height - 8} textAnchor="middle" className="chart-axis">
                      {new Date(report.timestamp).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })}
                    </text>
                  ) : null}
                </g>
              );
            })}
          </svg>
          <div className="chart-caption">
            <span>Older</span>
            <span>Newest</span>
          </div>
        </div>
      )}
    </section>
  );
};

const buildStrategyRows = (history: AttackReport[]): StrategyRow[] => {
  const rows = new Map<string, {
    label: string;
    attacks: number;
    stars: number;
    threeStar: number;
    loot: number;
    success: number;
  }>();

  history.forEach((report) => {
    const key = report.strategy || 'unknown';
    const row = rows.get(key) ?? {
      label: strategyLabel(key),
      attacks: 0,
      stars: 0,
      threeStar: 0,
      loot: 0,
      success: 0,
    };
    row.attacks += 1;
    row.stars += report.stars || 0;
    row.threeStar += report.stars === 3 ? 1 : 0;
    row.loot += report.gold_stolen + report.elixir_stolen + report.dark_elixir_stolen;
    row.success += report.deploy_success ? 1 : 0;
    rows.set(key, row);
  });

  return [...rows.values()]
    .map((row) => ({
      label: row.label,
      attacks: row.attacks,
      avgStars: row.stars / row.attacks,
      threeStarRate: Math.round((row.threeStar / row.attacks) * 100),
      lootPerAttack: row.loot / row.attacks,
      deployRate: Math.round((row.success / row.attacks) * 100),
    }))
    .sort((a, b) => b.attacks - a.attacks || b.avgStars - a.avgStars);
};

const Analytics: React.FC<AnalyticsProps> = React.memo(({ stats, history, activityHistory }) => {
  const starData = [
    { label: '3★', count: stats.stars_3, color: 'var(--gold)' },
    { label: '2★', count: stats.stars_2, color: 'var(--blue)' },
    { label: '1★', count: stats.stars_1, color: 'var(--violet)' },
    { label: '0★', count: stats.stars_0, color: 'var(--red)' },
  ];

  const totalAttacks = starData.reduce((sum, row) => sum + row.count, 0);
  // Revenue counters cover every completed attack; the star buckets only
  // cover attacks that produced a parsed report. Use the attack total for
  // loot averages so a partial report window cannot inflate them.
  const completedAttacks = stats.attacks_completed > 0 ? stats.attacks_completed : totalAttacks;
  const percent = (count: number) => totalAttacks > 0 ? Math.round((count / totalAttacks) * 100) : 0;
  const threeStarRate = percent(stats.stars_3);
  const avgGold = completedAttacks > 0 ? Math.round(stats.total_gold / completedAttacks) : 0;
  const avgElixir = completedAttacks > 0 ? Math.round(stats.total_elixir / completedAttacks) : 0;
  const avgDe = completedAttacks > 0 ? Math.round(stats.total_de / completedAttacks) : 0;
  const avgAll = completedAttacks > 0 ? Math.round((stats.total_gold + stats.total_elixir + stats.total_de) / completedAttacks) : 0;
  const avgStars = totalAttacks > 0
    ? ((stats.stars_3 * 3 + stats.stars_2 * 2 + stats.stars_1) / totalAttacks).toFixed(2)
    : '0.00';

  const activity = React.useMemo(() => buildActivity(activityHistory), [activityHistory]);
  const strategyRows = React.useMemo(() => buildStrategyRows(history), [history]);

  return (
    <div className="analytics-page">
      <div className="page-head">
        <div>
          <h1>Analytics</h1>
          <p>Loot throughput, attack outcomes, and daily activity for this profile.</p>
        </div>
      </div>

      <div className="analytics-kpis">
        <div className="analytics-kpi"><b>{exactNumber(stats.attacks_completed)}</b><span>attacks</span></div>
        <div className="analytics-kpi"><b>{threeStarRate}%</b><span>three-star</span></div>
        <div className="analytics-kpi"><b>{compactNumber(avgAll)}</b><span>avg loot / raid</span></div>
        <div className="analytics-kpi"><b>{avgStars}</b><span>avg stars</span></div>
      </div>

      <LootChart history={history} />

      <div className="analytics-grid">
        <section className="card">
          <div className="card-head">
            <div>
              <h3>Attack outcomes</h3>
              <p className="card-sub">Star distribution across reported attacks</p>
            </div>
          </div>

          {totalAttacks === 0 ? (
            <div className="empty">
              <span className="empty-ico"><Icon name="chart" size={20} /></span>
              <b>No attack data yet</b>
              <p>Run the bot once and star distribution will populate here.</p>
            </div>
          ) : (
            <div className="star-summary">
              <div className="star-rate">
                <b>{threeStarRate}%</b>
                <span>3★ rate</span>
              </div>
              <div className="dist">
                {starData.map((row) => (
                  <div key={row.label} className="dist-row">
                    <span className="dist-lab" style={{ color: row.color }}>{row.label}</span>
                    <span className="dist-bar">
                      <i style={{ width: `${percent(row.count)}%`, background: row.color }} />
                    </span>
                    <span className="dist-val">
                      {exactNumber(row.count)} · {percent(row.count)}%
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </section>

        <section className="card">
          <div className="card-head">
            <div>
              <h3>Revenue</h3>
              <p className="card-sub">Session totals and per-attack averages</p>
            </div>
          </div>

          {completedAttacks === 0 ? (
            <div className="empty">
              <span className="empty-ico"><Icon name="coin" size={20} /></span>
              <b>No revenue recorded yet</b>
              <p>Loot totals appear after the first completed attack.</p>
            </div>
          ) : (
            <div className="revenue-grid">
              <div className="rev-tile gold">
                <span>Gold</span>
                <b>{compactNumber(stats.total_gold)}</b>
                <small>avg {compactNumber(avgGold)} / raid</small>
              </div>
              <div className="rev-tile elixir">
                <span>Elixir</span>
                <b>{compactNumber(stats.total_elixir)}</b>
                <small>avg {compactNumber(avgElixir)} / raid</small>
              </div>
              <div className="rev-tile de">
                <span>Dark elixir</span>
                <b>{compactNumber(stats.total_de)}</b>
                <small>avg {compactNumber(avgDe)} / raid</small>
              </div>
              <div className="rev-tile combined">
                <span>Combined</span>
                <b>{compactNumber(stats.total_gold + stats.total_elixir + stats.total_de)}</b>
                <small>avg {compactNumber(avgAll)} / raid</small>
              </div>
            </div>
          )}
        </section>
      </div>

      <div className="analytics-lower">
        <section className="card analytics-activity">
          <div className="card-head">
            <div>
              <h3>Attack activity</h3>
              <p className="card-sub">Daily raids across the last year</p>
            </div>
            <div className="card-tools">
              <span className="tag">{exactNumber(activity.total)} in range</span>
            </div>
          </div>

          {activityHistory.length === 0 ? (
            <div className="empty">
              <span className="empty-ico"><Icon name="calendar" size={20} /></span>
              <b>No activity in this window</b>
              <p>Completed attacks populate this calendar by day.</p>
            </div>
          ) : (
            <>
              <div className="heatmap">
                <div className="heat-days" aria-hidden="true">
                  <span />
                  <span>Mon</span>
                  <span />
                  <span>Wed</span>
                  <span />
                  <span>Fri</span>
                  <span />
                </div>
                <div className="heat-weeks">
                  <div className="heat-months" aria-hidden="true">
                    {activity.monthLabels.map((label, index) => (
                      <span key={`${label}-${index}`}>{label}</span>
                    ))}
                  </div>
                  <div
                    className="heat-grid"
                    role="img"
                    aria-label={`${activity.total} attacks across ${activity.activeDays} active days in the last year`}
                  >
                    {activity.days.map((day) => (
                      <span
                        key={day.key}
                        className={`heat-cell l${day.level}${day.future ? ' future' : ''}`}
                        title={day.future ? undefined : `${day.count} ${day.count === 1 ? 'attack' : 'attacks'} on ${dateLabel(day.date)}`}
                      />
                    ))}
                  </div>
                </div>
              </div>

              <div className="heat-foot">
                <div className="heat-metrics">
                  <div className="heat-metric"><span>Attacks</span><b>{exactNumber(activity.total)}</b></div>
                  <div className="heat-metric"><span>Active days</span><b>{exactNumber(activity.activeDays)}</b></div>
                  <div className="heat-metric"><span>Best day</span><b>{exactNumber(activity.best)}</b></div>
                  <div className="heat-metric"><span>Streak</span><b>{activity.streak > 0 ? `${activity.streak}d` : '—'}</b></div>
                </div>
                <div className="heat-legend" aria-hidden="true">
                  Less
                  {[0, 1, 2, 3, 4].map((level) => (
                    <span key={level} className={`heat-cell l${level}`} />
                  ))}
                  More
                </div>
              </div>
            </>
          )}
        </section>

        <section className="card analytics-strategies">
          <div className="card-head">
            <div>
              <h3>Strategy performance</h3>
              <p className="card-sub">Aggregated from the current report window</p>
            </div>
          </div>

          {strategyRows.length === 0 ? (
            <div className="empty">
              <span className="empty-ico"><Icon name="layers" size={20} /></span>
              <b>No strategy results yet</b>
              <p>Reports are grouped by strategy once attacks complete.</p>
            </div>
          ) : (
            <div className="table-wrap">
              <table className="data-table">
                <thead>
                  <tr>
                    <th>Strategy</th>
                    <th className="num">Raids</th>
                    <th className="num">Avg stars</th>
                    <th className="num">3★</th>
                    <th className="num">Loot / raid</th>
                    <th className="num">Deploy</th>
                  </tr>
                </thead>
                <tbody>
                  {strategyRows.map((row) => (
                    <tr key={row.label}>
                      <td>
                        <span className="strategy-name"><i />{row.label}</span>
                      </td>
                      <td className="num">{exactNumber(row.attacks)}</td>
                      <td className="num">{row.avgStars.toFixed(2)}</td>
                      <td className="num">{row.threeStarRate}%</td>
                      <td className="num">{compactNumber(row.lootPerAttack)}</td>
                      <td className="num">{row.deployRate}%</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      </div>
    </div>
  );
});

Analytics.displayName = 'Analytics';

export default Analytics;
