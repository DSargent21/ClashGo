import React from 'react';
import { TabType } from '../types';
import logo from '../assets/images/clashgo-logo.png';

interface SidebarProps {
  tab: TabType;
  setTab: (tab: TabType) => void;
  expanded: boolean;
  setExpanded: (expanded: boolean) => void;
  running: boolean;
  onStart: () => void;
  onStop: () => void;
}

const Sidebar: React.FC<SidebarProps> = React.memo(({
  tab,
  setTab,
  expanded,
  setExpanded,
  running,
  onStart,
  onStop
}) => {
  const menuItems: { id: TabType; label: string; icon: string }[] = [
    { id: 'dashboard', label: 'Dashboard', icon: 'dashboard' },
    { id: 'analytics', label: 'Analytics', icon: 'monitoring' },
    { id: 'config', label: 'Config', icon: 'tune' },
    { id: 'settings', label: 'Settings', icon: 'settings' },
  ];

  return (
    <aside
      className={`fixed left-0 top-0 h-full bg-white dark:bg-zinc-900 border-r border-zinc-100/50 dark:border-zinc-800/50 z-50 transition-[width,background-color] duration-200 ease-out group overflow-hidden ${expanded ? 'w-64 shadow-premium-lg dark:shadow-none' : 'w-20'}`}
      style={{
        position: 'fixed',
        left: 0,
        top: 0,
        height: '100%',
        width: expanded ? '256px' : '80px',
        zIndex: 50,
        willChange: 'width'
      }}
      onMouseEnter={() => setExpanded(true)}
      onMouseLeave={() => setExpanded(false)}
    >
      <div className="draggable absolute top-0 left-0 right-0 h-8 z-10" />
      <div className="flex flex-col h-full py-8 px-4 relative z-20">
        <div className="flex items-center mb-10 px-0 overflow-hidden draggable">
          <div className="w-12 h-12 flex-shrink-0 flex items-center justify-center no-drag">
            <img
              src={logo}
              alt="ClashGO Logo"
              className="w-9 h-9 object-contain dark:invert"
            />
          </div>
          <div className={`transition-[opacity,transform] duration-200 ease-out ml-2 ${expanded ? 'opacity-100 translate-x-0' : 'opacity-0 -translate-x-2 w-0 overflow-hidden'}`}>
            <h1 className="font-headline text-lg font-bold tracking-tight whitespace-nowrap text-zinc-950 dark:text-white">Clash<span className="text-zinc-400 dark:text-zinc-500 font-medium ml-0.5">GO</span></h1>
          </div>
        </div>

        <nav className="space-y-1.5 flex-grow">
          {menuItems.map((item) => (
            <button
              key={item.id}
              onClick={() => setTab(item.id)}
              title={expanded ? undefined : item.label}
              aria-label={item.label}
              className={`w-full flex items-center h-11 rounded-xl transition-all duration-150 active:scale-[0.97] relative group/btn ${
                tab === item.id
                  ? 'bg-zinc-950 dark:bg-zinc-800 text-white dark:text-zinc-200 shadow-premium dark:shadow-none'
                  : 'text-zinc-500 dark:text-zinc-500 hover:bg-zinc-100/80 dark:hover:bg-zinc-800/40 hover:text-zinc-900 dark:hover:text-zinc-200'
              }`}
            >
              {tab === item.id && (
                <span className="absolute left-0 top-1/2 -translate-y-1/2 w-[3px] h-6 rounded-r-full bg-emerald-500 shadow-[0_0_10px_rgba(16,185,129,0.6)]" />
              )}
              <div className="w-11 h-11 flex-shrink-0 flex items-center justify-center">
                <span className={`material-symbols-outlined text-[20px] transition-transform duration-150 ${tab === item.id ? 'scale-110' : 'group-hover/btn:scale-110'}`}>
                  {item.icon}
                </span>
              </div>
              <span className={`text-[13px] font-bold tracking-tight transition-[opacity,transform] duration-200 ease-out whitespace-nowrap ${expanded ? 'opacity-100 translate-x-0' : 'opacity-0 -translate-x-2 w-0 overflow-hidden'}`}>
                {item.label}
              </span>
            </button>
          ))}
        </nav>

        <div className="mt-auto space-y-3">
          {/* Bot status footer — a persistent pulse even when the
              sidebar is collapsed, so the running state is visible
              at a glance from the icon rail. */}
          <div className="flex items-center gap-3 h-9 px-3 rounded-xl bg-zinc-50 dark:bg-zinc-800/40 border border-zinc-100/50 dark:border-zinc-800/50 overflow-hidden">
            <span className="relative flex w-2 h-2 shrink-0">
              <span className={`w-2 h-2 rounded-full ${running ? 'bg-emerald-500 animate-pulse' : 'bg-zinc-300 dark:bg-zinc-700'}`}></span>
              {running && <span className="absolute inset-0 w-2 h-2 rounded-full bg-emerald-500 animate-ping opacity-20"></span>}
            </span>
            <span className={`text-[10px] font-black uppercase tracking-[0.2em] whitespace-nowrap overflow-hidden transition-[opacity,width] duration-200 ease-out ${
              expanded ? 'opacity-100 w-auto' : 'opacity-0 w-0'
            } ${running ? 'text-emerald-600 dark:text-emerald-400' : 'text-zinc-400 dark:text-zinc-600'}`}>
              {running ? 'Bot Running' : 'Bot Idle'}
            </span>
          </div>

          <button
            onClick={running ? onStop : onStart}
            title={expanded ? undefined : (running ? 'Stop bot' : 'Start bot')}
            className={`w-full h-12 rounded-2xl font-black text-[10px] tracking-[0.2em] transition-all duration-200 flex items-center relative overflow-hidden group/start ${
              running
                ? 'bg-rose-50 dark:bg-rose-950/30 text-rose-600 dark:text-rose-400 hover:bg-rose-100 dark:hover:bg-rose-950/50 border border-rose-100 dark:border-rose-900/30'
                : 'bg-zinc-950 dark:bg-zinc-800 text-white dark:text-zinc-300 hover:bg-zinc-800 dark:hover:bg-zinc-700 shadow-premium dark:shadow-none border border-transparent dark:border-zinc-700/50'
            }`}
          >
            <div className="w-12 h-12 flex-shrink-0 flex items-center justify-center z-10 transition-transform duration-200 group-hover/start:scale-110">
              <span className="material-symbols-outlined text-[18px]">{running ? 'stop' : 'play_arrow'}</span>
            </div>
            <span className={`transition-[opacity,transform] duration-200 ease-out whitespace-nowrap z-10 ${expanded ? 'opacity-100 translate-x-0' : 'opacity-0 w-0 -translate-x-2 overflow-hidden'}`}>
              {running ? 'STOP BOT' : 'START BOT'}
            </span>
          </button>
        </div>
      </div>
    </aside>
  );
});

export default Sidebar;
