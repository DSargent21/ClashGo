import React from 'react';
import { BotStats, UpdateStatus } from '../types';

interface SettingsViewProps {
  stats: BotStats;
  adbPort: number;
  darkMode: boolean;
  setDarkMode: (val: boolean) => void;
  onResetStats: () => void;
  appVersion: string;
  updateStatus: UpdateStatus;
  onCheckUpdates: () => void;
  onClearSkip: () => void;
}

const SettingsView: React.FC<SettingsViewProps> = React.memo(({
  stats, adbPort, darkMode, setDarkMode, onResetStats,
  appVersion, updateStatus, onCheckUpdates, onClearSkip,
}) => {
  return (
    <div className="bg-white dark:bg-zinc-900 p-10 rounded-[3rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none max-w-2xl mx-auto transition-all duration-500">

      <div className="flex justify-between items-center mb-12">
        <div>
          <h3 className="text-2xl font-bold text-zinc-950 dark:text-white mb-2 tracking-tight">System Settings</h3>
          <p className="text-sm text-zinc-500 dark:text-zinc-500 font-medium">Core application and connection status.</p>
        </div>
        <div className="w-14 h-14 rounded-2xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center border border-zinc-100 dark:border-zinc-700 shadow-sm transition-colors">
          <span className="material-symbols-outlined text-zinc-500 dark:text-zinc-500 text-2xl">memory</span>
        </div>
      </div>

      <div className="space-y-4">
        {/* Dark Mode Toggle */}
        <div 
          onClick={() => setDarkMode(!darkMode)}
          className="flex justify-between items-center bg-zinc-50/50 dark:bg-zinc-800/30 p-6 rounded-2xl border border-zinc-100/50 dark:border-zinc-800/50 hover:bg-white dark:hover:bg-zinc-800/60 hover:shadow-premium dark:hover:shadow-none transition-all duration-300 group cursor-pointer"
        >
          <div className="flex items-center gap-5">
            <div className="w-12 h-12 rounded-xl bg-white dark:bg-zinc-800 flex items-center justify-center border border-zinc-100 dark:border-zinc-700 group-hover:scale-105 transition-all duration-300 shadow-sm">
              <span className="material-symbols-outlined text-xl text-zinc-500 dark:text-zinc-500">
                {darkMode ? 'dark_mode' : 'light_mode'}
              </span>
            </div>
            <div className="flex flex-col">
              <span className="text-[10px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em] mb-0.5">App Theme</span>
              <span className="text-sm font-bold text-zinc-950 dark:text-white">{darkMode ? 'Dark' : 'Light'}</span>
            </div>
          </div>
          <div 
            className={`w-12 h-6 rounded-full p-1 transition-all duration-500 ease-in-out relative ${darkMode ? 'bg-zinc-700' : 'bg-zinc-200'}`}
          >
            <div className={`w-4 h-4 rounded-full bg-white dark:bg-zinc-400 transition-all duration-500 ease-in-out shadow-md ${darkMode ? 'translate-x-6' : 'translate-x-0'}`}></div>
          </div>
        </div>

          {[
            { label: 'Connection Status', value: stats.adb_health.consecutive_fails === 0 ? 'Optimal' : 'Interrupted', status: stats.adb_health.consecutive_fails === 0 ? 'success' : 'error', icon: 'hub' },
            { label: 'ADB Port', value: adbPort.toString(), status: 'info', icon: 'router' },
            { label: 'Capture Latency', value: isNaN(stats.adb_health.avg_capture_ms) ? '0ms' : `${stats.adb_health.avg_capture_ms.toFixed(1)}ms`, status: stats.adb_health.avg_capture_ms < 200 ? 'success' : 'info', icon: 'speed' },
            // cpu_time_sec is device-independent (absolute CPU seconds since
            // start). cpu_cores is a fraction of one core; scaled by the host's
            // logical core count only to render a familiar 0-100% number.
            { label: 'CPU Time', value: `${stats.cpu_time_sec.toFixed(1)}s`, status: 'info', icon: 'schedule' },
            { label: 'CPU Usage', value: isNaN(stats.cpu_cores) ? '0%' : `${(stats.cpu_cores * (navigator.hardwareConcurrency || 1) * 100).toFixed(1)}%`, status: stats.cpu_cores < 0.5 ? 'success' : 'info', icon: 'memory' },
          ].map((item, i) => (

          <div key={i} className="flex justify-between items-center bg-zinc-50/50 dark:bg-zinc-800/30 p-6 rounded-2xl border border-zinc-100/50 dark:border-zinc-800/50 hover:bg-white dark:hover:bg-zinc-800/60 hover:shadow-premium dark:hover:shadow-none transition-all duration-300 group">
            <div className="flex items-center gap-5">
              <div className="w-12 h-12 rounded-xl bg-white dark:bg-zinc-900 flex items-center justify-center border border-zinc-100 dark:border-zinc-800 group-hover:scale-105 transition-all duration-300 shadow-sm">
                <span className="material-symbols-outlined text-xl text-zinc-500 dark:text-zinc-500">{item.icon}</span>
              </div>
              <div className="flex flex-col">
                <span className="text-[10px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em] mb-0.5">{item.label}</span>
                <span className={`text-sm font-bold tracking-tight ${item.status === 'error' ? 'text-rose-600 dark:text-rose-400' : 'text-zinc-950 dark:text-white'}`}>{item.value}</span>
              </div>
            </div>
            <div className="flex items-center gap-3">
               <div className={`w-2 h-2 rounded-full ${
                 item.status === 'success' ? 'bg-emerald-500 shadow-[0_0_8px_rgba(16,185,129,0.4)]' :
                 item.status === 'error' ? 'bg-rose-500 animate-pulse shadow-[0_0_8px_rgba(244,63,94,0.4)]' : 'bg-zinc-300 dark:bg-zinc-600'
               }`}></div>
            </div>
          </div>
        ))}

        {/* Update row — surfaces current version + a manual check
            button so users can force a refresh without waiting for the
            6h background poller. */}
        <div
          onClick={onCheckUpdates}
          className="flex justify-between items-center bg-zinc-50/50 dark:bg-zinc-800/30 p-6 rounded-2xl border border-zinc-100/50 dark:border-zinc-800/50 hover:bg-white dark:hover:bg-zinc-800/60 hover:shadow-premium dark:hover:shadow-none transition-all duration-300 group cursor-pointer"
        >
          <div className="flex items-center gap-5">
            <div className="w-12 h-12 rounded-xl bg-white dark:bg-zinc-900 flex items-center justify-center border border-zinc-100 dark:border-zinc-800 group-hover:scale-105 transition-all duration-300 shadow-sm">
              <span className="material-symbols-outlined text-xl text-zinc-500 dark:text-zinc-500">system_update</span>
            </div>
            <div className="flex flex-col">
              <span className="text-[10px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em] mb-0.5">
                App Version
              </span>
              <span className="text-sm font-bold tracking-tight text-zinc-950 dark:text-white tabular-nums">
                v{appVersion || '0.0.0'}
                {updateStatus.available && (
                  <span className="ml-3 text-[10px] font-black uppercase tracking-widest text-emerald-500">
                    Update {updateStatus.latest_version} available
                  </span>
                )}
              </span>
            </div>
          </div>
          <div className="flex items-center gap-3">
            {updateStatus.skip_version && (
              <button
                onClick={(e) => { e.stopPropagation(); onClearSkip(); }}
                className="text-[10px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em] hover:text-zinc-700 dark:hover:text-zinc-300 transition-colors"
                title={`Resume notifications for v${updateStatus.skip_version}`}
              >
                Resume notifications
              </button>
            )}
            <span className="material-symbols-outlined text-zinc-300 dark:text-zinc-700 group-hover:translate-x-1 transition-transform">refresh</span>
          </div>
        </div>


        {/* Reset Section */}
        <div className="pt-8 mt-8 border-t border-zinc-50 dark:border-zinc-800/50">
           <button 
             onClick={onResetStats}
             className="w-full flex justify-between items-center bg-rose-50/50 dark:bg-rose-950/10 p-6 rounded-2xl border border-rose-100/50 dark:border-rose-900/20 hover:bg-rose-100/50 dark:hover:bg-rose-950/20 transition-all duration-300 group"
           >
             <div className="flex items-center gap-5">
               <div className="w-12 h-12 rounded-xl bg-white dark:bg-zinc-900 flex items-center justify-center border border-rose-100 dark:border-rose-900 group-hover:scale-105 transition-all duration-300 shadow-sm">
                 <span className="material-symbols-outlined text-xl text-rose-500 dark:text-rose-400" style={{ fontVariationSettings: "'FILL' 1" }}>delete_forever</span>
               </div>
               <div className="flex flex-col text-left">
                 <span className="text-[10px] font-black text-rose-600 dark:text-rose-500 uppercase tracking-[0.2em] mb-0.5">Danger Zone</span>
                 <span className="text-sm font-bold text-rose-600 dark:text-rose-400">Reset All Statistics</span>
               </div>
             </div>
             <span className="material-symbols-outlined text-rose-400 dark:text-rose-800 group-hover:translate-x-1 transition-transform">chevron_right</span>
           </button>
        </div>
      </div>
    </div>
  );
});

export default SettingsView;
