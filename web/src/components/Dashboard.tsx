import React from 'react';
import { BotStats, AttackReport } from '../types';
import { formatUptime } from '../utils';

interface DashboardProps {
  stats: BotStats;
  history: AttackReport[];
  logs: string[];
  terminalEndRef: React.RefObject<HTMLDivElement>;
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

const Dashboard: React.FC<DashboardProps> = React.memo(({
  stats,
  history,
  logs,
  terminalEndRef,
}) => {
  const containerRef = React.useRef<HTMLDivElement>(null);
  const [terminalAutoScroll, setTerminalAutoScroll] = React.useState(getTerminalAutoScroll);
  const uptimeHours = stats.uptime / (1e9 * 3600);
  
  React.useEffect(() => {
    try {
      localStorage.setItem('terminalAutoScroll', String(terminalAutoScroll));
    } catch (e) {
      console.warn('Failed to save terminalAutoScroll preference:', e);
    }
  }, [terminalAutoScroll]);

  React.useEffect(() => {
    if (!terminalAutoScroll) return;
    
    const container = containerRef.current;
    if (!container) return;

    // Smart scroll: only if already at bottom (within 100px)
    const isAtBottom = container.scrollHeight - container.scrollTop <= container.clientHeight + 100;
    if (isAtBottom) {
      container.scrollTop = container.scrollHeight;
    }
  }, [logs, terminalAutoScroll]);

  const getRate = (total: number) => {
    if (uptimeHours < 0.01 || total === 0) return '+0/hr';
    const rate = total / uptimeHours;
    if (rate > 1e6) return `+${(rate / 1e6).toFixed(1)}M/hr`;
    if (rate > 1e3) return `+${(rate / 1e3).toFixed(0)}k/hr`;
    return `+${rate.toFixed(0)}/hr`;
  };

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
                <span className="text-[10px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-widest leading-none">{item.rate}</span>
              </div>
            </div>
            <div className="space-y-1">
              <span className="text-[11px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em]">{item.label}</span>
              <div className="text-4xl font-bold tracking-tight text-zinc-950 dark:text-white">{item.value.toLocaleString()}</div>
            </div>
          </div>
        ))}
      </div>

      {/* Attack Log Table */}
      <div className="bg-white dark:bg-zinc-900 rounded-[2.5rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none overflow-hidden transition-all duration-500">
        <div className="px-10 py-5 border-b border-zinc-50 dark:border-zinc-800/50 flex justify-between items-center bg-zinc-50/30 dark:bg-zinc-800/20">
          <div>
            <h3 className="text-xl font-bold text-zinc-950 dark:text-white tracking-tight">Attack History</h3>
            <p className="text-sm text-zinc-400 dark:text-zinc-500 font-medium">Recent combat deployments and results.</p>
          </div>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-left border-collapse">
            <thead>
              <tr>
                <th className="px-10 py-4 text-[11px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em] bg-zinc-50/10 dark:bg-zinc-800/10">Loot Collected</th>
                <th className="px-10 py-4 text-[11px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em] bg-zinc-50/10 dark:bg-zinc-800/10 text-center">Stars</th>
                <th className="px-10 py-4 text-[11px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em] bg-zinc-50/10 dark:bg-zinc-800/10 text-right">Timestamp</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-50 dark:divide-zinc-800/50">
              {history.length === 0 ? (
                <tr>
                  <td colSpan={3} className="px-10 py-24 text-center text-zinc-300 dark:text-zinc-700 text-[11px] font-black uppercase tracking-[0.3em] italic">No data available // Waiting for activity</td>
                </tr>
              ) : (
                history.slice(0, 5).map((rep, i) => (
                  <tr key={i} className="hover:bg-zinc-50/50 dark:hover:bg-zinc-800/40 transition-colors group">
                    <td className="px-10 py-4">
                      <div className="flex gap-6">
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
                    <td className="px-10 py-4">
                       <div className="flex justify-center gap-1.5">
                        {[...Array(3)].map((_, sIdx) => (
                          <span 
                            key={sIdx} 
                            className={`material-symbols-outlined text-2xl ${sIdx < rep.stars ? 'text-amber-400' : 'text-zinc-100 dark:text-zinc-800'}`} 
                            style={{ fontVariationSettings: sIdx < rep.stars ? "'FILL' 1" : "'FILL' 0" }}
                          >
                            star
                          </span>
                        ))}
                      </div>
                    </td>
                    <td className="px-10 py-4 text-right text-xs font-black text-zinc-400 dark:text-zinc-500 tabular-nums uppercase tracking-widest">
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
          <div key={idx} className="bg-white dark:bg-zinc-900 p-5 rounded-[2rem] border border-zinc-100/50 dark:border-zinc-800/50 flex items-center gap-6 shadow-premium dark:shadow-none transition-all duration-500 hover:bg-zinc-50 dark:hover:bg-zinc-800/40">
             <div className="w-14 h-14 rounded-2xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center border border-zinc-100 dark:border-zinc-700 shadow-sm transition-transform group-hover:scale-110">
                <span className="material-symbols-outlined text-zinc-400 dark:text-zinc-500 text-2xl">{item.icon}</span>
             </div>
             <div className="flex flex-col min-w-0">
                <div className="text-[10px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em] mb-0.5">{item.label}</div>
                <div className="flex items-baseline gap-2">
                  <div className="text-2xl font-bold text-zinc-950 dark:text-white tracking-tight">{item.value}</div>
                  {item.detail && (
                    <div className="text-[9px] font-black text-zinc-300 dark:text-zinc-700 uppercase tracking-widest tabular-nums">{item.detail}</div>
                  )}
                </div>
             </div>
          </div>
        ))}
      </div>

      {/* Logs Terminal */}
      <section className="bg-zinc-950 dark:bg-black rounded-[3rem] p-3 shadow-premium-lg border border-zinc-800/50 dark:border-zinc-900/80 transition-all duration-500">
        <div className="px-8 py-3 flex justify-between items-center border-b border-zinc-900/50">
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
                  : 'bg-zinc-900 dark:bg-zinc-900/50 text-zinc-500 border-zinc-800/50 hover:text-white hover:bg-zinc-800'
              }`}
              title={terminalAutoScroll ? 'Auto-scroll enabled (click to disable)' : 'Auto-scroll disabled (click to enable)'}
            >
              <span className="material-symbols-outlined text-sm">
                {terminalAutoScroll ? 'keyboard_arrow_down' : 'keyboard_arrow_up'}
              </span>
              Auto
            </button>
            <button 
              onClick={() => {
                const text = logs.join('\n');
                navigator.clipboard.writeText(text);
              }} 
              className="h-9 px-5 rounded-xl bg-zinc-900 dark:bg-zinc-900/50 text-[10px] font-black text-zinc-500 hover:text-white hover:bg-zinc-800 transition-all uppercase tracking-[0.2em] border border-zinc-800/50"
            >
              Export Logs
            </button>
          </div>
        </div>
        <div 
          ref={containerRef}
          className="p-5 h-80 terminal-scroll overflow-y-auto font-mono text-[13px] leading-relaxed text-zinc-400 selection:bg-emerald-500/20"
        >
          <div className="space-y-2">
            {logs.length === 0 ? (
              <div className="flex items-center gap-4 text-zinc-800 dark:text-zinc-700">
                <div className="w-2 h-2 bg-zinc-800 dark:bg-zinc-700 rounded-full animate-pulse"></div>
                <span className="italic uppercase tracking-[0.3em] font-black text-[10px]">Initializing connection...</span>
              </div>
            ) : (
              logs.map((log, i) => {
                let displayMsg = log;
                let isError = log.includes('[ERROR]');
                let isSuccess = log.includes('[SUCCESS]');
                
                try {
                  if (log.startsWith('{')) {
                    const parsed = JSON.parse(log);
                    displayMsg = parsed.message || log;
                    if (parsed.level === 'error') isError = true;
                    if (parsed.level === 'info' && (displayMsg.includes('success') || displayMsg.includes('complete'))) isSuccess = true;
                  }
                } catch (e) {}

                return (
                  <div key={i} className="flex gap-4 group/log">
                    <span className="text-zinc-800 dark:text-zinc-800 shrink-0 font-bold tabular-nums">[{new Date().toLocaleTimeString([], { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })}]</span>
                    <span className={
                      isError ? 'text-rose-400 font-bold' :
                      isSuccess ? 'text-emerald-400 font-bold' :
                      log.includes('[INFO]') ? 'text-zinc-200' : 'text-zinc-600 dark:text-zinc-500'
                    }>
                      {displayMsg}
                    </span>
                  </div>
                );
              })
            )}
            <div ref={terminalEndRef} />
          </div>
        </div>
      </section>
    </div>
  );
});

export default Dashboard;
