import React from 'react';
import { BotStats } from '../types';

interface AnalyticsProps {
  stats: BotStats;
}

const Analytics: React.FC<AnalyticsProps> = React.memo(({ stats }) => {
  const starData = [
    { label: '3 Stars', count: stats.stars_3, color: 'bg-emerald-500', bg: 'bg-emerald-500/10' },
    { label: '2 Stars', count: stats.stars_2, color: 'bg-zinc-800', bg: 'bg-zinc-800/10' },
    { label: '1 Star', count: stats.stars_1, color: 'bg-zinc-400', bg: 'bg-zinc-400/10' },
    { label: '0 Stars', count: stats.stars_0, color: 'bg-rose-500', bg: 'bg-rose-500/10' },
  ];

  const totalAttacks = stats.stars_3 + stats.stars_2 + stats.stars_1 + stats.stars_0;

  return (
    <div className="grid grid-cols-1 xl:grid-cols-2 gap-10 max-w-6xl mx-auto">

      <div className="bg-white p-10 rounded-[2.5rem] border border-zinc-100/50 shadow-premium group">
        <div className="flex justify-between items-center mb-10">
          <h3 className="text-xl font-bold text-zinc-950">Performance Distribution</h3>
          <div className="w-10 h-10 rounded-2xl bg-zinc-50 flex items-center justify-center">
            <span className="material-symbols-outlined text-zinc-400">equalizer</span>
          </div>
        </div>
        <div className="space-y-8">
          {starData.map((item, idx) => {
            const percent = totalAttacks > 0 ? Math.round((item.count / totalAttacks) * 100) : 0;
            return (
              <div key={idx} className="space-y-3">
                <div className="flex justify-between text-[10px] font-black text-zinc-400 uppercase tracking-[0.15em]">
                  <span>{item.label}</span>
                  <span className="text-zinc-950">{item.count} <span className="text-zinc-300 ml-1.5 font-bold">({percent}%)</span></span>
                </div>
                <div className="w-full bg-zinc-50 h-3 rounded-full overflow-hidden p-0.5 border border-zinc-100">
                  <div 
                    className={`h-full transition-all duration-1000 rounded-full ${item.color} shadow-sm`} 
                    style={{ width: `${percent}%` }}
                  ></div>
                </div>
              </div>
            );
          })}
        </div>
      </div>

      <div className="bg-white p-10 rounded-[2.5rem] border border-zinc-100/50 shadow-premium">
        <div className="flex justify-between items-center mb-10">
          <h3 className="text-xl font-bold text-zinc-950">Session Averages</h3>
          <div className="w-10 h-10 rounded-2xl bg-zinc-50 flex items-center justify-center">
            <span className="material-symbols-outlined text-zinc-400">insights</span>
          </div>
        </div>
        <div className="space-y-10">
          {[
            { label: 'Total Loot Collected', value: (stats.total_gold + stats.total_elixir).toLocaleString(), icon: 'account_balance_wallet', color: 'text-zinc-950' },
            { label: 'Avg Gold / Cycle', value: stats.attacks_completed > 0 ? Math.round(stats.total_gold / stats.attacks_completed).toLocaleString() : '0', icon: 'payments', color: 'text-amber-500' },
            { label: 'Avg Elixir / Cycle', value: stats.attacks_completed > 0 ? Math.round(stats.total_elixir / stats.attacks_completed).toLocaleString() : '0', icon: 'water_drop', color: 'text-fuchsia-500' }
          ].map((m, i) => (
            <div key={i} className="flex justify-between items-center group">
              <div className="flex items-center gap-5">
                <div className="w-12 h-12 rounded-2xl bg-zinc-50 flex items-center justify-center border border-zinc-100 shadow-sm group-hover:scale-110 transition-transform duration-300">
                  <span className={`material-symbols-outlined ${m.color}`}>{m.icon}</span>
                </div>
                <div>
                  <span className="block text-[10px] font-black text-zinc-400 uppercase tracking-widest">{m.label}</span>
                </div>
              </div>
              <span className="text-2xl font-bold text-zinc-950 tracking-tight tabular-nums">{m.value}</span>
            </div>
          ))}
        </div>
      </div>

    </div>
  );
});

export default Analytics;
