import React from 'react';
import { BotStats, AttackReport } from '../types';
import {
  formatUptime,
  parseLogLineCached,
  LogSeverity,
  compactNumber,
  exactNumber,
  relativeTime,
  strategyLabel,
} from '../utils';
import Icon from './ui/Icon';
import Drawer from './ui/Drawer';

interface DashboardProps {
  stats: BotStats;
  history: AttackReport[];
  logs: string[];
  running: boolean;
  strategy: string;
  adbPort: number;
}

const getTerminalAutoScroll = (): boolean => {
  try {
    const stored = localStorage.getItem('terminalAutoScroll');
    if (stored !== null) return stored === 'true';
    return true;
  } catch {
    return true;
  }
};

// Clipboard write with a fallback for webviews where the async clipboard
// API isn't granted (Wails WkWebView in some macOS versions).
const copyText = async (text: string): Promise<void> => {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    document.execCommand('copy');
    document.body.removeChild(ta);
  }
};

const LEVEL_LABEL: Record<LogSeverity, string> = {
  debug: 'DBG',
  info: 'INF',
  success: 'OK',
  warn: 'WRN',
  error: 'ERR',
};

const LEVEL_CLASS: Record<LogSeverity, string> = {
  debug: 'lvl-debug',
  info: 'lvl-info',
  success: 'lvl-success',
  warn: 'lvl-warn',
  error: 'lvl-error',
};

const SEVERITIES: { id: LogSeverity | 'all'; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'debug', label: 'Debug' },
  { id: 'info', label: 'Info' },
  { id: 'success', label: 'Success' },
  { id: 'warn', label: 'Warn' },
  { id: 'error', label: 'Error' },
];

const Stars: React.FC<{ count: number; big?: boolean }> = ({ count, big = false }) => (
  <span className={`stars ${big ? 'big' : ''}`.trim()} aria-label={`${count} of 3 stars`}>
    {[1, 2, 3].map((index) => (
      <Icon
        key={index}
        name="star"
        size={big ? 25 : 12}
        className={`icon ${index <= count ? 'on' : ''}`.trim()}
      />
    ))}
  </span>
);

/** Tiny dependency-free sparkline. Values are drawn left → right (oldest → newest). */
const Sparkline: React.FC<{ values: number[]; tone: 'gold' | 'elixir' | 'de' }> = ({ values, tone }) => {
  const gradientId = React.useId().replace(/:/g, '');
  const width = 260;
  const height = 38;
  const pad = 1;
  // A flat rule at the baseline is not a trend. Until a raid lands there is
  // nothing to plot, and drawing the empty series read as a broken border
  // under the card; skip the chart and let the card stay compact.
  if (values.length < 2) return null;
  const data = values;
  const min = Math.min(...data);
  const max = Math.max(...data);
  const span = max - min || 1;
  const x = (index: number) => pad + index * (width - pad * 2) / (data.length - 1);
  const y = (value: number) => height - pad - ((value - min) / span) * (height - pad * 2);

  let line = `M${x(0).toFixed(1)},${y(data[0]).toFixed(1)}`;
  for (let i = 1; i < data.length; i++) {
    const px = x(i - 1);
    const cx = (px + x(i)) / 2;
    line += ` C${cx.toFixed(1)},${y(data[i - 1]).toFixed(1)} ${cx.toFixed(1)},${y(data[i]).toFixed(1)} ${x(i).toFixed(1)},${y(data[i]).toFixed(1)}`;
  }

  const color = tone === 'gold' ? 'var(--gold)' : tone === 'elixir' ? 'var(--elixir)' : 'var(--de)';
  const area = `${line} L${x(data.length - 1).toFixed(1)},${height} L${x(0).toFixed(1)},${height} Z`;

  return (
    <svg
      className="spark"
      viewBox={`0 0 ${width} ${height}`}
      preserveAspectRatio="none"
      aria-hidden="true"
    >
      <defs>
        <linearGradient id={`spark-${gradientId}`} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0" stopColor={color} stopOpacity=".16" />
          <stop offset="1" stopColor={color} stopOpacity="0" />
        </linearGradient>
      </defs>
      <path className="spark-area" d={area} fill={`url(#spark-${gradientId})`} />
      <path
        className="spark-line"
        d={line}
        fill="none"
        stroke={color}
        strokeWidth="1.5"
        strokeLinecap="round"
        vectorEffect="non-scaling-stroke"
        pathLength={1}
        opacity=".9"
      />
      <circle cx={x(data.length - 1)} cy={y(data[data.length - 1])} r="2.4" fill={color} />
    </svg>
  );
};

