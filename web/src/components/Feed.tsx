import React from 'react';
import { BotStats } from '../types';

interface FeedProps {
  screenshot: string;
  stats: BotStats;
}

const Feed: React.FC<FeedProps> = React.memo(({ screenshot, stats }) => {
  return (
    <div className="bg-white p-10 rounded-[2.5rem] border border-zinc-100/50 shadow-premium flex flex-col items-center max-w-5xl mx-auto">

      <div className="w-full max-w-[860px] aspect-[860/732] bg-zinc-950 rounded-[2rem] overflow-hidden border border-zinc-800 shadow-premium-lg flex items-center justify-center relative group">
        {screenshot ? (
          <img src={screenshot} className="w-full h-full object-contain transition-transform duration-700 group-hover:scale-[1.02]" alt="Live Feed" />
        ) : (
          <div className="text-zinc-600 font-mono text-[10px] flex flex-col items-center gap-6">
            <div className="w-20 h-20 rounded-full bg-zinc-900 flex items-center justify-center shadow-inner">
              <span className="material-symbols-outlined text-4xl animate-pulse text-zinc-700">videocam_off</span>
            </div>
            <span className="uppercase tracking-[0.3em] font-black opacity-40">Camera Offline</span>
          </div>
        )}
        
        {/* Overlays */}
        <div className="absolute top-8 left-8 flex items-center gap-3 px-4 py-2 bg-black/40 backdrop-blur-xl rounded-2xl border border-white/10 shadow-xl">
           <div className="relative">
             <span className="block w-2 h-2 bg-rose-500 rounded-full"></span>
             <span className="absolute inset-0 w-2 h-2 bg-rose-500 rounded-full animate-ping opacity-75"></span>
           </div>
           <span className="text-[10px] font-black text-white uppercase tracking-[0.2em]">Live View</span>
        </div>
      </div>

      <div className="mt-12 grid grid-cols-2 gap-20 w-full max-w-2xl">
        <div className="flex items-center gap-5">
          <div className="w-12 h-12 rounded-2xl bg-zinc-50 flex items-center justify-center border border-zinc-100 shadow-sm">
            <span className="material-symbols-outlined text-zinc-400 text-xl">speed</span>
          </div>
          <div className="flex flex-col">
            <span className="text-[10px] font-black text-zinc-300 uppercase tracking-widest mb-1">Capture Latency</span>
            <span className="text-xl text-zinc-950 font-bold tracking-tight tabular-nums">
              {(!stats.adb_health.avg_capture_ms || isNaN(stats.adb_health.avg_capture_ms)) 
                ? '0' 
                : (stats.adb_health.avg_capture_ms < 1 && stats.adb_health.avg_capture_ms > 0 
                  ? stats.adb_health.avg_capture_ms.toFixed(1) 
                  : Math.round(stats.adb_health.avg_capture_ms))} 
              <span className="text-xs text-zinc-400 font-medium">ms</span>
            </span>
          </div>
        </div>
        <div className="flex items-center gap-5">
          <div className="w-12 h-12 rounded-2xl bg-zinc-50 flex items-center justify-center border border-zinc-100 shadow-sm">
            <span className="material-symbols-outlined text-zinc-400 text-xl">data_alert</span>
          </div>
          <div className="flex flex-col">
            <span className="text-[10px] font-black text-zinc-300 uppercase tracking-widest mb-1">Capture Failures</span>
            <span className="text-xl text-zinc-950 font-bold tracking-tight tabular-nums">{stats.adb_health.consecutive_fails} <span className="text-xs text-zinc-400 font-medium">errors</span></span>
          </div>
        </div>
      </div>

    </div>
  );
});

export default Feed;
