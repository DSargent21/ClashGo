import React from 'react';
import { BotStats, AttackReport } from '../types';
import { formatUptime } from '../utils';

interface DashboardProps {
  stats: BotStats;
  history: AttackReport[];
  logs: string[];
  onClearLogs: () => void;
  terminalEndRef: React.RefObject<HTMLDivElement>;
}

const Dashboard: React.FC<DashboardProps> = React.memo(({
  stats,
  history,
  logs,
  onClearLogs,
  terminalEndRef
}) => {
  const containerRef = React.useRef<HTMLDivElement>(null);
  const uptimeHours = stats.uptime / (1e9 * 3600);
  
  React.useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    // Smart scroll: only if already at bottom (within 50px)
    const isAtBottom = container.scrollHeight - container.scrollTop <= container.clientHeight + 50;
    if (isAtBottom) {
      terminalEndRef.current?.scrollIntoView({ behavior: 'auto' });
    }
  }, [logs]);

  const getRate = (total: number) => {
    if (uptimeHours < 0.01) return 'CALC...';
    const rate = total / uptimeHours;
    if (rate > 1e6) return `+${(rate / 1e6).toFixed(1)}M/hr`;
    if (rate > 1e3) return `+${(rate / 1e3).toFixed(0)}k/hr`;
    return `+${rate.toFixed(0)}/hr`;
  };

  return (
    <div className="space-y-8">
      {/* Metrics Row */}
      <div className="grid grid-cols-3 gap-8">
        {[
          { label: 'Gold Looted', value: stats.total_gold, color: 'text-amber-500', bg: 'bg-amber-50/50', icon: 'payments', rate: getRate(stats.total_gold) },
          { label: 'Elixir Looted', value: stats.total_elixir, color: 'text-fuchsia-500', bg: 'bg-fuchsia-50/50', icon: 'water_drop', rate: getRate(stats.total_elixir) },
          { label: 'Dark Elixir', value: stats.total_de, color: 'text-zinc-950', bg: 'bg-zinc-100/50', icon: 'database', rate: getRate(stats.total_de) }
        ].map((item, idx) => (
          <div key={idx} className="bg-white p-8 rounded-[2rem] border border-zinc-100/50 shadow-premium hover:shadow-premium-hover transition-all duration-300 group">
            <div className="flex items-center justify-between mb-6">
              <div className={`w-12 h-12 rounded-2xl ${item.bg} flex items-center justify-center transition-transform group-hover:scale-110 duration-300`}>
                 <span className={`material-symbols-outlined ${item.color} text-2xl`}>{item.icon}</span>
              </div>
              <div className="px-3 py-1 bg-zinc-50 rounded-full border border-zinc-100 flex items-center justify-center min-w-[70px]">
                <span className="text-[10px] font-black text-zinc-400 uppercase tracking-widest leading-none">{item.rate}</span>
              </div>
            </div>
            <div className="space-y-1">
              <span className="text-[11px] font-black text-zinc-400 uppercase tracking-widest">{item.label}</span>
              <div className="text-3xl font-bold tracking-tight text-zinc-950">{item.value.toLocaleString()}</div>
            </div>
          </div>
        ))}
      </div>

      {/* Attack Log Table */}
      <div className="bg-white rounded-[2rem] border border-zinc-100/50 shadow-premium overflow-hidden">
        <div className="px-8 py-6 border-b border-zinc-50 flex justify-between items-center bg-zinc-50/30">
          <div>
            <h3 className="text-lg font-bold text-zinc-950">Attack Log</h3>
            <p className="text-xs text-zinc-400 font-medium">Real-time tactical deployment logs.</p>
          </div>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-left border-collapse">
            <thead>
              <tr>
                <th className="px-8 py-5 text-[10px] font-black text-zinc-400 uppercase tracking-[0.15em] bg-zinc-50/20">Loot Breakdown</th>
                <th className="px-8 py-5 text-[10px] font-black text-zinc-400 uppercase tracking-[0.15em] bg-zinc-50/20 text-center">Outcome</th>
                <th className="px-8 py-5 text-[10px] font-black text-zinc-400 uppercase tracking-[0.15em] bg-zinc-50/20 text-right">Timestamp</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-zinc-50">
              {history.length === 0 ? (
                <tr>
                  <td colSpan={3} className="px-8 py-16 text-center text-zinc-400 text-[10px] font-black uppercase tracking-[0.2em] italic">No attacks detected // waiting for deployment</td>
                </tr>
              ) : (
                history.slice(0, 5).map((rep, i) => (
                  <tr key={i} className="hover:bg-zinc-50/50 transition-colors group">
                    <td className="px-8 py-5">
                      <div className="flex gap-5">
                        <div className="flex items-center gap-2 text-xs font-bold text-zinc-700">
                          <span className="w-2 h-2 bg-amber-400 rounded-full shadow-[0_0_8px_rgba(251,191,36,0.5)]"></span> 
                          {rep.gold_stolen.toLocaleString()}
                        </div>
                        <div className="flex items-center gap-2 text-xs font-bold text-zinc-700">
                          <span className="w-2 h-2 bg-fuchsia-400 rounded-full shadow-[0_0_8px_rgba(192,38,211,0.5)]"></span> 
                          {rep.elixir_stolen.toLocaleString()}
                        </div>
                        <div className="flex items-center gap-2 text-xs font-bold text-zinc-700">
                          <span className="w-2 h-2 bg-zinc-900 rounded-full shadow-[0_0_8px_rgba(0,0,0,0.2)]"></span> 
                          {rep.dark_elixir_stolen.toLocaleString()}
                        </div>
                      </div>
                    </td>
                    <td className="px-8 py-5">
                       <div className="flex justify-center gap-1">
                        {[...Array(3)].map((_, sIdx) => (
                          <span 
                            key={sIdx} 
                            className={`material-symbols-outlined text-[20px] ${sIdx < rep.stars ? 'text-amber-400' : 'text-zinc-100'}`} 
                            style={{ fontVariationSettings: sIdx < rep.stars ? "'FILL' 1" : "'FILL' 0" }}
                          >
                            star
                          </span>
                        ))}
                      </div>
                    </td>
                    <td className="px-8 py-5 text-right text-xs font-bold text-zinc-400 tabular-nums">
                      {new Date(rep.timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>

      {/* Summary Row */}
      <div className="grid grid-cols-4 gap-8">
        {[
          { label: 'Search Skips', value: stats.search_skips, icon: 'search' },
          { label: 'Total Attacks', value: stats.attacks_completed, icon: 'bolt' },
          { label: 'Gross Loot', value: `${((stats.total_gold + stats.total_elixir) / 1e6).toFixed(1)}M`, icon: 'trending_up' },
          { label: 'Active Uptime', value: formatUptime(stats.uptime), icon: 'timer' }
        ].map((item, idx) => (
          <div key={idx} className="bg-white p-6 rounded-[1.5rem] border border-zinc-100/50 flex items-center gap-5 shadow-premium">
             <div className="w-12 h-12 rounded-2xl bg-zinc-50 flex items-center justify-center shadow-sm">
                <span className="material-symbols-outlined text-zinc-400 text-xl">{item.icon}</span>
             </div>
             <div className="flex flex-col min-w-0">
                <div className="text-[10px] font-black text-zinc-400 uppercase tracking-widest whitespace-nowrap">{item.label}</div>
                <div className="text-xl font-bold text-zinc-950 tracking-tight">{item.value}</div>
             </div>
          </div>
        ))}
      </div>

      {/* Logs Terminal */}
      <section className="bg-zinc-950 rounded-[2.5rem] p-2 shadow-premium-lg border border-zinc-800/50">
        <div className="px-6 py-2.5 flex justify-between items-center border-b border-zinc-900/50">
          <div className="flex items-center gap-3">
            <div className="flex gap-1.5">
              <div className="w-2.5 h-2.5 rounded-full bg-rose-500/20 border border-rose-500/30"></div>
              <div className="w-2.5 h-2.5 rounded-full bg-amber-500/20 border border-amber-500/30"></div>
              <div className="w-2.5 h-2.5 rounded-full bg-emerald-500/20 border border-emerald-500/30"></div>
            </div>
            <span className="ml-1 text-[9px] font-black text-zinc-500 uppercase tracking-[0.2em]">Console</span>
          </div>
          <button 
            onClick={() => {
              const text = logs.join('\n');
              navigator.clipboard.writeText(text);
            }} 
            className="h-7 px-3 rounded-lg bg-zinc-900 text-[9px] font-black text-zinc-500 hover:text-white hover:bg-zinc-800 transition-all uppercase tracking-widest"
          >
            Copy Log
          </button>
        </div>
        <div 
          ref={containerRef}
          className="p-6 h-64 terminal-scroll overflow-y-auto font-mono text-xs leading-relaxed text-zinc-400 selection:bg-zinc-800"
        >
          <div className="space-y-1.5">
            {logs.length === 0 ? (
              <div className="flex items-center gap-3 text-zinc-700">
                <span className="w-1.5 h-1.5 bg-zinc-800 rounded-full animate-pulse"></span>
                <span className="italic uppercase tracking-widest text-[9px] font-bold">Initializing telemetry stream...</span>
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
                  <div key={i} className="flex gap-3 group/log">
                    <span className="text-zinc-800 shrink-0 font-bold tabular-nums">[{new Date().toLocaleTimeString([], { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })}]</span>
                    <span className={
                      isError ? 'text-rose-400 font-bold' :
                      isSuccess ? 'text-emerald-400 font-bold' :
                      log.includes('[INFO]') ? 'text-zinc-200' : 'text-zinc-500'
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