// cumulativeSeries turns per-raid loot into the session total the card shows:
// the line has to climb as raids add up, not spike and fall back. The leading 0
// is the session start, so a single raid already draws a rise instead of the
// flat line a one-point series collapses to.
const cumulativeSeries = (perRaid: number[]): number[] => {
  const out = [0];
  let total = 0;
  for (const value of perRaid) {
    total += value;
    out.push(total);
  }
  return out;
};

// lootSeries feeds the card's sparkline. Raid history is the real trend; when
// the session has loot but no per-raid rows yet (restored stats), the line is
// the session's own 0 → total climb. Zero loot draws nothing, so the card never
// shows a meaningless flat rule.
const lootSeries = (perRaid: number[], total: number): number[] => {
  if (perRaid.length > 0) return cumulativeSeries(perRaid);
  return total > 0 ? [0, total] : [];
};

const rateLabel = (total: number, uptimeHours: number): string => {
  if (uptimeHours < 0.01 || total === 0) return '+0/hr';
  return `+${compactNumber(total / uptimeHours)}/hr`;
};

const safeTimestamp = (iso: string): string => {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso || 'Unknown' : date.toLocaleString();
};

const LootRow: React.FC<{
  label: string;
  value: number;
  max: number;
  tone: 'gold' | 'elixir' | 'de';
  icon: 'coin' | 'drop';
  bonus?: number;
}> = ({ label, value, max, tone, icon, bonus = 0 }) => (
  <>
    <div className={`loot-row ${tone}`.trim()}>
      <span className="li"><Icon name={icon} size={13} /></span>
      <span className="ll">{label}</span>
      <span className="lb">
        <i
          style={{
            width: `${Math.min(100, Math.max(1.5, (value / Math.max(max, 1)) * 100))}%`,
            background: tone === 'gold' ? 'var(--gold)' : tone === 'elixir' ? 'var(--elixir)' : 'var(--de)',
          }}
        />
      </span>
      <span className="lv">{exactNumber(value)}</span>
    </div>
    {bonus > 0 ? <div className="bonus-line">+ {exactNumber(bonus)} bonus</div> : null}
  </>
);

const RaidReport: React.FC<{ report: AttackReport }> = ({ report }) => {
  const maxLoot = Math.max(report.gold_stolen, report.elixir_stolen, report.dark_elixir_stolen, 1);
  const clean = report.deploy_success;
  const partial = !clean && report.undeployed_slots > 0;
  const edge = report.target_edge || 'Auto';
  const parsedAt = safeTimestamp(report.timestamp);

  return (
    <>
      <div className="report-result">
        <Stars count={report.stars} big />
        <div className={`report-word ${report.stars > 0 ? 'win' : 'lose'}`}>
          {report.stars > 0 ? 'Victory' : 'No stars'} · {report.stars}/3
        </div>
      </div>

      <div className="dr-sec">Loot secured</div>
      <LootRow label="Gold" value={report.gold_stolen} max={maxLoot} tone="gold" icon="coin" bonus={report.bonus_gold} />
      <LootRow label="Elixir" value={report.elixir_stolen} max={maxLoot} tone="elixir" icon="drop" bonus={report.bonus_elixir} />
      <LootRow label="Dark" value={report.dark_elixir_stolen} max={maxLoot} tone="de" icon="drop" bonus={report.bonus_de} />

      <div className="dr-sec">Deployment</div>
      <div className="meta-grid">
        <div>
          <span>Strategy</span>
          <b>{strategyLabel(report.strategy)}</b>
        </div>
        <div>
          <span>Target edge</span>
          <b>{edge.charAt(0).toUpperCase() + edge.slice(1)}</b>
        </div>
        <div>
          <span>Deploy health</span>
          <b className={clean ? 'pos' : undefined}>{clean ? 'Clean' : partial ? 'Partial' : 'Failed'}</b>
        </div>
        <div>
          <span>Undeployed slots</span>
          <b>{report.undeployed_slots}</b>
        </div>
        <div>
          <span>Result parsing</span>
          <b>{report.parsed_results ? 'Parsed' : 'Not parsed'}</b>
        </div>
        <div>
          <span>Session attack</span>
          <b>#{report.total_attacks_session}</b>
        </div>
      </div>

      <div className="dr-sec">Timestamp</div>
      <div className="meta-grid">
        <div>
          <span>Observed</span>
          <b>{relativeTime(report.timestamp, 'unknown')}</b>
        </div>
        <div>
          <span>Exact</span>
          <b title={parsedAt}>{parsedAt.length > 24 ? `${parsedAt.slice(0, 24)}…` : parsedAt}</b>
        </div>
      </div>

      {report.deploy_error ? (
        <>
          <div className="dr-sec">Deploy error</div>
          <div className="error-banner">
            <Icon name="alert" size={14} />
            <span className="mono" style={{ fontSize: 11, wordBreak: 'break-word' }}>{report.deploy_error}</span>
          </div>
        </>
      ) : null}
    </>
  );
};

