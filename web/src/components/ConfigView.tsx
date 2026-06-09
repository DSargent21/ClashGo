import React from 'react';

interface ConfigViewProps {
  goldThreshold: number;
  setGoldThreshold: (v: number) => void;
  elixirThreshold: number;
  setElixirThreshold: (v: number) => void;
  deThreshold: number;
  setDeThreshold: (v: number) => void;
  selectedStrategy: string;
  setSelectedStrategy: (v: string) => void;
  strategies: string[];
  searchEnabled: boolean;
  setSearchEnabled: (v: boolean) => void;
  upgradeWalls: boolean;
  setUpgradeWalls: (v: boolean) => void;
  onSave: (e: React.FormEvent) => void;
}

const ConfigView: React.FC<ConfigViewProps> = React.memo(({
  goldThreshold, setGoldThreshold,
  elixirThreshold, setElixirThreshold,
  deThreshold, setDeThreshold,
  selectedStrategy, setSelectedStrategy,
  strategies,
  searchEnabled, setSearchEnabled,
  upgradeWalls, setUpgradeWalls,
  onSave
}) => {
  const [isOpen, setIsOpen] = React.useState(false);
  const dropdownRef = React.useRef<HTMLDivElement>(null);

  React.useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  return (
    <div className="max-w-4xl mx-auto">

      <form onSubmit={onSave} className="space-y-8">
        {/* Resource Thresholds */}
        <div className="bg-white dark:bg-zinc-900 p-8 rounded-[3rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none space-y-10 transition-all duration-500">
          <div className="flex justify-between items-start">
            <div>
              <h3 className="text-2xl font-bold text-zinc-950 dark:text-white mb-2 tracking-tight">Search Settings</h3>
              <p className="text-sm text-zinc-400 dark:text-zinc-500 font-medium">Minimum loot requirements for engagement.</p>
            </div>
          </div>
          
          <div className="grid grid-cols-1 md:grid-cols-2 gap-8">
            {[
              { label: 'Min Gold', value: goldThreshold, setter: setGoldThreshold, icon: 'monetization_on', color: 'text-amber-500', bg: 'bg-amber-500/10' },
              { label: 'Min Elixir', value: elixirThreshold, setter: setElixirThreshold, icon: 'water_drop', color: 'text-fuchsia-500', bg: 'bg-fuchsia-500/10' },
              { label: 'Min Dark Elixir', value: deThreshold, setter: setDeThreshold, icon: 'water_drop', color: 'text-zinc-950 dark:text-zinc-100', bg: 'bg-zinc-100 dark:bg-zinc-800' }
            ].map((item, idx) => (
              <div key={idx} className="space-y-4">
                <label className="flex items-center gap-3 text-[11px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em] px-1">
                  <div className={`w-8 h-8 rounded-xl ${item.bg} flex items-center justify-center border border-zinc-100/10`}>
                    <span className={`material-symbols-outlined text-base ${item.color}`}>{item.icon}</span>
                  </div>
                  {item.label}
                </label>
                <div className="relative group">
                  <input 
                    type="number" 
                    value={item.value} 
                    onChange={e => item.setter(parseInt(e.target.value) || 0)}
                    disabled={!searchEnabled}
                    className={`w-full bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl py-4 px-6 text-base font-bold text-zinc-900 dark:text-white focus:outline-none focus:ring-4 focus:ring-zinc-950/5 dark:focus:ring-white/5 focus:border-zinc-300 dark:focus:border-zinc-700 transition-all tabular-nums ${!searchEnabled ? 'opacity-30 cursor-not-allowed' : 'group-hover:bg-white dark:group-hover:bg-zinc-950/60'}`}
                  />
                </div>
              </div>
            ))}

            
            <div className="space-y-4">
              <label className="flex items-center gap-3 text-[11px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em] px-1">
                <div className="w-8 h-8 rounded-xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center text-zinc-400 dark:text-zinc-500 border border-zinc-100/10">
                  <span className="material-symbols-outlined text-base">precision_manufacturing</span>
                </div>
                Attack Strategy
              </label>
              <div className="relative" ref={dropdownRef}>
                <div 
                  onClick={() => searchEnabled && setIsOpen(!isOpen)}
                  className={`w-full bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl py-4 px-6 text-base font-bold text-zinc-900 dark:text-white cursor-pointer flex justify-between items-center transition-all ${isOpen ? 'ring-4 ring-zinc-950/5 dark:ring-white/5 border-zinc-300 dark:border-zinc-700 bg-white dark:bg-zinc-900' : 'hover:bg-white dark:hover:bg-zinc-900'} ${!searchEnabled ? 'opacity-30 cursor-not-allowed' : ''}`}
                >
                  <span className="truncate">
                    {selectedStrategy ? selectedStrategy.split('/').pop()?.replace('.yaml', '').replace('.csv', '') : 'Standard Protocol'}
                  </span>
                  <span className={`material-symbols-outlined text-zinc-400 transition-transform duration-500 ${isOpen ? 'rotate-180 text-zinc-950 dark:text-white' : ''}`}>
                    expand_more
                  </span>
                </div>

                {isOpen && searchEnabled && (
                  <div className="absolute top-[calc(100%+12px)] left-0 w-full bg-white dark:bg-zinc-900 border border-zinc-100 dark:border-zinc-800 rounded-2xl shadow-premium-lg dark:shadow-2xl z-50 py-3 overflow-hidden animate-in fade-in slide-in-from-top-2 duration-300">
                    {strategies.filter(s => s.includes('auto_edrag_rush') || s.includes('default')).map((s, idx) => {
                      const isActive = selectedStrategy.endsWith(s);
                      return (
                        <div 
                          key={idx}
                          onClick={() => { setSelectedStrategy(s); setIsOpen(false); }}
                          className={`px-6 py-3 text-sm font-bold cursor-pointer transition-colors ${isActive ? 'bg-zinc-50 dark:bg-zinc-800 text-zinc-950 dark:text-white' : 'text-zinc-500 dark:text-zinc-400 hover:bg-zinc-50 dark:hover:bg-zinc-800/50 hover:text-zinc-950 dark:hover:text-white'}`}
                        >
                          {s.replace('.yaml', '').replace('.csv', '')}
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            </div>
          </div>
        </div>

        {/* Operational Toggles */}
        <div className="bg-white dark:bg-zinc-900 p-8 rounded-[3rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none space-y-8 transition-all duration-500">
          <div className="flex items-center justify-between group cursor-pointer" onClick={() => setSearchEnabled(!searchEnabled)}>
            <div className="max-w-[80%]">
              <span className="block text-lg font-bold text-zinc-950 dark:text-white mb-1 tracking-tight">Enable Search</span>
              <span className="block text-sm text-zinc-400 dark:text-zinc-500 font-medium">Automatically skip bases that don't meet loot requirements.</span>
            </div>
            <div className={`w-14 h-7 rounded-full transition-all duration-500 relative ${searchEnabled ? 'bg-zinc-950 dark:bg-zinc-700' : 'bg-zinc-100 dark:bg-zinc-800'}`}>
               <div className={`absolute top-1 w-5 h-5 rounded-full transition-all duration-500 shadow-lg ${searchEnabled ? 'left-8 bg-white dark:bg-zinc-400' : 'left-1 bg-white dark:bg-zinc-500'}`}></div>
            </div>
          </div>

          <div className="h-px bg-zinc-50 dark:bg-zinc-800/50 w-full"></div>

          <div className="flex items-center justify-between group cursor-pointer" onClick={() => setUpgradeWalls(!upgradeWalls)}>
            <div className="max-w-[80%]">
              <span className="block text-lg font-bold text-zinc-950 dark:text-white mb-1 tracking-tight">Upgrade Walls</span>
              <span className="block text-sm text-zinc-400 dark:text-zinc-500 font-medium">Automatically use spare gold to upgrade walls.</span>
            </div>
            <div className={`w-14 h-7 rounded-full transition-all duration-500 relative ${upgradeWalls ? 'bg-zinc-950 dark:bg-zinc-700' : 'bg-zinc-100 dark:bg-zinc-800'}`}>
               <div className={`absolute top-1 w-5 h-5 rounded-full transition-all duration-500 shadow-lg ${upgradeWalls ? 'left-8 bg-white dark:bg-zinc-400' : 'left-1 bg-white dark:bg-zinc-500'}`}></div>
            </div>
          </div>
        </div>


        <div className="flex justify-end pt-4">
          <button 
            type="submit"
            className="h-16 px-12 bg-zinc-950 dark:bg-zinc-800 text-white dark:text-zinc-100 font-black text-xs uppercase tracking-[0.3em] rounded-3xl hover:bg-zinc-800 dark:hover:bg-zinc-700 transition-all shadow-premium-hover dark:shadow-none active:scale-[0.98] group flex items-center gap-4 border border-transparent dark:border-white/10"
          >
            Save Settings
            <span className="material-symbols-outlined text-lg group-hover:translate-x-1 transition-transform">save</span>
          </button>
        </div>
      </form>
    </div>
  );
});

export default ConfigView;
