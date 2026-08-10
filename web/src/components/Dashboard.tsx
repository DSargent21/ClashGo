import React from 'react';
import { BotStats, AttackReport } from '../types';
import { formatUptime, parseLogLine, LogSeverity } from '../utils';

interface DashboardProps {
  stats: BotStats;
  history: AttackReport[];
  logs: string[];
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

const Dashboard: React.FC<DashboardProps> = React.memo(({
  stats,
  history,
  logs,
}) => {
  const containerRef = React.useRef<HTMLDivElement>(null);
  const [terminalAutoScroll, setTerminalAutoScroll] = React.useState(getTerminalAutoScroll);
  const [terminalHovered, setTerminalHovered] = React.useState(false);
  const [logFilter, setLogFilter] = React.useState('');
  const [severityFilter, setSeverityFilter] = React.useState<LogSeverity | 'all'>('all');
  const [copiedIdx, setCopiedIdx] = React.useState<number | null>(null);
  const copiedTimerRef = React.useRef<number | null>(null);
  const uptimeHours = stats.uptime / (1e9 * 3600);

  React.useEffect(() => {
    try {
      localStorage.setItem('terminalAutoScroll', String(terminalAutoScroll));
    } catch (e) {
      console.warn('Failed to save terminalAutoScroll preference:', e);
    }
  }, [terminalAutoScroll]);

  // Unmount-only cleanup for the copy-feedback timer.
  React.useEffect(() => () => {
    if (copiedTimerRef.current) window.clearTimeout(copiedTimerRef.current);
  }, []);

  // Parse once per log update — the raw strings are stable between
  // polls, so memoizing on `logs` keeps re-renders cheap.
  const parsedLogs = React.useMemo(() => logs.map(parseLogLine), [logs]);

  const severityCounts = React.useMemo(() => {
    const counts: Record<LogSeverity, number> = { debug: 0, info: 0, success: 0, warn: 0, error: 0 };
    for (const l of parsedLogs) counts[l.level]++;
    return counts;
  }, [parsedLogs]);

  const filteredLogs = React.useMemo(() => {
    const needle = logFilter.trim().toLowerCase();
    if (!needle && severityFilter === 'all') return parsedLogs;
    return parsedLogs.filter((l) => {
      if (severityFilter !== 'all' && l.level !== severityFilter) return false;
      if (needle && !l.message.toLowerCase().includes(needle)) return false;
      return true;
    });
  }, [parsedLogs, logFilter, severityFilter]);

  // ANSI-free export: rebuild each line from the parsed structure so
  // the copied log never carries zerolog escape codes.
  const exportText = React.useMemo(() =>
    parsedLogs
      .map((l) => `${l.timestamp ? l.timestamp + ' | ' : ''}${l.level.toUpperCase()} | ${l.message}`)
      .join('\n'),
  [parsedLogs]);

  const copyLine = (idx: number, text: string) => {
    void copyText(text);
    setCopiedIdx(idx);
    if (copiedTimerRef.current) window.clearTimeout(copiedTimerRef.current);
    copiedTimerRef.current = window.setTimeout(() => setCopiedIdx(null), 1200);
  };

  // Wrap every (case-insensitive) occurrence of the filter text in
  // <mark> so matches pop out while the message keeps its color.
  const highlightMatch = (message: string): React.ReactNode => {
    const needle = logFilter.trim().toLowerCase();
    if (!needle) return message;
    const lower = message.toLowerCase();
    const parts: React.ReactNode[] = [];
    let last = 0;
    let i = lower.indexOf(needle);
    while (i !== -1) {
      parts.push(message.slice(last, i));
      parts.push(<mark key={i}>{message.slice(i, i + needle.length)}</mark>);
      last = i + needle.length;
      i = lower.indexOf(needle, last);
    }
    parts.push(message.slice(last));
    return parts;
  };

  React.useEffect(() => {
    if (!terminalAutoScroll || terminalHovered) return;

    const container = containerRef.current;
    if (!container) return;

    // Smart scroll: only if already at bottom (within 100px). While
    // hovering, auto-scroll is suspended so the user can inspect/copy
    // freely — resuming the moment the pointer leaves.
    const isAtBottom = container.scrollHeight - container.scrollTop <= container.clientHeight + 100;
    if (isAtBottom) {
      container.scrollTop = container.scrollHeight;
    }
  }, [filteredLogs, terminalAutoScroll, terminalHovered]);

  const getRate = (total: number) => {
    if (uptimeHours < 0.01 || total === 0) return '+0/hr';
    const rate = total / uptimeHours;
    if (rate > 1e6) return `+${(rate / 1e6).toFixed(1)}M/hr`;
    if (rate > 1e3) return `+${(rate / 1e3).toFixed(0)}k/hr`;
    return `+${rate.toFixed(0)}/hr`;
  };

  const severityChips: { id: LogSeverity | 'all'; label: string; active: string; dot: string }[] = [
    { id: 'all', label: 'All', active: 'bg-zinc-100 dark:bg-zinc-800 text-zinc-900 dark:text-white border-zinc-200 dark:border-zinc-700', dot: 'bg-zinc-400' },
    { id: 'debug', label: 'Debug', active: 'bg-violet-500/15 text-violet-600 dark:text-violet-400 border-violet-500/40', dot: 'bg-violet-500' },
    { id: 'info', label: 'Info', active: 'bg-zinc-100 dark:bg-zinc-800 text-zinc-900 dark:text-white border-zinc-200 dark:border-zinc-700', dot: 'bg-zinc-400' },
    { id: 'success', label: 'Success', active: 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/40', dot: 'bg-emerald-500' },
    { id: 'warn', label: 'Warn', active: 'bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/40', dot: 'bg-amber-500' },
    { id: 'error', label: 'Error', active: 'bg-rose-500/15 text-rose-600 dark:text-rose-400 border-rose-500/40', dot: 'bg-rose-500' },
  ];

  return (
    <div className="space-y-6">
      {/* Metrics Row */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-8">
        {[
          { label: 'Gold Looted', value: stats.total_gold, color: 'text-amber-500', bg: 'bg-amber-500/10', icon: 'monetization_on', rate: getRate(stats.total_gold) },
          { label: 'Elixir Looted', value: stats.total_elixir, color: 'text-fuchsia-500', bg: 'bg-fuchsia-500/10', icon: 'water_drop', rate: getRate(stats.total_elixir) },
          { label: 'Dark Elixir', value: stats.total_de, color: 'text-zinc-950 dark:text-zinc-100', bg: 'bg-zinc-100 dark:bg-zinc-800', icon: 'water_drop', rate: getRate(stats.total_de) }
        ].map((item, idx) => (
          <div key={idx} className="bg-white dark:bg-zinc-900 p-6 rounded-[2.5rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none hover:shadow-premium-hover dark:hover:bg-zinc-800/50 transition-all duration-300 group">
            <div className="flex items-center justify-between mb-5">
              <div className={`w-14 h-14 rounded-2xl ${item.bg} flex items-center justify-center transition-transform group-hover:scale-110 duration-300 shadow-sm`}>
                 <span className={`material-symbols-outlined ${item.color} text-2xl`}>{item.icon}</span>
              </div>
              <div className="px-4 py-1.5 bg-zinc-50 dark:bg-zinc-800 rounded-full border border-zinc-100 dark:border-zinc-700 flex items-center justify-center min-w-[80px]">
                <span className="text-[10px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-widest leading-none">{item.rate}</span>
              </div>
            </div>
            <div className="space-y-1">
              <span className="text-[11px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em]">{item.label}</span>
              <div className="text-4xl font-bold tracking-tight text-zinc-950 dark:text-white">{item.value.toLocaleString()}</div>
            </div>
          </div>
        ))}
      </div>

      {/* Attack Log Table */}
      <div className="bg-white dark:bg-zinc-900 rounded-[2.5rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none overflow-hidden transition-all duration-500">
        <div className="px-6 py-5 border-b border-zinc-50 dark:border-zinc-800/50 flex justify-between items-center bg-zinc-50/30 dark:bg-zinc-800/20">
          <div>
            <h3 className="text-xl font-bold text-zinc-950 dark:text-white tracking-tight">Attack History</h3>
            <p className="text-sm text-zinc-500 dark:text-zinc-500 font-medium">Recent combat deployments and results.</p>
          </div>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-left border-collapse">
            <thead>
              <tr>
                <th className="px-6 py-4 text-[11px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em] bg-zinc-50/10 dark:bg-zinc-800/10">Loot Collected</th>
                <th className="px-6 py-4 text-[11px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em] bg-zinc-50/10 dark:bg-zinc-800/10 text-center">Stars</th>
                <th className="px-6 py-4 text-[11px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em] bg-zinc-50/10 dark:bg-zinc-800/10 text-right">Timestamp</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-50 dark:divide-zinc-800/50">
              {history.length === 0 ? (
                <tr>
                  <td colSpan={3} className="px-6 py-24 text-center text-zinc-400 dark:text-zinc-700 text-[11px] font-black uppercase tracking-[0.3em] italic">No data available // Waiting for activity</td>
                </tr>
              ) : (
                history.slice(0, 5).map((rep, i) => (
                  <tr key={i} className="hover:bg-zinc-50/50 dark:hover:bg-zinc-800/40 transition-colors group">
                    <td className="px-6 py-4">
                      <div className="flex gap-5">
                        <div className="flex flex-col">
                          <div className="flex items-center gap-2 text-sm font-bold text-zinc-700 dark:text-zinc-300 tabular-nums">
                            <span className="material-symbols-outlined text-amber-500 text-base">monetization_on</span>
                            {(rep.gold_stolen + rep.bonus_gold).toLocaleString()}
                          </div>
                          {rep.bonus_gold > 0 && (
                            <div className="text-[10px] font-black text-amber-500/60 uppercase tracking-widest pl-6">
                              + {rep.bonus_gold.toLocaleString()} bonus
                            </div>
                          )}
                        </div>
                        <div className="flex flex-col">
                          <div className="flex items-center gap-2 text-sm font-bold text-zinc-700 dark:text-zinc-300 tabular-nums">
                            <span className="material-symbols-outlined text-fuchsia-500 text-base">water_drop</span>
                            {(rep.elixir_stolen + rep.bonus_elixir).toLocaleString()}
                          </div>
                          {rep.bonus_elixir > 0 && (
                            <div className="text-[10px] font-black text-fuchsia-500/60 uppercase tracking-widest pl-6">
                              + {rep.bonus_elixir.toLocaleString()} bonus
                            </div>
                          )}
                        </div>
                        <div className="flex flex-col">
                          <div className="flex items-center gap-2 text-sm font-bold text-zinc-700 dark:text-zinc-300 tabular-nums">
                            <span className="material-symbols-outlined text-zinc-900 dark:text-zinc-400 text-base">water_drop</span>
                            {(rep.dark_elixir_stolen + rep.bonus_de).toLocaleString()}
                          </div>
                          {rep.bonus_de > 0 && (
                            <div className="text-[10px] font-black text-zinc-500/60 uppercase tracking-widest pl-6">
                              + {rep.bonus_de.toLocaleString()} bonus
                            </div>
                          )}
                        </div>
                      </div>
                    </td>
                    <td className="px-6 py-4">
                       <div className="flex justify-center gap-1.5">
                        {[...Array(3)].map((_, sIdx) => (
                          <span
                            key={sIdx}
                            className={`material-symbols-outlined text-2xl ${sIdx < rep.stars ? 'text-amber-400' : 'text-zinc-200 dark:text-zinc-800'}`}
                            style={{ fontVariationSettings: sIdx < rep.stars ? "'FILL' 1" : "'FILL' 0" }}
                          >
                            star
                          </span>
                        ))}
                      </div>
                    </td>
                    <td className="px-6 py-4 text-right text-xs font-black text-zinc-500 dark:text-zinc-500 tabular-nums uppercase tracking-widest">
                      {new Date(rep.timestamp).toLocaleTimeString([], { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })}
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>

      {/* Summary Row */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-8">
        {[
          { label: 'Bases Searched', value: stats.search_skips + stats.attacks_completed, icon: 'search', detail: `${stats.search_skips} skips` },
          { label: 'Attacks', value: stats.attacks_completed, icon: 'bolt' },
          { label: 'Total Revenue', value: `${((stats.total_gold + stats.total_elixir) / 1e6).toFixed(1)}M`, icon: 'trending_up' },
          { label: 'System Uptime', value: formatUptime(stats.uptime), icon: 'timer' }
        ].map((item, idx) => (
          <div key={idx} className="group bg-white dark:bg-zinc-900 p-5 rounded-[2rem] border border-zinc-100/50 dark:border-zinc-800/50 flex items-center gap-6 shadow-premium dark:shadow-none transition-all duration-500 hover:bg-zinc-50 dark:hover:bg-zinc-800/40">
             <div className="w-14 h-14 rounded-2xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center border border-zinc-100 dark:border-zinc-700 shadow-sm transition-transform group-hover:scale-110">
                <span className="material-symbols-outlined text-zinc-500 dark:text-zinc-500 text-2xl">{item.icon}</span>
             </div>
             <div className="flex flex-col min-w-0">
                <div className="text-[10px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em] mb-0.5">{item.label}</div>
                <div className="flex items-baseline gap-2">
                  <div className="text-2xl font-bold text-zinc-950 dark:text-white tracking-tight">{item.value}</div>
                  {item.detail && (
                    <div className="text-[9px] font-black text-zinc-400 dark:text-zinc-700 uppercase tracking-widest tabular-nums">{item.detail}</div>
                  )}
                </div>
             </div>
          </div>
        ))}
      </div>

      {/* Logs Terminal */}
      <section className="bg-white dark:bg-black rounded-[3rem] p-3 shadow-premium-lg border border-zinc-200/60 dark:border-zinc-900/80 transition-all duration-500">
        <div className="px-8 py-3 flex flex-wrap items-center gap-4 justify-between border-b border-zinc-100 dark:border-zinc-900/50">
          <div className="flex items-center gap-4">
            <div className="flex gap-2">
              <div className="w-3 h-3 rounded-full bg-rose-500/20 border border-rose-500/40"></div>
              <div className="w-3 h-3 rounded-full bg-amber-500/20 border border-amber-500/40"></div>
              <div className="w-3 h-3 rounded-full bg-emerald-500/20 border border-emerald-500/40"></div>
            </div>
            <span className="text-[10px] font-black text-zinc-600 dark:text-zinc-500 uppercase tracking-[0.3em]">System Console</span>
          </div>
          <div className="flex items-center gap-3">
            <button
              onClick={() => setTerminalAutoScroll(!terminalAutoScroll)}
              className={`flex items-center gap-2 h-9 px-4 rounded-xl transition-all uppercase tracking-[0.2em] text-[10px] font-black border ${
                terminalAutoScroll
                  ? 'bg-emerald-500/10 text-emerald-500 border-emerald-500/30 hover:bg-emerald-500/20'
                  : 'bg-zinc-100 dark:bg-zinc-900/50 text-zinc-500 border-zinc-200 dark:border-zinc-800/50 hover:text-zinc-700 dark:hover:text-white hover:bg-zinc-200 dark:hover:bg-zinc-800'
              }`}
              title={terminalAutoScroll ? 'Auto-scroll enabled (click to disable)' : 'Auto-scroll disabled (click to enable)'}
            >
              <span className="material-symbols-outlined text-sm">
                {terminalAutoScroll ? 'keyboard_arrow_down' : 'keyboard_arrow_up'}
              </span>
              Auto
            </button>
            <button
              onClick={() => void copyText(exportText)}
              className="h-9 px-5 rounded-xl bg-zinc-100 dark:bg-zinc-900/50 text-[10px] font-black text-zinc-500 hover:text-zinc-700 dark:hover:text-white hover:bg-zinc-200 dark:hover:bg-zinc-800 transition-all uppercase tracking-[0.2em] border border-zinc-200 dark:border-zinc-800/50 active:scale-95"
              title="Copy the full console to the clipboard (ANSI-free)"
            >
              Export Logs
            </button>
          </div>
        </div>

        {/* Filter bar: text search + severity chips. */}
        <div className="px-8 py-3 flex flex-wrap items-center gap-3 border-b border-zinc-100 dark:border-zinc-900/50">
          <div className="relative flex-1 min-w-[180px] max-w-sm">
            <span className="absolute left-3 top-1/2 -translate-y-1/2 material-symbols-outlined text-sm text-zinc-600">search</span>
            <input
              value={logFilter}
              onChange={(e) => setLogFilter(e.target.value)}
              placeholder="Filter logs…"
              aria-label="Filter logs by text"
              className="w-full h-9 pl-9 pr-8 rounded-xl bg-white dark:bg-zinc-900/50 border border-zinc-200 dark:border-zinc-800/60 text-xs font-bold text-zinc-800 dark:text-zinc-200 placeholder:text-zinc-400 dark:placeholder:text-zinc-600 focus:outline-none focus:border-emerald-500/40 focus:ring-2 focus:ring-emerald-500/10 transition-all"
            />
            {logFilter && (
              <button
                onClick={() => setLogFilter('')}
                className="absolute right-2.5 top-1/2 -translate-y-1/2 text-zinc-400 dark:text-zinc-600 hover:text-zinc-700 dark:hover:text-zinc-300 transition-colors"
                aria-label="Clear log filter"
              >
                <span className="material-symbols-outlined text-sm">close</span>
              </button>
            )}
          </div>
          <div className="flex items-center gap-2 text-[10px] font-black text-zinc-400 dark:text-zinc-600 uppercase tracking-widest tabular-nums whitespace-nowrap" aria-live="polite">
            {filteredLogs.length}<span className="text-zinc-300 dark:text-zinc-700">/</span>{parsedLogs.length} lines
          </div>
          <div className="flex items-center gap-1.5 flex-wrap">
            {severityChips.map((chip) => {
              const count = chip.id === 'all' ? parsedLogs.length : severityCounts[chip.id];
              const isActive = severityFilter === chip.id;
              return (
                <button
                  key={chip.id}
                  onClick={() => setSeverityFilter(chip.id)}
                  aria-pressed={isActive}
                  className={`h-8 px-3 rounded-lg text-[10px] font-black uppercase tracking-widest border transition-all flex items-center gap-1.5 ${
                    isActive
                      ? chip.active
                      : 'text-zinc-500 border-zinc-200 dark:border-zinc-800/50 hover:text-zinc-700 dark:hover:text-zinc-300 hover:border-zinc-300 dark:hover:border-zinc-700'
                  }`}
                >
                  <span className={`w-1.5 h-1.5 rounded-full ${chip.dot} ${isActive ? '' : 'opacity-40'}`}></span>
                  {chip.label}
                  <span className={`tabular-nums ${isActive ? '' : 'opacity-40'}`}>{count}</span>
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
          className="p-5 h-80 terminal-scroll overflow-y-auto font-mono text-[13px] leading-relaxed text-zinc-600 dark:text-zinc-400 selection:bg-emerald-500/20"
        >
          <div className="space-y-1">
            {parsedLogs.length === 0 ? (
              <div className="flex items-center gap-4 text-zinc-400 dark:text-zinc-700 py-2">
                <div className="w-2 h-2 bg-zinc-300 dark:bg-zinc-700 rounded-full animate-pulse"></div>
                <span className="italic uppercase tracking-[0.3em] font-black text-[10px]">Initializing connection...</span>
              </div>
            ) : filteredLogs.length === 0 ? (
              <div className="text-zinc-400 dark:text-zinc-700 py-2 text-[11px] font-bold uppercase tracking-[0.25em]">
                No logs match the current filter
              </div>
            ) : (
              filteredLogs.map((line, i) => (
                <div key={i} className="flex items-start gap-3 group/log hover:bg-zinc-100 dark:hover:bg-zinc-900/50 rounded-lg px-2 -mx-2 py-1 transition-colors">
                  <span className="text-zinc-400 dark:text-zinc-700 shrink-0 font-bold tabular-nums text-xs pt-px">
                    {line.timestamp || '--:--:--'}
                  </span>
                  <span className="shrink-0 text-[10px] font-black w-12 pt-px text-center uppercase tracking-wider tabular-nums">
                    <span className={line.level === 'error' ? 'text-rose-500' : line.level === 'warn' ? 'text-amber-500' : line.level === 'debug' ? 'text-violet-500' : line.level === 'success' ? 'text-emerald-500' : 'text-zinc-600'}>
                      {line.level === 'success' ? 'OK' : line.level}
                    </span>
                  </span>
                  <span
                    className={`flex-1 break-words ${
                      line.level === 'error' ? 'text-rose-600 dark:text-rose-400 font-semibold' :
                      line.level === 'warn' ? 'text-amber-600 dark:text-amber-300/90' :
                      line.level === 'debug' ? 'text-violet-600 dark:text-violet-300/70' :
                      line.level === 'success' ? 'text-emerald-600 dark:text-emerald-400' :
                      'text-zinc-600 dark:text-zinc-300'
                    }`}
                  >
                    {highlightMatch(line.message)}
                  </span>
                  <button
                    onClick={() => copyLine(i, line.message)}
                    className={`opacity-0 group-hover/log:opacity-100 transition-all p-0.5 -mr-1 ${
                      copiedIdx === i
                        ? 'text-emerald-500 opacity-100'
                        : 'text-zinc-400 dark:text-zinc-600 hover:text-zinc-700 dark:hover:text-white'
                    }`}
                    title="Copy line"
                    aria-label="Copy log line"
                  >
                    <span className="material-symbols-outlined text-sm">
                      {copiedIdx === i ? 'check' : 'content_copy'}
                    </span>
                  </button>
                </div>
              ))
            )}
          </div>
        </div>
      </section>
    </div>
  );
});

export default Dashboard;
