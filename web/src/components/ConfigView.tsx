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

      <form onSubmit={onSave} className="space-y-10">
        {/* Resource Thresholds */}
        <div className="bg-white p-10 rounded-[2.5rem] border border-zinc-100/50 shadow-premium space-y-10">
          <div className="flex justify-between items-start">
            <div>
              <h3 className="text-xl font-bold text-zinc-950 mb-1.5">Target Parameters</h3>
              <p className="text-xs text-zinc-400 font-medium">Minimum resources required for deployment.</p>
            </div>
          </div>
          
          <div className="grid grid-cols-1 md:grid-cols-2 gap-8">
            {[
              { label: 'Minimum Gold', value: goldThreshold, setter: setGoldThreshold, icon: 'payments', color: 'text-amber-500', bg: 'bg-amber-50/50' },
              { label: 'Minimum Elixir', value: elixirThreshold, setter: setElixirThreshold, icon: 'water_drop', color: 'text-fuchsia-500', bg: 'bg-fuchsia-50/50' },
              { label: 'Minimum Dark Elixir', value: deThreshold, setter: setDeThreshold, icon: 'database', color: 'text-zinc-950', bg: 'bg-zinc-100/50' }
            ].map((item, idx) => (
              <div key={idx} className="space-y-3">
                <label className="flex items-center gap-2.5 text-[10px] font-black text-zinc-400 uppercase tracking-widest px-1">
                  <div className={`w-6 h-6 rounded-lg ${item.bg} flex items-center justify-center`}>
                    <span className={`material-symbols-outlined text-sm ${item.color}`}>{item.icon}</span>
                  </div>
                  {item.label}
                </label>
                <div className="relative group">
                  <input 
                    type="number" 
                    value={item.value} 
                    onChange={e => item.setter(parseInt(e.target.value) || 0)}
                    disabled={!searchEnabled}
                    className={`w-full bg-zinc-50/50 border border-zinc-100 rounded-2xl py-4 px-5 text-sm font-bold text-zinc-900 focus:outline-none focus:ring-4 focus:ring-zinc-950/5 focus:border-zinc-300 transition-all tabular-nums ${!searchEnabled ? 'opacity-40 cursor-not-allowed' : 'group-hover:bg-white'}`}
                  />
                </div>
              </div>
            ))}

            
            <div className="space-y-3">
              <label className="flex items-center gap-2.5 text-[10px] font-black text-zinc-400 uppercase tracking-widest px-1">
                <div className="w-6 h-6 rounded-lg bg-zinc-50 flex items-center justify-center text-zinc-400">
                  <span className="material-symbols-outlined text-sm">precision_manufacturing</span>
                </div>
                Deployment Strategy
              </label>
              <div className="relative" ref={dropdownRef}>
                <div 
                  onClick={() => searchEnabled && setIsOpen(!isOpen)}
                  className={`w-full bg-zinc-50/50 border border-zinc-100 rounded-2xl py-4 px-5 text-sm font-bold text-zinc-900 cursor-pointer flex justify-between items-center transition-all ${isOpen ? 'ring-4 ring-zinc-950/5 border-zinc-300 bg-white' : 'hover:bg-white'} ${!searchEnabled ? 'opacity-40 cursor-not-allowed' : ''}`}
                >
                  <span className="truncate">
                    {selectedStrategy ? selectedStrategy.split('/').pop()?.replace('.yaml', '').replace('.csv', '') : 'Standard Protocol'}
                  </span>
                  <span className={`material-symbols-outlined text-zinc-400 transition-transform duration-300 ${isOpen ? 'rotate-180 text-zinc-950' : ''}`}>
                    expand_more
                  </span>
                </div>

                {isOpen && searchEnabled && (
                  <div className="absolute top-[calc(100%+8px)] left-0 w-full bg-white border border-zinc-100 rounded-2xl shadow-premium-lg z-50 py-2 overflow-hidden">
                    {strategies.filter(s => s.includes('auto_edrag_rush')).map((s, idx) => {
                      const isActive = selectedStrategy.endsWith(s);
                      return (
                        <div 
                          key={idx}
                          onClick={() => { setSelectedStrategy(s); setIsOpen(false); }}
                          className={`px-5 py-3 text-sm font-bold cursor-pointer transition-colors ${isActive ? 'bg-zinc-50 text-zinc-950' : 'text-zinc-500 hover:bg-zinc-50 hover:text-zinc-950'}`}
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
        <div className="bg-white p-10 rounded-[2.5rem] border border-zinc-100/50 shadow-premium space-y-8">
          <div className="flex items-center justify-between group cursor-pointer" onClick={() => setSearchEnabled(!searchEnabled)}>
            <div className="max-w-[80%]">
              <span className="block text-base font-bold text-zinc-950 mb-0.5">Matchmaking Filters</span>
              <span className="block text-xs text-zinc-400 font-medium">Verify loot requirements before attacking.</span>
            </div>
            <div className={`w-14 h-7 rounded-full transition-all duration-500 relative ${searchEnabled ? 'bg-zinc-950' : 'bg-zinc-100'}`}>
               <div className={`absolute top-1 w-5 h-5 rounded-full bg-white transition-all duration-500 shadow-sm ${searchEnabled ? 'left-8' : 'left-1'}`}></div>
            </div>
          </div>

          <div className="h-px bg-zinc-50 w-full"></div>

          <div className="flex items-center justify-between group cursor-pointer" onClick={() => setUpgradeWalls(!upgradeWalls)}>
            <div className="max-w-[80%]">
              <span className="block text-base font-bold text-zinc-950 mb-0.5">Wall Upgrades</span>
              <span className="block text-xs text-zinc-400 font-medium">Automatically upgrade walls when builders are idle.</span>
            </div>
            <div className={`w-14 h-7 rounded-full transition-all duration-500 relative ${upgradeWalls ? 'bg-zinc-950' : 'bg-zinc-100'}`}>
               <div className={`absolute top-1 w-5 h-5 rounded-full bg-white transition-all duration-500 shadow-sm ${upgradeWalls ? 'left-8' : 'left-1'}`}></div>
            </div>
          </div>
        </div>


        <div className="flex justify-end pt-4">
          <button 
            type="submit"
            className="h-16 px-12 bg-zinc-950 text-white font-black text-xs uppercase tracking-[0.2em] rounded-2xl hover:bg-zinc-800 transition-all shadow-premium-hover active:scale-[0.98] group flex items-center gap-3"
          >
            Deploy Configuration
            <span className="material-symbols-outlined text-sm group-hover:translate-x-1 transition-transform">bolt</span>
          </button>
        </div>
      </form>
    </div>
  );
});

export default ConfigView;
