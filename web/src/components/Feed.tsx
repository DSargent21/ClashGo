import React from 'react';
import { BotStats } from '../types';

interface FeedProps {
  screenshot: string;
  stats: BotStats;
}

const Feed: React.FC<FeedProps> = React.memo(({ screenshot, stats }) => {
  return (
    <div className="bg-white dark:bg-zinc-900 p-10 rounded-[3rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none flex flex-col items-center max-w-5xl mx-auto transition-all duration-500">

      <div className="w-full max-w-[860px] aspect-[860/732] bg-zinc-950 dark:bg-black rounded-[2.5rem] overflow-hidden border border-zinc-800 dark:border-zinc-900 shadow-premium-lg flex items-center justify-center relative group">
        {screenshot ? (
          <img src={screenshot} className="w-full h-full object-contain transition-transform duration-700 group-hover:scale-[1.01]" alt="Live Feed" />
        ) : (
          <div className="text-zinc-600 font-mono text-[11px] flex flex-col items-center gap-8">
            <div className="w-24 h-24 rounded-full bg-zinc-900/50 dark:bg-zinc-800/20 flex items-center justify-center shadow-inner border border-zinc-800/50">
              <span className="material-symbols-outlined text-5xl animate-pulse text-zinc-700 dark:text-zinc-600">videocam_off</span>
            </div>
            <span className="uppercase tracking-[0.4em] font-black opacity-30">Node Telemetry Offline</span>
          </div>
        )}
        
        {/* Overlays */}
        <div className="absolute top-10 left-10 flex items-center gap-4 px-5 py-2.5 bg-black/60 backdrop-blur-2xl rounded-2xl border border-white/10 shadow-2xl">
           <div className="relative">
             <span className="block w-2.5 h-2.5 bg-rose-500 rounded-full shadow-[0_0_10px_rgba(244,63,94,0.6)]"></span>
             <span className="absolute inset-0 w-2.5 h-2.5 bg-rose-500 rounded-full animate-ping opacity-75"></span>
           </div>
           <span className="text-[11px] font-black text-white uppercase tracking-[0.3em]">Operational Live Stream</span>
        </div>
      </div>

      <div className="mt-14 grid grid-cols-2 gap-24 w-full max-w-3xl">
        <div className="flex items-center gap-6 group">
          <div className="w-14 h-14 rounded-2xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center border border-zinc-100 dark:border-zinc-700 shadow-sm transition-transform group-hover:rotate-12">
            <span className="material-symbols-outlined text-zinc-400 dark:text-zinc-500 text-2xl">speed</span>
          </div>
          <div className="flex flex-col">
            <span className="text-[10px] font-black text-zinc-300 dark:text-zinc-700 uppercase tracking-[0.2em] mb-1.5">Network Latency</span>
            <span className="text-2xl text-zinc-950 dark:text-white font-bold tracking-tight tabular-nums">
              {(!stats.adb_health.avg_capture_ms || isNaN(stats.adb_health.avg_capture_ms)) 
                ? '0' 
                : (stats.adb_health.avg_capture_ms < 1 && stats.adb_health.avg_capture_ms > 0 
                  ? stats.adb_health.avg_capture_ms.toFixed(1) 
                  : Math.round(stats.adb_health.avg_capture_ms))} 
              <span className="text-xs text-zinc-400 dark:text-zinc-500 font-medium ml-1">ms</span>
            </span>
          </div>
        </div>
        <div className="flex items-center gap-6 group">
          <div className="w-14 h-14 rounded-2xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center border border-zinc-100 dark:border-zinc-700 shadow-sm transition-transform group-hover:rotate-12">
            <span className="material-symbols-outlined text-zinc-400 dark:text-zinc-500 text-2xl">data_alert</span>
          </div>
          <div className="flex flex-col">
            <span className="text-[10px] font-black text-zinc-300 dark:text-zinc-700 uppercase tracking-[0.2em] mb-1.5">Integrity Fails</span>
            <span className="text-2xl text-zinc-950 dark:text-white font-bold tracking-tight tabular-nums">{stats.adb_health.consecutive_fails} <span className="text-xs text-zinc-400 dark:text-zinc-500 font-medium ml-1">errors</span></span>
          </div>
        </div>
      </div>

    </div>
  );
});

export default Feed;