const Dashboard: React.FC<DashboardProps> = React.memo(({
  stats,
  history,
  logs,
  running,
  strategy,
  adbPort,
}) => {
  const containerRef = React.useRef<HTMLDivElement>(null);
  const [terminalAutoScroll, setTerminalAutoScroll] = React.useState(getTerminalAutoScroll);
  const [terminalHovered, setTerminalHovered] = React.useState(false);
  const [logFilter, setLogFilter] = React.useState('');
  const [severityFilter, setSeverityFilter] = React.useState<LogSeverity | 'all'>('all');
  const [copiedIdx, setCopiedIdx] = React.useState<number | null>(null);
  const [report, setReport] = React.useState<AttackReport | null>(null);
  const copiedTimerRef = React.useRef<number | null>(null);
  const uptimeHours = stats.uptime / (1e9 * 3600);

  React.useEffect(() => {
    try {
      localStorage.setItem('terminalAutoScroll', String(terminalAutoScroll));
    } catch (e) {
      console.warn('Failed to save terminalAutoScroll preference:', e);
    }
  }, [terminalAutoScroll]);

  React.useEffect(() => () => {
    if (copiedTimerRef.current) window.clearTimeout(copiedTimerRef.current);
  }, []);

  const parsedLogs = React.useMemo(() => logs.map(parseLogLineCached), [logs]);

  const severityCounts = React.useMemo(() => {
    const counts: Record<LogSeverity, number> = { debug: 0, info: 0, success: 0, warn: 0, error: 0 };
    for (const line of parsedLogs) counts[line.level] += 1;
    return counts;
  }, [parsedLogs]);

  const filteredLogs = React.useMemo(() => {
    const needle = logFilter.trim().toLowerCase();
    if (!needle && severityFilter === 'all') return parsedLogs;
    return parsedLogs.filter((line) => {
      if (severityFilter !== 'all' && line.level !== severityFilter) return false;
      if (needle && !line.message.toLowerCase().includes(needle)) return false;
      return true;
    });
  }, [parsedLogs, logFilter, severityFilter]);

  // Built on click, not every poll: the buffer only changes when the bot
  // writes, and the string is ~10KB.
  const exportLogs = () => {
    void copyText(parsedLogs
      .map((line) => `${line.timestamp ? `${line.timestamp} | ` : ''}${levelUpper(line.level)} | ${line.message}`)
      .join('\n'));
  };

  React.useEffect(() => {
    if (!terminalAutoScroll || terminalHovered) return;
    const container = containerRef.current;
    if (!container) return;
    const isAtBottom = container.scrollHeight - container.scrollTop <= container.clientHeight + 100;
    if (isAtBottom) container.scrollTop = container.scrollHeight;
  }, [filteredLogs, terminalAutoScroll, terminalHovered]);

  const copyLine = (idx: number, text: string) => {
    void copyText(text);
    setCopiedIdx(idx);
    if (copiedTimerRef.current) window.clearTimeout(copiedTimerRef.current);
    copiedTimerRef.current = window.setTimeout(() => setCopiedIdx(null), 1200);
  };

  const highlightMatch = (message: string): React.ReactNode => {
    const needle = logFilter.trim().toLowerCase();
    if (!needle) return message;
    const lower = message.toLowerCase();
    const parts: React.ReactNode[] = [];
    let last = 0;
    let index = lower.indexOf(needle);
    while (index !== -1) {
      parts.push(message.slice(last, index));
      parts.push(<mark key={index}>{message.slice(index, index + needle.length)}</mark>);
      last = index + needle.length;
      index = lower.indexOf(needle, last);
    }
    parts.push(message.slice(last));
    return parts;
  };

  const openReport = (event: React.MouseEvent | React.KeyboardEvent, item: AttackReport) => {
    if ((event.target as HTMLElement).closest('button')) return;
    setReport(item);
  };

  const resources = [
    {
      key: 'gold' as const,
      label: 'Gold looted',
      icon: 'coin' as const,
      value: stats.total_gold,
      series: lootSeries(history.slice(0, 24).map((item) => item.gold_stolen).reverse(), stats.total_gold),
    },
    {
      key: 'elixir' as const,
      label: 'Elixir looted',
      icon: 'drop' as const,
      value: stats.total_elixir,
      series: lootSeries(history.slice(0, 24).map((item) => item.elixir_stolen).reverse(), stats.total_elixir),
    },
    {
      key: 'de' as const,
      label: 'Dark elixir',
      icon: 'drop' as const,
      value: stats.total_de,
      series: lootSeries(history.slice(0, 24).map((item) => item.dark_elixir_stolen).reverse(), stats.total_de),
    },
  ];

  const adbState =
    stats.attacks_completed + stats.search_skips > 0
      ? stats.adb_health.consecutive_fails === 0
        ? { label: 'Connected', tone: 'green' }
        : stats.adb_health.consecutive_fails < 5
          ? { label: 'Degraded', tone: 'warn' }
          : { label: 'Disconnected', tone: 'error' }
      : { label: 'Awaiting', tone: 'warn' };

  const latency = Number.isFinite(stats.adb_health.avg_capture_ms)
    ? `${stats.adb_health.avg_capture_ms.toFixed(1)} ms`
    : '—';

  const recentReports = history.slice(0, 8);

  return (
    <div>
      <div className="page-head">
        <div>
          <h1>Dashboard</h1>
          <p>Loot, raids, and engine health — live from the bot.</p>
        </div>
      </div>

      <div className="res-grid">
        {resources.map((resource) => (
          <article key={resource.key} className={`card res-card ${resource.key}`.trim()}>
            <div className="res-top">
              <div className="res-label">
                <span className="res-ico"><Icon name={resource.icon} size={14} /></span>
                {resource.label}
              </div>
              <span className="rate">{rateLabel(resource.value, uptimeHours)}</span>
            </div>
            <div className="res-value">{exactNumber(resource.value)}</div>
            <div className="res-meta">Session total</div>
            <Sparkline values={resource.series} tone={resource.key} />
          </article>
        ))}
      </div>

      <div className="dash-grid">
        <div className="card history-card">
          <div className="card-head">
            <div>
              <h3>Attack history</h3>
              <p className="card-sub">Recent deployments and results</p>
            </div>
            <div className="card-tools history-meta">
              {running ? <span className="live"><i />Live</span> : null}
              <span className="hist-count">{history.length} raids</span>
            </div>
          </div>

          {history.length === 0 ? (
            <div className="empty">
              <span className="empty-ico"><Icon name="target" size={20} /></span>
              <b>No raids recorded yet</b>
              <p>Reports appear here after the bot completes its first attack.</p>
            </div>
          ) : (
            <div className="table-wrap">
              <table className="data-table">
                <thead>
                  <tr>
                    <th>Result</th>
                    <th>Loot collected</th>
                    <th>Strategy</th>
                    <th>Edge</th>
                    <th>Time</th>
                    <th aria-label="Open report" />
                  </tr>
                </thead>
                <tbody>
                  {recentReports.map((item, index) => (
                    <tr
                      key={`${item.timestamp}-${index}`}
                      tabIndex={0}
                      aria-label={`Open report for ${strategyLabel(item.strategy)}`}
                      onClick={(event) => openReport(event, item)}
                      onKeyDown={(event) => {
                        if (event.key === 'Enter' || event.key === ' ') {
                          event.preventDefault();
                          openReport(event, item);
                        }
                      }}
                    >
                      <td><Stars count={item.stars} /></td>
                      <td>
                        <div className="loot-cell">
                          <span className="loot">
                            <span title={`${exactNumber(item.gold_stolen)} gold`}><i className="dot gold" />{compactNumber(item.gold_stolen)}</span>
                            <span title={`${exactNumber(item.elixir_stolen)} elixir`}><i className="dot elixir" />{compactNumber(item.elixir_stolen)}</span>
                            <span title={`${exactNumber(item.dark_elixir_stolen)} dark elixir`}><i className="dot de" />{compactNumber(item.dark_elixir_stolen)}</span>
                          </span>
                          {item.bonus_gold + item.bonus_elixir + item.bonus_de > 0 ? (
                            <span className="loot-bonus">
                              {item.bonus_gold > 0 ? <span className="gold">+{compactNumber(item.bonus_gold)}</span> : null}
                              {item.bonus_elixir > 0 ? <span className="elixir">+{compactNumber(item.bonus_elixir)}</span> : null}
                              {item.bonus_de > 0 ? <span className="de">+{compactNumber(item.bonus_de)}</span> : null}
                            </span>
                          ) : null}
                        </div>
                      </td>
                      <td>
                        <span className="strategy-cell" title={item.strategy}>
                          {strategyLabel(item.strategy)}
                        </span>
                        {!item.deploy_success ? <span className="tag danger" style={{ marginLeft: 6 }}>Deploy</span> : null}
                      </td>
                      <td>
                        <span className="tag">{item.target_edge || 'auto'}</span>
                      </td>
                      <td className="time-cell" title={safeTimestamp(item.timestamp)}>
                        {relativeTime(item.timestamp, 'unknown')}
                      </td>
                      <td style={{ width: 40 }}>
                        <button
                          type="button"
                          className="icon-btn plain sm"
                          aria-label={`Open report for ${strategyLabel(item.strategy)}`}
                          onClick={() => setReport(item)}
                        >
                          <Icon name="chevronRight" size={14} />
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>

        <div className="dash-rail">
          <div className="card">
            <div className="card-head">
              <div>
                <h3>Engine</h3>
                <p className="card-sub">Autonomous raiding</p>
              </div>
              <span className={`status-pill ${running ? 'running' : ''}`.trim()}>
                <i />{running ? 'Running' : 'Idle'}
              </span>
            </div>
            <div className="kv-list">
              <div className="kv"><span>Strategy</span><b title={strategy}>{strategyLabel(strategy)}</b></div>
              <div className="kv"><span>Uptime</span><b>{formatUptime(stats.uptime)}</b></div>
              <div className="kv"><span>Searches</span><b>{exactNumber(stats.search_skips + stats.attacks_completed)}</b></div>
              <div className="kv"><span>Skips</span><b>{exactNumber(stats.search_skips)}</b></div>
            </div>
          </div>

          <div className="card">
            <div className="card-head">
              <div>
                <h3>Device</h3>
                <p className="card-sub">ADB bridge</p>
              </div>
              <span className={`status-pill ${adbState.tone}`}>
                <i />{adbState.label}
              </span>
            </div>
            <div className="kv-list">
              <div className="kv"><span>Port</span><b>{adbPort}</b></div>
              <div className="kv"><span>Capture latency</span><b>{latency}</b></div>
              <div className="kv"><span>Consecutive fails</span><b>{stats.adb_health.consecutive_fails}</b></div>
              <div className="kv">
                <span>Last error</span>
                <b className={stats.adb_health.last_error ? 'wrap' : undefined} title={stats.adb_health.last_error || undefined}>
                  {stats.adb_health.last_error || 'None'}
                </b>
              </div>
            </div>
          </div>
        </div>
      </div>

      <section className="card">
        <div className="card-head">
          <div>
            <h3>System console</h3>
            <p className="card-sub">Live bot output · click a line to copy</p>
          </div>
          <div className="card-tools">
            <span className="muted mono">
              {filteredLogs.length}/{parsedLogs.length}
            </span>
            <button
              type="button"
              onClick={() => setTerminalAutoScroll(!terminalAutoScroll)}
              className={`btn sm ${terminalAutoScroll ? '' : 'ghost'}`.trim()}
              aria-pressed={terminalAutoScroll}
              title={terminalAutoScroll ? 'Auto-scroll enabled' : 'Auto-scroll disabled'}
            >
              <Icon name={terminalAutoScroll ? 'chevronDown' : 'chevronUp'} size={12} />
              Auto
            </button>
            <button
              type="button"
              onClick={exportLogs}
              className="btn sm"
              title="Copy the full console to the clipboard (ANSI-free)"
            >
              <Icon name="download" size={12} />
              Export logs
            </button>
          </div>
        </div>

        <div className="console-toolbar">
          <label className="search">
            <Icon name="search" size={14} />
            <input
              value={logFilter}
              onChange={(event) => setLogFilter(event.target.value)}
              placeholder="Filter logs…"
              aria-label="Filter logs by text"
            />
            {logFilter ? (
              <button
                type="button"
                onClick={() => setLogFilter('')}
                className="icon-btn plain sm"
                aria-label="Clear log filter"
              >
                <Icon name="x" size={12} />
              </button>
            ) : null}
          </label>
          <div className="chipbar" aria-label="Filter by severity">
            {SEVERITIES.map((chip) => {
              const count = chip.id === 'all' ? parsedLogs.length : severityCounts[chip.id];
              const active = severityFilter === chip.id;
              return (
                <button
                  key={chip.id}
                  type="button"
                  onClick={() => setSeverityFilter(chip.id)}
                  aria-pressed={active}
                  className={`chip ${active ? 'active' : ''}`.trim()}
                  title={`${count} ${chip.label.toLowerCase()} lines`}
                >
                  {chip.id === 'all' ? null : <i className={LEVEL_CLASS[chip.id]} />}
                  {chip.label}
                </button>
              );
            })}
          </div>
        </div>

        <div
          ref={containerRef}
          role="log"
          aria-label="System console — live bot output"
          onMouseEnter={() => setTerminalHovered(true)}
          onMouseLeave={() => setTerminalHovered(false)}
          className="console"
        >
          {parsedLogs.length === 0 ? (
            <div className="muted" style={{ padding: '6px 2px' }}>Initializing connection…</div>
          ) : filteredLogs.length === 0 ? (
            <div className="muted" style={{ padding: '6px 2px' }}>No lines match the current filter.</div>
          ) : (
            filteredLogs.map((line, index) => (
              <div key={`${line.timestamp}-${index}`} className="log-row">
                <span className="log-time">{line.timestamp || '--:--:--'}</span>
                <span className={`log-lvl ${LEVEL_CLASS[line.level]}`.trim()}>{LEVEL_LABEL[line.level]}</span>
                <span className="log-msg">{highlightMatch(line.message)}</span>
                <button
                  type="button"
                  onClick={() => copyLine(index, line.message)}
                  className="icon-btn plain sm log-copy"
                  title="Copy line"
                  aria-label="Copy log line"
                >
                  <Icon name={copiedIdx === index ? 'check' : 'file'} size={12} />
                </button>
              </div>
            ))
          )}
        </div>
      </section>

      <Drawer
        open={report !== null}
        onClose={() => setReport(null)}
        kicker={report ? `Raid report · ${report.total_attacks_session} in session` : 'Raid report'}
        title={report ? strategyLabel(report.strategy) : 'Raid report'}
        footer={<button type="button" className="btn block" onClick={() => setReport(null)}>Close</button>}
      >
        {report ? <RaidReport report={report} /> : null}
      </Drawer>
    </div>
  );
});

const levelUpper = (level: LogSeverity): string => LEVEL_LABEL[level];

Dashboard.displayName = 'Dashboard';

export default Dashboard;
