import React from 'react';
import { BotStats } from '../types';

interface SettingsViewProps {
  stats: BotStats;
  adbPort: number;
}

const SettingsView: React.FC<SettingsViewProps> = React.memo(({ stats, adbPort }) => {
  return (
    <div className="bg-white p-10 rounded-[2.5rem] border border-zinc-100/50 shadow-premium max-w-2xl mx-auto">

      <div className="flex justify-between items-center mb-10">
        <div>
          <h3 className="text-xl font-bold text-zinc-950 mb-1">System Architecture</h3>
          <p className="text-xs text-zinc-400 font-medium">Core operational parameters and health.</p>
        </div>
        <div className="w-12 h-12 rounded-2xl bg-zinc-50 flex items-center justify-center border border-zinc-100 shadow-sm">
          <span className="material-symbols-outlined text-zinc-400">memory</span>
        </div>
      </div>

      <div className="space-y-4">
        {[
          { label: 'ADB Connection', value: stats.adb_health.consecutive_fails === 0 ? 'Optimal' : 'Interrupted', status: stats.adb_health.consecutive_fails === 0 ? 'success' : 'error', icon: 'hub' },
          { label: 'ADB Port', value: adbPort.toString(), status: 'info', icon: 'router' },
          { label: 'Avg Latency', value: isNaN(stats.adb_health.avg_capture_ms) ? '0ms' : `${stats.adb_health.avg_capture_ms.toFixed(1)}ms`, status: stats.adb_health.avg_capture_ms < 200 ? 'success' : 'info', icon: 'speed' },
        ].map((item, i) => (

          <div key={i} className="flex justify-between items-center bg-zinc-50/50 p-5 rounded-2xl border border-zinc-100/50 hover:bg-white hover:shadow-premium transition-all duration-300 group">
            <div className="flex items-center gap-4">
              <div className="w-10 h-10 rounded-xl bg-white flex items-center justify-center border border-zinc-100 group-hover:scale-110 transition-transform duration-300 shadow-sm">
                <span className="material-symbols-outlined text-lg text-zinc-400">{item.icon}</span>
              </div>
              <span className="text-[10px] font-black text-zinc-400 uppercase tracking-widest">{item.label}</span>
            </div>
            <div className="flex items-center gap-3">
               <div className={`w-2 h-2 rounded-full ${
                 item.status === 'success' ? 'bg-emerald-500' : 
                 item.status === 'error' ? 'bg-rose-500 animate-pulse' : 'bg-zinc-300'
               }`}></div>
               <span className={`text-sm font-bold tracking-tight ${item.status === 'error' ? 'text-rose-600' : 'text-zinc-950'}`}>{item.value}</span>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
});

export default SettingsView;
