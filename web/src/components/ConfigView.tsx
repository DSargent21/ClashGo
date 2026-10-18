import React from 'react';
import { SaveConfig } from '../../wailsjs/go/main/App';

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
  stallTimer: number;
  setStallTimer: (v: number) => void;
  armySlot: number;
  onSetArmySlot: (slot: number) => void;
  autoEnd: boolean;
  onSetAutoEnd: (v: boolean) => void;
  // Storage Protection & Maintenance
  haltGold: boolean;
  setHaltGold: (v: boolean) => void;
  haltElixir: boolean;
  setHaltElixir: (v: boolean) => void;
  haltDE: boolean;
  setHaltDE: (v: boolean) => void;
  storageThreshold: number;
  setStorageThreshold: (v: number) => void;
  dumpToWallsBeforeHalt: boolean;
  setDumpToWallsBeforeHalt: (v: boolean) => void;
  autoCollect: boolean;
  setAutoCollect: (v: boolean) => void;
  // 24/7 Schedule & Anti-Ban
  scheduleEnabled: boolean;
  setScheduleEnabled: (v: boolean) => void;
  activeHoursStart: string;
  setActiveHoursStart: (v: string) => void;
  activeHoursEnd: string;
  setActiveHoursEnd: (v: string) => void;
  sessionDuration: number;
  setSessionDuration: (v: number) => void;
  breakDuration: number;
  setBreakDuration: (v: number) => void;
  forceStopOnBreak: boolean;
  setForceStopOnBreak: (v: boolean) => void;
  maxPerHour: number;
  setMaxPerHour: (v: number) => void;
  maxPerDay: number;
  setMaxPerDay: (v: number) => void;
  // Build Menu 2.0 Upgrades & Filters
  autoBuilders: boolean;
  setAutoBuilders: (v: boolean) => void;
  reserveBuilders: number;
  setReserveBuilders: (v: number) => void;
  upgradeStrategy: string;
  setUpgradeStrategy: (v: string) => void;
  upgradeDefenses: boolean;
  setUpgradeDefenses: (v: boolean) => void;
  upgradeResources: boolean;
  setUpgradeResources: (v: boolean) => void;
  upgradeArmy: boolean;
  setUpgradeArmy: (v: boolean) => void;
  upgradeTraps: boolean;
  setUpgradeTraps: (v: boolean) => void;
  upgradeHeroes: boolean;
  setUpgradeHeroes: (v: boolean) => void;
  maxGoldPerUpgrade: number;
  setMaxGoldPerUpgrade: (v: number) => void;
  maxElixirPerUpgrade: number;
  setMaxElixirPerUpgrade: (v: number) => void;
  maxDEPerUpgrade: number;
  setMaxDEPerUpgrade: (v: number) => void;
  autoLab: boolean;
  setAutoLab: (v: boolean) => void;
  labStrategy: string;
  setLabStrategy: (v: string) => void;
  labAllowDE: boolean;
  setLabAllowDE: (v: boolean) => void;
  autoPets: boolean;
  setAutoPets: (v: boolean) => void;
  petPriorities: string[];
  setPetPriorities: (v: string[]) => void;
  wallResource: string;
  setWallResource: (v: string) => void;
  wallMinGoldReserve: number;
  setWallMinGoldReserve: (v: number) => void;
  wallMinElixirReserve: number;
  setWallMinElixirReserve: (v: number) => void;
  dryRun: boolean;
  setDryRun: (v: boolean) => void;
  // Returns the underlying SaveConfig promise so ConfigView can own
  // the save-status indicator (green flash / red flash + inline
  // "Saved!" / "Save failed" pill) and surface success or failure to
  // the user. Errors thrown by Wails are intentionally surfaced.
  onSave: () => Promise<void>;
}

type SaveStatus = 'idle' | 'saving' | 'saved' | 'error';

// Range bounds for the numeric fields. Out-of-range values get a red
// ring + aria-invalid so the user sees the problem before saving.
const THRESHOLD_MAX = 10_000_000;
const STALL_MAX = 600;
const invalid = (v: number, max: number): boolean =>
  !Number.isFinite(v) || v < 0 || v > max;

const ConfigView: React.FC<ConfigViewProps> = React.memo(({
  goldThreshold, setGoldThreshold,
  elixirThreshold, setElixirThreshold,
  deThreshold, setDeThreshold,
  selectedStrategy, setSelectedStrategy,
  strategies,
  searchEnabled, setSearchEnabled,
  upgradeWalls, setUpgradeWalls,
  stallTimer, setStallTimer,
  armySlot, onSetArmySlot,
  autoEnd, onSetAutoEnd,
  haltGold, setHaltGold,
  haltElixir, setHaltElixir,
  haltDE, setHaltDE,
  storageThreshold, setStorageThreshold,
  dumpToWallsBeforeHalt, setDumpToWallsBeforeHalt,
  autoCollect, setAutoCollect,
  scheduleEnabled, setScheduleEnabled,
  activeHoursStart, setActiveHoursStart,
  activeHoursEnd, setActiveHoursEnd,
  sessionDuration, setSessionDuration,
  breakDuration, setBreakDuration,
  forceStopOnBreak, setForceStopOnBreak,
  maxPerHour, setMaxPerHour,
  maxPerDay, setMaxPerDay,
  autoBuilders, setAutoBuilders,
  reserveBuilders, setReserveBuilders,
  upgradeStrategy, setUpgradeStrategy,
  upgradeDefenses, setUpgradeDefenses,
  upgradeResources, setUpgradeResources,
  upgradeArmy, setUpgradeArmy,
  upgradeTraps, setUpgradeTraps,
  upgradeHeroes, setUpgradeHeroes,
  maxGoldPerUpgrade, setMaxGoldPerUpgrade,
  maxElixirPerUpgrade, setMaxElixirPerUpgrade,
  maxDEPerUpgrade, setMaxDEPerUpgrade,
  autoLab, setAutoLab,
  labStrategy, setLabStrategy,
  labAllowDE, setLabAllowDE,
  autoPets, setAutoPets,
  petPriorities, setPetPriorities,
  wallResource, setWallResource,
  wallMinGoldReserve, setWallMinGoldReserve,
  wallMinElixirReserve, setWallMinElixirReserve,
  dryRun, setDryRun,
  onSave
}) => {
  const [isOpen, setIsOpen] = React.useState(false);
  const [saveStatus, setSaveStatus] = React.useState<SaveStatus>('idle');
  const [lastSaveError, setLastSaveError] = React.useState<string | null>(null);
  const dropdownRef = React.useRef<HTMLDivElement>(null);
  const savedTimerRef = React.useRef<number | null>(null);
  const errorTimerRef = React.useRef<number | null>(null);

  // Sub-navigation tabs (Attack & Search, Upgrades, Lab & Pets, Schedule, Storage)
  type ConfigSubTab = 'attack' | 'upgrades' | 'lab_pets' | 'schedule' | 'storage';
  const [activeTab, setActiveTab] = React.useState<ConfigSubTab>('attack');

  // All known Clash of Clans Pets for interactive reordering & addition
  const allKnownPets = React.useMemo(() => [
    'Diggy', 'Unicorn', 'Frosty', 'Phoenix', 'Spirit Fox', 'Angry Jelly', 'L.A.S.S.I', 'Electro Owl', 'Mighty Yak', 'Poison Lizard'
  ], []);

  // Move pet priority up or down
  const movePet = (index: number, direction: 'up' | 'down') => {
    const targetIdx = direction === 'up' ? index - 1 : index + 1;
    if (targetIdx < 0 || targetIdx >= petPriorities.length) return;
    const next = [...petPriorities];
    const temp = next[index];
    next[index] = next[targetIdx];
    next[targetIdx] = temp;
    setPetPriorities(next);
  };

  const removePet = (petName: string) => {
    setPetPriorities(petPriorities.filter(p => p !== petName));
  };

  const addPet = (petName: string) => {
    if (!petPriorities.includes(petName)) {
      setPetPriorities([...petPriorities, petName]);
    }
  };

  React.useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => {
      document.removeEventListener('mousedown', handleClickOutside);
      if (savedTimerRef.current) window.clearTimeout(savedTimerRef.current);
      if (errorTimerRef.current) window.clearTimeout(errorTimerRef.current);
    };
  }, []);

  // Form submit handler — owns the save-state transitions so the user
  // can see the result of their click (green ✓ flash on success,
  // red ✕ flash + inline error on failure). Errors thrown by the
  // Wails SaveConfig IPC are surfaced here rather than being
  // swallowed by App.tsx.
  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (saveStatus === 'saving') return;
    if (savedTimerRef.current) { window.clearTimeout(savedTimerRef.current); savedTimerRef.current = null; }
    if (errorTimerRef.current) { window.clearTimeout(errorTimerRef.current); errorTimerRef.current = null; }
    setSaveStatus('saving');
    setLastSaveError(null);
    try {
      await onSave();
      setSaveStatus('saved');
      savedTimerRef.current = window.setTimeout(() => {
        setSaveStatus('idle');
        savedTimerRef.current = null;
      }, 1800);
    } catch (err) {
      setSaveStatus('error');
      setLastSaveError(err instanceof Error ? err.message : String(err));
      errorTimerRef.current = window.setTimeout(() => {
        setSaveStatus('idle');
        setLastSaveError(null);
        errorTimerRef.current = null;
      }, 3000);
    }
  };

  // Visual state for the submit button.
  const saveButtonClasses =
    saveStatus === 'saved'
      ? 'bg-emerald-500 text-white shadow-emerald-500/40'
      : saveStatus === 'error'
        ? 'bg-rose-500 text-white shadow-rose-500/40'
        : saveStatus === 'saving'
          ? 'bg-zinc-700 text-white cursor-wait'
          : 'bg-zinc-950 dark:bg-zinc-800 text-white dark:text-zinc-100 hover:bg-zinc-800 dark:hover:bg-zinc-700 shadow-premium-hover dark:shadow-none';

  const saveButtonIcon =
    saveStatus === 'saving'
      ? 'progress_activity'
      : saveStatus === 'saved'
        ? 'check_circle'
        : saveStatus === 'error'
          ? 'error'
          : 'save';

  const saveButtonLabel =
    saveStatus === 'saving'
      ? 'Saving…'
      : saveStatus === 'saved'
        ? 'Saved'
        : saveStatus === 'error'
          ? 'Failed'
          : 'Save Settings';

  const thresholdItems = [
    { label: 'Min Gold', value: goldThreshold, setter: setGoldThreshold, icon: 'monetization_on', color: 'text-amber-500', bg: 'bg-amber-500/10' },
    { label: 'Min Elixir', value: elixirThreshold, setter: setElixirThreshold, icon: 'water_drop', color: 'text-fuchsia-500', bg: 'bg-fuchsia-500/10' },
    { label: 'Min Dark Elixir', value: deThreshold, setter: setDeThreshold, icon: 'water_drop', color: 'text-zinc-950 dark:text-zinc-100', bg: 'bg-zinc-100 dark:bg-zinc-800' },
  ];

  const stallInvalid = invalid(stallTimer, STALL_MAX);
  const anyInvalid = stallInvalid || thresholdItems.some((t) => invalid(t.value, THRESHOLD_MAX));

  return (
    <div className="max-w-4xl mx-auto space-y-8">
      {/* Segmented Sub-Navigation Header & Quick Save */}
      <div className="sticky top-0 z-30 pt-1 pb-4 bg-zinc-50/90 dark:bg-zinc-950/90 backdrop-blur-xl transition-all">
        <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-4 p-2 bg-white/80 dark:bg-zinc-900/80 rounded-[2rem] border border-zinc-100 dark:border-zinc-800 shadow-premium dark:shadow-none">
          {/* Navigation Pills */}
          <nav className="flex items-center gap-1.5 overflow-x-auto p-1 no-scrollbar scrollbar-none flex-1 min-w-0" aria-label="Configuration Sections">
            {[
              { id: 'attack', label: 'Attack & Loot', icon: 'swords' },
              { id: 'upgrades', label: 'Builders', icon: 'construction' },
              { id: 'lab_pets', label: 'Lab & Pets', icon: 'experiment' },
              { id: 'schedule', label: 'Schedule', icon: 'schedule' },
              { id: 'storage', label: 'Storage & Walls', icon: 'inventory_2' },
            ].map(tab => {
              const isSelected = activeTab === tab.id;
              return (
                <button
                  key={tab.id}
                  type="button"
                  onClick={() => setActiveTab(tab.id as ConfigSubTab)}
                  className={`px-3.5 py-2.5 rounded-2xl text-xs font-bold transition-all flex items-center gap-2 whitespace-nowrap shrink-0 ${
                    isSelected
                      ? 'bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950 shadow-md'
                      : 'text-zinc-500 hover:text-zinc-900 dark:hover:text-white hover:bg-zinc-100/60 dark:hover:bg-zinc-800/60'
                  }`}
                >
                  <span className="material-symbols-outlined text-base">{tab.icon}</span>
                  <span>{tab.label}</span>
                </button>
              );
            })}
          </nav>

          {/* Quick Save Pill */}
          <div className="flex items-center gap-3 px-2 shrink-0">
            {anyInvalid && (
              <span className="text-[11px] font-bold text-amber-500 flex items-center gap-1" title="Values out of range">
                <span className="material-symbols-outlined text-sm">warning</span>
              </span>
            )}
            <button
              type="button"
              onClick={handleSubmit}
              disabled={saveStatus === 'saving' || anyInvalid}
              className={`h-11 px-5 rounded-2xl font-black text-xs uppercase tracking-wider transition-all flex items-center gap-2 shadow-sm disabled:opacity-40 disabled:cursor-not-allowed ${saveButtonClasses}`}
            >
              <span>{saveButtonLabel}</span>
              <span className={`material-symbols-outlined text-base ${saveStatus === 'saving' ? 'animate-spin' : ''}`}>
                {saveButtonIcon}
              </span>
            </button>
          </div>
        </div>
      </div>

      <form onSubmit={handleSubmit} className="space-y-8">
        {/* TAB 1: ATTACK & LOOT */}
        {activeTab === 'attack' && (
          <div className="space-y-8 animate-fadeIn">
            {/* Resource Thresholds */}
            <div className="bg-white dark:bg-zinc-900 p-8 rounded-[3rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none space-y-10 transition-all duration-500">
              <div className="flex justify-between items-start">
                <div>
                  <h3 className="text-2xl font-bold text-zinc-950 dark:text-white mb-2 tracking-tight">Search Settings</h3>
                  <p className="text-sm text-zinc-500 dark:text-zinc-500 font-medium">Minimum loot requirements for engagement.</p>
                </div>
                {!searchEnabled && (
                  <div className="px-4 py-2 rounded-xl bg-amber-500/10 border border-amber-500/30 text-[10px] font-black text-amber-600 dark:text-amber-400 uppercase tracking-[0.2em] whitespace-nowrap">
                    Disabled
                  </div>
                )}
              </div>

          {/* Thresholds — three across on md+ */}
          <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
            {thresholdItems.map((item, idx) => {
              const itemInvalid = invalid(item.value, THRESHOLD_MAX);
              return (
                <div key={idx} className="space-y-4">
                  <label className="flex items-center gap-3 text-[11px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em] px-1">
                    <div className={`w-8 h-8 rounded-xl ${item.bg} flex items-center justify-center border border-zinc-100/10`}>
                      <span className={`material-symbols-outlined text-base ${item.color}`}>{item.icon}</span>
                    </div>
                    {item.label}
                  </label>
                  <div className="relative group">
                    <input
                      type="number"
                      inputMode="numeric"
                      min={0}
                      max={THRESHOLD_MAX}
                      value={item.value}
                      onChange={e => item.setter(parseInt(e.target.value) || 0)}
                      disabled={!searchEnabled}
                      aria-invalid={itemInvalid}
                      className={`w-full bg-zinc-50/50 dark:bg-zinc-950/40 border rounded-2xl py-4 px-6 text-base font-bold text-zinc-900 dark:text-white focus:outline-none focus:ring-4 transition-all tabular-nums ${
                        itemInvalid
                          ? 'border-rose-400/60 focus:border-rose-500 focus:ring-rose-500/10'
                          : 'border-zinc-100 dark:border-zinc-800 focus:ring-zinc-950/5 dark:focus:ring-white/5 focus:border-zinc-300 dark:focus:border-zinc-700'
                      } ${!searchEnabled ? 'opacity-30 cursor-not-allowed' : 'group-hover:bg-white dark:group-hover:bg-zinc-950/60'}`}
                    />
                  </div>
                  {itemInvalid && (
                    <p className="px-1 text-[10px] font-bold text-rose-500 uppercase tracking-widest" role="alert">
                      Max {THRESHOLD_MAX.toLocaleString()}
                    </p>
                  )}
                </div>
              );
            })}
          </div>

          {/* Strategy + stall timer — two across on md+ */}
          <div className="grid grid-cols-1 md:grid-cols-2 gap-8">
            <div className="space-y-4">
              <div className="flex items-center justify-between px-1">
                <label className="flex items-center gap-3 text-[11px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em]">
                  <div className="w-8 h-8 rounded-xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center text-zinc-500 dark:text-zinc-500 border border-zinc-100/10">
                    <span className="material-symbols-outlined text-base">precision_manufacturing</span>
                  </div>
                  Attack Strategy
                </label>
                <span className="text-[10px] font-black text-zinc-400 dark:text-zinc-600 uppercase tracking-widest tabular-nums">
                  {(strategies ?? []).length} available
                </span>
              </div>
              <div className="relative" ref={dropdownRef}>
                <div
                  role="combobox"
                  aria-expanded={isOpen}
                  aria-haspopup="listbox"
                  tabIndex={0}
                  onClick={() => setIsOpen(!isOpen)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' || e.key === ' ' || e.key === 'ArrowDown') {
                      e.preventDefault();
                      setIsOpen(true);
                    } else if (e.key === 'Escape') {
                      setIsOpen(false);
                    }
                  }}
                  className={`w-full bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl py-4 px-6 text-base font-bold text-zinc-900 dark:text-white cursor-pointer flex justify-between items-center transition-all ${isOpen ? 'ring-4 ring-zinc-950/5 dark:ring-white/5 border-zinc-300 dark:border-zinc-700 bg-white dark:bg-zinc-900' : 'hover:bg-white dark:hover:bg-zinc-900'}`}
                >
                  <span className="truncate">
                    {selectedStrategy ? selectedStrategy.split('/').pop()?.replace('.yaml', '').replace('.csv', '') : 'Standard Protocol'}
                  </span>
                  <span className={`material-symbols-outlined text-zinc-500 transition-transform duration-500 ${isOpen ? 'rotate-180 text-zinc-950 dark:text-white' : ''}`}>
                    expand_more
                  </span>
                </div>

                {isOpen && (
                  <div role="listbox" className="dropdown-pop absolute top-[calc(100%+12px)] left-0 w-full bg-white dark:bg-zinc-900 border border-zinc-100 dark:border-zinc-800 rounded-2xl shadow-premium-lg dark:shadow-2xl z-50 py-3 max-h-72 overflow-y-auto">
                    {(strategies ?? []).map((s, idx) => {
                      const isActive = !!selectedStrategy && selectedStrategy.endsWith(s);
                      return (
                        <div
                          key={idx}
                          role="option"
                          aria-selected={isActive}
                          onClick={() => {
                            setSelectedStrategy(s);
                            setIsOpen(false);
                            // Ponytail: auto-save immediately on strategy switch so user doesn't have to hit "Save Settings"
                            SaveConfig(
                              goldThreshold,
                              elixirThreshold,
                              deThreshold,
                              upgradeWalls,
                              s,
                              searchEnabled,
                              stallTimer
                            ).catch(e => console.error('Failed to auto-save strategy:', e));
                          }}
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

            <div className="space-y-4">
              <label className="flex items-center gap-3 text-[11px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em] px-1">
                <div className="w-8 h-8 rounded-xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center text-zinc-500 dark:text-zinc-500 border border-zinc-100/10">
                  <span className="material-symbols-outlined text-base">timer</span>
                </div>
                Stall Timer (Seconds)
              </label>
              <div className="relative group">
                <input
                  type="number"
                  inputMode="numeric"
                  min={0}
                  max={STALL_MAX}
                  value={stallTimer}
                  onChange={e => setStallTimer(parseInt(e.target.value) || 0)}
                  aria-invalid={stallInvalid}
                  placeholder="0 to disable"
                  className={`w-full bg-zinc-50/50 dark:bg-zinc-950/40 border rounded-2xl py-4 px-6 text-base font-bold text-zinc-900 dark:text-white focus:outline-none focus:ring-4 transition-all tabular-nums ${
                    stallInvalid
                      ? 'border-rose-400/60 focus:border-rose-500 focus:ring-rose-500/10'
                      : 'border-zinc-100 dark:border-zinc-800 focus:ring-zinc-950/5 dark:focus:ring-white/5 focus:border-zinc-300 dark:focus:border-zinc-700'
                  } group-hover:bg-white dark:group-hover:bg-zinc-950/60`}
                />
              </div>
              {stallInvalid && (
                <p className="px-1 text-[10px] font-bold text-rose-500 uppercase tracking-widest" role="alert">
                  Max {STALL_MAX}s
                </p>
              )}
            </div>
          </div>

          {/* Army Menu Recipe Slot selector */}
          <div className="space-y-4 pt-2 border-t border-zinc-100 dark:border-zinc-800/60">
            <div className="flex items-center justify-between px-1">
              <label className="flex items-center gap-3 text-[11px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em]">
                <div className="w-8 h-8 rounded-xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center text-zinc-500 dark:text-zinc-500 border border-zinc-100/10">
                  <span className="material-symbols-outlined text-base">view_carousel</span>
                </div>
                Army Recipe Slot (In-Game Menu)
              </label>
              <span className="text-[10px] font-black text-zinc-400 dark:text-zinc-600 uppercase tracking-widest">
                Slot {armySlot} Active
              </span>
            </div>
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
              {[1, 2, 3, 4].map((slotNum) => {
                const isSelected = armySlot === slotNum;
                return (
                  <button
                    key={slotNum}
                    type="button"
                    onClick={() => onSetArmySlot(slotNum)}
                    className={`py-3.5 px-4 rounded-2xl font-bold text-sm transition-all flex items-center justify-center gap-2.5 border ${
                      isSelected
                        ? 'bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950 border-zinc-950 dark:border-white shadow-premium'
                        : 'bg-zinc-50/50 dark:bg-zinc-950/40 text-zinc-600 dark:text-zinc-400 border-zinc-100 dark:border-zinc-800 hover:bg-white dark:hover:bg-zinc-900 hover:text-zinc-900 dark:hover:text-white'
                    }`}
                  >
                    <span className="material-symbols-outlined text-base">
                      {isSelected ? 'check_circle' : 'tag'}
                    </span>
                    <span>Slot {slotNum}</span>
                  </button>
                );
              })}
            </div>
            <p className="px-1 text-xs text-zinc-500 dark:text-zinc-500">
              Selects which saved army recipe ClashGO taps in the train menu for this strategy (e.g. Slot 4 for Valk Spam, Slot 1 for E-Drag).
            </p>
          </div>
        </div>

        {/* Operational Toggles */}
        <div className="bg-white dark:bg-zinc-900 p-8 rounded-[3rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none space-y-8 transition-all duration-500">
          <button
            type="button"
            role="switch"
            aria-checked={searchEnabled}
            onClick={() => setSearchEnabled(!searchEnabled)}
            className="w-full flex items-center justify-between group cursor-pointer text-left"
          >
            <div className="max-w-[80%]">
              <span className="block text-lg font-bold text-zinc-950 dark:text-white mb-1 tracking-tight">Enable Search</span>
              <span className="block text-sm text-zinc-500 dark:text-zinc-500 font-medium">Automatically skip bases that don't meet loot requirements.</span>
            </div>
            <div className={`w-14 h-7 rounded-full transition-all duration-500 relative shrink-0 ${searchEnabled ? 'bg-emerald-500/80' : 'bg-zinc-200 dark:bg-zinc-800'}`}>
               <div className={`absolute top-1 w-5 h-5 rounded-full transition-all duration-500 shadow-lg ${searchEnabled ? 'left-8 bg-white' : 'left-1 bg-white dark:bg-zinc-500'}`}></div>
            </div>
          </button>

          <div className="h-px bg-zinc-50 dark:bg-zinc-800/50 w-full"></div>

          <button
            type="button"
            role="switch"
            aria-checked={autoEnd}
            onClick={() => onSetAutoEnd(!autoEnd)}
            className="w-full flex items-center justify-between group cursor-pointer text-left"
          >
            <div className="max-w-[80%]">
              <span className="block text-lg font-bold text-zinc-950 dark:text-white mb-1 tracking-tight">Auto End Battle (50% Damage)</span>
              <span className="block text-sm text-zinc-500 dark:text-zinc-500 font-medium">Surrender immediately once 50% damage (1 star) is secured to save time and troops.</span>
            </div>
            <div className={`w-14 h-7 rounded-full transition-all duration-500 relative shrink-0 ${autoEnd ? 'bg-emerald-500/80' : 'bg-zinc-200 dark:bg-zinc-800'}`}>
               <div className={`absolute top-1 w-5 h-5 rounded-full transition-all duration-500 shadow-lg ${autoEnd ? 'left-8 bg-white' : 'left-1 bg-white dark:bg-zinc-500'}`}></div>
            </div>
          </button>
        </div>
      </div>
    )}

    {/* TAB 5: STORAGE & WALLS */}
    {activeTab === 'storage' && (
      <div className="space-y-8 animate-fadeIn">
        {/* Storage Protection */}
        <div className="bg-white dark:bg-zinc-900 p-8 rounded-[3rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none space-y-8 transition-all duration-500">
          <div className="flex justify-between items-start">
            <div>
              <h3 className="text-2xl font-bold text-zinc-950 dark:text-white mb-2 tracking-tight">Storage Protection</h3>
              <p className="text-sm text-zinc-500 dark:text-zinc-500 font-medium">Auto-halt attacks when village storages reach full capacity to avoid wasting loot.</p>
            </div>
            <div className="w-10 h-10 rounded-2xl bg-amber-500/10 flex items-center justify-center text-amber-500 border border-amber-500/20">
              <span className="material-symbols-outlined text-xl">inventory_2</span>
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
            <button
              type="button"
              role="switch"
              aria-checked={haltGold}
              onClick={() => setHaltGold(!haltGold)}
              className="p-5 rounded-2xl bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 text-left flex justify-between items-center group hover:bg-white dark:hover:bg-zinc-900 transition-all"
            >
              <div>
                <span className="block text-sm font-bold text-zinc-900 dark:text-white">Halt on Gold</span>
                <span className="block text-xs text-zinc-500">Stop when Gold full</span>
              </div>
              <div className={`w-11 h-6 rounded-full transition-all duration-300 relative shrink-0 ${haltGold ? 'bg-amber-500' : 'bg-zinc-300 dark:bg-zinc-700'}`}>
                <div className={`absolute top-1 w-4 h-4 rounded-full transition-all duration-300 shadow ${haltGold ? 'left-6 bg-white' : 'left-1 bg-white'}`}></div>
              </div>
            </button>

            <button
              type="button"
              role="switch"
              aria-checked={haltElixir}
              onClick={() => setHaltElixir(!haltElixir)}
              className="p-5 rounded-2xl bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 text-left flex justify-between items-center group hover:bg-white dark:hover:bg-zinc-900 transition-all"
            >
              <div>
                <span className="block text-sm font-bold text-zinc-900 dark:text-white">Halt on Elixir</span>
                <span className="block text-xs text-zinc-500">Stop when Elixir full</span>
              </div>
              <div className={`w-11 h-6 rounded-full transition-all duration-300 relative shrink-0 ${haltElixir ? 'bg-fuchsia-500' : 'bg-zinc-300 dark:bg-zinc-700'}`}>
                <div className={`absolute top-1 w-4 h-4 rounded-full transition-all duration-300 shadow ${haltElixir ? 'left-6 bg-white' : 'left-1 bg-white'}`}></div>
              </div>
            </button>

            <button
              type="button"
              role="switch"
              aria-checked={haltDE}
              onClick={() => setHaltDE(!haltDE)}
              className="p-5 rounded-2xl bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 text-left flex justify-between items-center group hover:bg-white dark:hover:bg-zinc-900 transition-all"
            >
              <div>
                <span className="block text-sm font-bold text-zinc-900 dark:text-white">Halt on Dark Elixir</span>
                <span className="block text-xs text-zinc-500">Stop when DE full</span>
              </div>
              <div className={`w-11 h-6 rounded-full transition-all duration-300 relative shrink-0 ${haltDE ? 'bg-zinc-700 dark:bg-zinc-500' : 'bg-zinc-300 dark:bg-zinc-700'}`}>
                <div className={`absolute top-1 w-4 h-4 rounded-full transition-all duration-300 shadow ${haltDE ? 'left-6 bg-white' : 'left-1 bg-white'}`}></div>
              </div>
            </button>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-8 pt-2 border-t border-zinc-100 dark:border-zinc-800/60">
            <div className="space-y-4">
              <label className="flex items-center gap-3 text-[11px] font-black text-zinc-500 uppercase tracking-[0.2em] px-1">
                <span className="material-symbols-outlined text-base">percent</span>
                Full Storage Threshold (%)
              </label>
              <input
                type="number"
                min={50}
                max={100}
                value={storageThreshold}
                onChange={e => setStorageThreshold(Math.max(50, Math.min(100, parseInt(e.target.value) || 95)))}
                className="w-full bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl py-4 px-6 text-base font-bold text-zinc-900 dark:text-white tabular-nums"
              />
              <p className="px-1 text-xs text-zinc-500">Trigger halt when fill level reaches this percent (default 95%).</p>
            </div>

            <div className="space-y-4">
              <button
                type="button"
                role="switch"
                aria-checked={upgradeWalls}
                onClick={() => {
                  const next = !upgradeWalls;
                  setUpgradeWalls(next);
                  setDumpToWallsBeforeHalt(next);
                }}
                className="w-full flex items-center justify-between p-4 rounded-2xl bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 text-left group hover:bg-white dark:hover:bg-zinc-900 transition-all"
              >
                <div className="max-w-[75%]">
                  <span className="block text-sm font-bold text-zinc-900 dark:text-white">Auto Upgrade Walls (Loot Dumping)</span>
                  <span className="block text-xs text-zinc-500">Spend excess gold & elixir on walls before storage halts</span>
                </div>
                <div className={`w-11 h-6 rounded-full transition-all duration-300 relative shrink-0 ${upgradeWalls ? 'bg-emerald-500' : 'bg-zinc-300 dark:bg-zinc-700'}`}>
                  <div className={`absolute top-1 w-4 h-4 rounded-full transition-all duration-300 shadow ${upgradeWalls ? 'left-6 bg-white' : 'left-1 bg-white'}`}></div>
                </div>
              </button>

              <button
                type="button"
                role="switch"
                aria-checked={autoCollect}
                onClick={() => setAutoCollect(!autoCollect)}
                className="w-full flex items-center justify-between p-4 rounded-2xl bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 text-left group hover:bg-white dark:hover:bg-zinc-900 transition-all"
              >
                <div className="max-w-[75%]">
                  <span className="block text-sm font-bold text-zinc-900 dark:text-white">Auto-Collect Resources</span>
                  <span className="block text-xs text-zinc-500">Regularly tap Mines, Collectors, Drills, and Cart</span>
                </div>
                <div className={`w-11 h-6 rounded-full transition-all duration-300 relative shrink-0 ${autoCollect ? 'bg-emerald-500' : 'bg-zinc-300 dark:bg-zinc-700'}`}>
                  <div className={`absolute top-1 w-4 h-4 rounded-full transition-all duration-300 shadow ${autoCollect ? 'left-6 bg-white' : 'left-1 bg-white'}`}></div>
                </div>
              </button>
            </div>
          </div>

          {/* Wall Reserve Customization */}
          {(upgradeWalls || dumpToWallsBeforeHalt) && (
            <div className="p-6 rounded-2xl bg-zinc-50/30 dark:bg-zinc-950/20 border border-zinc-100 dark:border-zinc-800/60 space-y-4">
              <span className="block text-xs font-black text-zinc-500 uppercase tracking-widest">Wall Dumping Reserves</span>
              <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                <div>
                  <label className="text-[10px] text-zinc-400 font-bold block mb-1.5 uppercase tracking-wider">Wall Resource</label>
                  <div className="relative">
                    <select
                      value={wallResource}
                      onChange={e => setWallResource(e.target.value)}
                      className="w-full h-12 appearance-none bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 pr-10 text-sm font-bold text-zinc-900 dark:text-white focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700 cursor-pointer"
                    >
                      <option value="both">Gold & Elixir</option>
                      <option value="gold">Gold Only</option>
                      <option value="elixir">Elixir Only</option>
                    </select>
                    <span className="material-symbols-outlined text-zinc-400 absolute right-3 top-1/2 -translate-y-1/2 pointer-events-none text-xl">
                      expand_more
                    </span>
                  </div>
                </div>
                <div>
                  <label className="text-[10px] text-zinc-400 font-bold block mb-1.5 uppercase tracking-wider">Min Gold Reserve</label>
                  <input
                    type="number"
                    value={wallMinGoldReserve}
                    onChange={e => setWallMinGoldReserve(parseInt(e.target.value) || 0)}
                    className="w-full h-12 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 text-sm font-bold text-zinc-900 dark:text-white tabular-nums focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700"
                  />
                </div>
                <div>
                  <label className="text-[10px] text-zinc-400 font-bold block mb-1.5 uppercase tracking-wider">Min Elixir Reserve</label>
                  <input
                    type="number"
                    value={wallMinElixirReserve}
                    onChange={e => setWallMinElixirReserve(parseInt(e.target.value) || 0)}
                    className="w-full h-12 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 text-sm font-bold text-zinc-900 dark:text-white tabular-nums focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700"
                  />
                </div>
              </div>
            </div>
          )}
        </div>
      </div>
    )}

    {/* TAB 4: 24/7 SCHEDULE & ANTI-BAN */}
    {activeTab === 'schedule' && (
      <div className="space-y-8 animate-fadeIn">
        {/* 24/7 Schedule & Anti-Ban Humanization */}
        <div className="bg-white dark:bg-zinc-900 p-8 rounded-[3rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none space-y-8 transition-all duration-500">
          <div className="flex justify-between items-start">
            <div>
              <h3 className="text-2xl font-bold text-zinc-950 dark:text-white mb-2 tracking-tight">24/7 Schedule & Anti-Ban</h3>
              <p className="text-sm text-zinc-500 dark:text-zinc-500 font-medium">Humanized session pacing, natural breaks, and offline simulation for 24/7 botting.</p>
            </div>
            <button
              type="button"
              role="switch"
              aria-checked={scheduleEnabled}
              onClick={() => setScheduleEnabled(!scheduleEnabled)}
              className={`w-14 h-7 rounded-full transition-all duration-500 relative shrink-0 ${scheduleEnabled ? 'bg-emerald-500/80' : 'bg-zinc-200 dark:bg-zinc-800'}`}
            >
              <div className={`absolute top-1 w-5 h-5 rounded-full transition-all duration-500 shadow-lg ${scheduleEnabled ? 'left-8 bg-white' : 'left-1 bg-white dark:bg-zinc-500'}`}></div>
            </button>
          </div>

          <div className={`space-y-6 transition-all duration-300 ${!scheduleEnabled ? 'opacity-40 pointer-events-none' : ''}`}>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
              <div className="space-y-3">
                <label className="flex items-center gap-2 text-[11px] font-black text-zinc-500 uppercase tracking-[0.2em] px-1">
                  <span className="material-symbols-outlined text-base">schedule</span>
                  Active Hours Window
                </label>
                <div className="grid grid-cols-2 gap-3">
                  <div>
                    <span className="text-[10px] text-zinc-400 font-bold block mb-1.5 uppercase tracking-wider">Start (HH:MM)</span>
                    <input
                      type="time"
                      value={activeHoursStart}
                      onChange={e => setActiveHoursStart(e.target.value)}
                      className="w-full h-12 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 text-sm font-bold text-zinc-900 dark:text-white focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700"
                    />
                  </div>
                  <div>
                    <span className="text-[10px] text-zinc-400 font-bold block mb-1.5 uppercase tracking-wider">End (HH:MM)</span>
                    <input
                      type="time"
                      value={activeHoursEnd}
                      onChange={e => setActiveHoursEnd(e.target.value)}
                      className="w-full h-12 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 text-sm font-bold text-zinc-900 dark:text-white focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700"
                    />
                  </div>
                </div>
              </div>

              <div className="space-y-3">
                <label className="flex items-center gap-2 text-[11px] font-black text-zinc-500 uppercase tracking-[0.2em] px-1">
                  <span className="material-symbols-outlined text-base">timelapse</span>
                  Session / Break Intervals (Minutes)
                </label>
                <div className="grid grid-cols-2 gap-3">
                  <div>
                    <span className="text-[10px] text-zinc-400 font-bold block mb-1.5 uppercase tracking-wider">Session Duration</span>
                    <input
                      type="number"
                      min={10}
                      max={360}
                      value={sessionDuration}
                      onChange={e => setSessionDuration(parseInt(e.target.value) || 60)}
                      className="w-full h-12 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 text-sm font-bold text-zinc-900 dark:text-white tabular-nums focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700"
                    />
                  </div>
                  <div>
                    <span className="text-[10px] text-zinc-400 font-bold block mb-1.5 uppercase tracking-wider">Break Duration</span>
                    <input
                      type="number"
                      min={5}
                      max={180}
                      value={breakDuration}
                      onChange={e => setBreakDuration(parseInt(e.target.value) || 20)}
                      className="w-full h-12 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 text-sm font-bold text-zinc-900 dark:text-white tabular-nums focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700"
                    />
                  </div>
                </div>
              </div>
            </div>

            <div className="grid grid-cols-1 md:grid-cols-3 gap-6 pt-2 border-t border-zinc-100 dark:border-zinc-800/60 items-end">
              <div className="space-y-2">
                <label className="flex items-center gap-2 text-[11px] font-black text-zinc-500 uppercase tracking-[0.2em] px-1">
                  <span className="material-symbols-outlined text-base">speed</span>
                  Max Attacks / Hour
                </label>
                <input
                  type="number"
                  min={1}
                  max={60}
                  value={maxPerHour}
                  onChange={e => setMaxPerHour(parseInt(e.target.value) || 6)}
                  className="w-full h-12 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 text-sm font-bold text-zinc-900 dark:text-white tabular-nums focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700"
                />
              </div>

              <div className="space-y-2">
                <label className="flex items-center gap-2 text-[11px] font-black text-zinc-500 uppercase tracking-[0.2em] px-1">
                  <span className="material-symbols-outlined text-base">event_note</span>
                  Max Attacks / Day
                </label>
                <input
                  type="number"
                  min={1}
                  max={300}
                  value={maxPerDay}
                  onChange={e => setMaxPerDay(parseInt(e.target.value) || 40)}
                  className="w-full h-12 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 text-sm font-bold text-zinc-900 dark:text-white tabular-nums focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700"
                />
              </div>

              <div className="space-y-2">
                <label className="text-[11px] font-black text-zinc-500 uppercase tracking-[0.2em] px-1 block opacity-0 pointer-events-none select-none">
                  Placeholder
                </label>
                <button
                  type="button"
                  role="switch"
                  aria-checked={forceStopOnBreak}
                  onClick={() => setForceStopOnBreak(!forceStopOnBreak)}
                  className="w-full h-12 px-4 rounded-2xl bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 text-left flex justify-between items-center group hover:bg-white dark:hover:bg-zinc-900 transition-all"
                >
                  <div className="max-w-[70%]">
                    <span className="block text-xs font-bold text-zinc-900 dark:text-white">Offline Simulation</span>
                    <span className="block text-[10px] text-zinc-500">Stop game during breaks</span>
                  </div>
                  <div className={`w-10 h-5 rounded-full transition-all duration-300 relative shrink-0 ${forceStopOnBreak ? 'bg-emerald-500' : 'bg-zinc-300 dark:bg-zinc-700'}`}>
                    <div className={`absolute top-0.5 w-4 h-4 rounded-full transition-all duration-300 shadow ${forceStopOnBreak ? 'left-5 bg-white' : 'left-0.5 bg-white'}`}></div>
                  </div>
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    )}

    {/* TAB 2: BUILDERS & UPGRADES */}
    {activeTab === 'upgrades' && (
      <div className="space-y-8 animate-fadeIn">
        <div className="bg-white dark:bg-zinc-900 p-8 rounded-[3rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none space-y-8 transition-all duration-500">
          <div className="flex justify-between items-start">
            <div>
              <h3 className="text-2xl font-bold text-zinc-950 dark:text-white mb-2 tracking-tight">Village Builders & Upgrades</h3>
              <p className="text-sm text-zinc-500 dark:text-zinc-500 font-medium">Automated builder assignment, hero prioritization, and spend limits.</p>
            </div>
            <button
              type="button"
              role="switch"
              aria-checked={autoBuilders}
              onClick={() => setAutoBuilders(!autoBuilders)}
              className={`w-14 h-7 rounded-full transition-all duration-500 relative shrink-0 ${autoBuilders ? 'bg-emerald-500/80' : 'bg-zinc-200 dark:bg-zinc-800'}`}
            >
              <div className={`absolute top-1 w-5 h-5 rounded-full transition-all duration-500 shadow-lg ${autoBuilders ? 'left-8 bg-white' : 'left-1 bg-white dark:bg-zinc-500'}`}></div>
            </button>
          </div>

          {/* Builder Controls */}
          <div className={`space-y-6 transition-all duration-300 ${!autoBuilders ? 'opacity-40 pointer-events-none' : ''}`}>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-6 items-start">
              <div className="space-y-2">
                <label className="text-[11px] font-black text-zinc-500 uppercase tracking-wider block">Upgrade Priority Strategy</label>
                <div className="relative">
                  <select
                    value={upgradeStrategy}
                    onChange={e => setUpgradeStrategy(e.target.value)}
                    className="w-full h-14 appearance-none bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-5 pr-10 text-sm font-bold text-zinc-900 dark:text-white focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700 focus:ring-4 focus:ring-zinc-950/5 dark:focus:ring-white/5 transition-all cursor-pointer"
                  >
                    <option value="heroes_first">Heroes First (Prioritize King, Queen, Warden, Champion)</option>
                    <option value="cheapest_first">Cheapest First (Lowest cost upgrade)</option>
                    <option value="most_expensive_first">Most Expensive First (Heavy defense upgrades)</option>
                    <option value="balanced">Balanced / In-Game Suggestions</option>
                  </select>
                  <span className="material-symbols-outlined text-zinc-400 absolute right-4 top-1/2 -translate-y-1/2 pointer-events-none text-xl">
                    expand_more
                  </span>
                </div>
                <span className="text-[11px] text-zinc-500 block">Determines which item ClashGO upgrades first when builder is free</span>
              </div>

              <div className="space-y-2">
                <label className="text-[11px] font-black text-zinc-500 uppercase tracking-wider block">Reserve Builders for Walls</label>
                <input
                  type="number"
                  min={0}
                  max={5}
                  value={reserveBuilders}
                  onChange={e => setReserveBuilders(Math.max(0, Math.min(5, parseInt(e.target.value) || 0)))}
                  className="w-full h-14 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-5 text-sm font-bold text-zinc-900 dark:text-white tabular-nums focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700 focus:ring-4 focus:ring-zinc-950/5 dark:focus:ring-white/5 transition-all"
                />
                <span className="text-[11px] text-zinc-500 block">Always keeps {reserveBuilders} builder{reserveBuilders === 1 ? '' : 's'} free for wall dumping</span>
              </div>
            </div>

            {/* Category Filters */}
            <div className="space-y-3 pt-2 border-t border-zinc-200/50 dark:border-zinc-800/50">
              <label className="text-[11px] font-black text-zinc-500 uppercase tracking-wider block">Allowed Building Categories</label>
              <div className="flex flex-wrap gap-3">
                {[
                  { label: 'Heroes', val: upgradeHeroes, set: setUpgradeHeroes },
                  { label: 'Defenses', val: upgradeDefenses, set: setUpgradeDefenses },
                  { label: 'Resources', val: upgradeResources, set: setUpgradeResources },
                  { label: 'Army Buildings', val: upgradeArmy, set: setUpgradeArmy },
                  { label: 'Traps', val: upgradeTraps, set: setUpgradeTraps },
                ].map((cat, i) => (
                  <button
                    key={i}
                    type="button"
                    onClick={() => cat.set(!cat.val)}
                    className={`py-2 px-4 rounded-xl text-xs font-bold transition-all border ${
                      cat.val
                        ? 'bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950 border-zinc-950 dark:border-white shadow'
                        : 'bg-white dark:bg-zinc-900 text-zinc-400 border-zinc-200 dark:border-zinc-800'
                    }`}
                  >
                    {cat.val ? '✓ ' : '+ '}{cat.label}
                  </button>
                ))}
              </div>
            </div>

            {/* Spend Limits */}
            <div className="space-y-3 pt-2 border-t border-zinc-200/50 dark:border-zinc-800/50">
              <label className="text-[11px] font-black text-zinc-500 uppercase tracking-wider block">Max Cost Per Upgrade (0 = Unlimited)</label>
              <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                <div>
                  <span className="text-[10px] text-zinc-400 font-bold block mb-1.5 uppercase tracking-wider">Max Gold</span>
                  <input
                    type="number"
                    value={maxGoldPerUpgrade}
                    onChange={e => setMaxGoldPerUpgrade(parseInt(e.target.value) || 0)}
                    className="w-full h-12 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 text-sm font-bold text-zinc-900 dark:text-white tabular-nums focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700"
                  />
                </div>
                <div>
                  <span className="text-[10px] text-zinc-400 font-bold block mb-1.5 uppercase tracking-wider">Max Elixir</span>
                  <input
                    type="number"
                    value={maxElixirPerUpgrade}
                    onChange={e => setMaxElixirPerUpgrade(parseInt(e.target.value) || 0)}
                    className="w-full h-12 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 text-sm font-bold text-zinc-900 dark:text-white tabular-nums focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700"
                  />
                </div>
                <div>
                  <span className="text-[10px] text-zinc-400 font-bold block mb-1.5 uppercase tracking-wider">Max Dark Elixir</span>
                  <input
                    type="number"
                    value={maxDEPerUpgrade}
                    onChange={e => setMaxDEPerUpgrade(parseInt(e.target.value) || 0)}
                    className="w-full h-12 bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-4 text-sm font-bold text-zinc-900 dark:text-white tabular-nums focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700"
                  />
                </div>
              </div>
            </div>

            {/* Dry Run / Simulation Mode */}
            <div className="pt-2 border-t border-zinc-200/50 dark:border-zinc-800/50">
              <button
                type="button"
                role="switch"
                aria-checked={dryRun}
                onClick={() => setDryRun(!dryRun)}
                className="w-full flex items-center justify-between p-4 rounded-2xl bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 text-left group hover:bg-white dark:hover:bg-zinc-900 transition-all"
              >
                <div className="max-w-[75%]">
                  <span className="block text-sm font-bold text-zinc-900 dark:text-white">Dry Run / Simulation Mode</span>
                  <span className="block text-xs text-zinc-500">Inspect & verify upgrade dialogs without spending gold, elixir, or gems</span>
                </div>
                <div className={`w-11 h-6 rounded-full transition-all duration-300 relative shrink-0 ${dryRun ? 'bg-amber-500' : 'bg-zinc-300 dark:bg-zinc-700'}`}>
                  <div className={`absolute top-1 w-4 h-4 rounded-full transition-all duration-300 shadow ${dryRun ? 'left-6 bg-white' : 'left-1 bg-white'}`}></div>
                </div>
              </button>
            </div>
          </div>
        </div>
      </div>
    )}

    {/* TAB 3: LABORATORY & HERO PETS */}
    {activeTab === 'lab_pets' && (
      <div className="space-y-8 animate-fadeIn">
        {/* Laboratory Research */}
        <div className="bg-white dark:bg-zinc-900 p-8 rounded-[3rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none space-y-8 transition-all duration-500">
          <div className="flex justify-between items-start">
            <div>
              <h3 className="text-2xl font-bold text-zinc-950 dark:text-white mb-2 tracking-tight">Laboratory Research</h3>
              <p className="text-sm text-zinc-500 dark:text-zinc-500 font-medium">Automatic troop, spell, and siege machine upgrades.</p>
            </div>
            <button
              type="button"
              role="switch"
              aria-checked={autoLab}
              onClick={() => setAutoLab(!autoLab)}
              className={`w-14 h-7 rounded-full transition-all duration-500 relative shrink-0 ${autoLab ? 'bg-emerald-500/80' : 'bg-zinc-200 dark:bg-zinc-800'}`}
            >
              <div className={`absolute top-1 w-5 h-5 rounded-full transition-all duration-500 shadow-lg ${autoLab ? 'left-8 bg-white' : 'left-1 bg-white dark:bg-zinc-500'}`}></div>
            </button>
          </div>

          <div className={`space-y-6 transition-all duration-300 ${!autoLab ? 'opacity-40 pointer-events-none' : ''}`}>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-6 items-start">
              <div className="space-y-2">
                <label className="text-[11px] font-black text-zinc-500 uppercase tracking-wider block">Research Priority</label>
                <div className="relative">
                  <select
                    value={labStrategy}
                    onChange={e => setLabStrategy(e.target.value)}
                    className="w-full h-14 appearance-none bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 rounded-2xl px-5 pr-10 text-sm font-bold text-zinc-900 dark:text-white focus:outline-none focus:border-zinc-300 dark:focus:border-zinc-700 focus:ring-4 focus:ring-zinc-950/5 dark:focus:ring-white/5 transition-all cursor-pointer"
                  >
                    <option value="cheapest_first">Cheapest First</option>
                    <option value="spells_first">Spells First (Prioritize Spell Upgrades)</option>
                    <option value="troops_first">Troops First (Prioritize Troops)</option>
                    <option value="siege_first">Siege Machines First</option>
                  </select>
                  <span className="material-symbols-outlined text-zinc-400 absolute right-4 top-1/2 -translate-y-1/2 pointer-events-none text-xl">
                    expand_more
                  </span>
                </div>
                <span className="text-[11px] text-zinc-500 block">Sets research priority type</span>
              </div>

              <div className="space-y-2">
                <label className="text-[11px] font-black text-zinc-500 uppercase tracking-wider block">Dark Elixir Permission</label>
                <button
                  type="button"
                  role="switch"
                  aria-checked={labAllowDE}
                  onClick={() => setLabAllowDE(!labAllowDE)}
                  className="w-full h-14 px-5 rounded-2xl bg-zinc-50/50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 text-left flex justify-between items-center group hover:bg-white dark:hover:bg-zinc-900 transition-all"
                >
                  <div>
                    <span className="block text-xs font-bold text-zinc-900 dark:text-white">Allow Dark Elixir Research</span>
                    <span className="block text-[11px] text-zinc-500">Spend DE on research</span>
                  </div>
                  <div className={`w-10 h-5 rounded-full transition-all duration-300 relative shrink-0 ${labAllowDE ? 'bg-emerald-500' : 'bg-zinc-300 dark:bg-zinc-700'}`}>
                    <div className={`absolute top-0.5 w-4 h-4 rounded-full transition-all duration-300 shadow ${labAllowDE ? 'left-5 bg-white' : 'left-0.5 bg-white'}`}></div>
                  </div>
                </button>
                <span className="text-[11px] text-zinc-500 block">Toggles DE spending in lab</span>
              </div>
            </div>
          </div>
        </div>

        {/* Interactive Hero Pets Queue */}
        <div className="bg-white dark:bg-zinc-900 p-8 rounded-[3rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none space-y-8 transition-all duration-500">
          <div className="flex justify-between items-start">
            <div>
              <h3 className="text-2xl font-bold text-zinc-950 dark:text-white mb-2 tracking-tight">Hero Pets Queue & Priority</h3>
              <p className="text-sm text-zinc-500 dark:text-zinc-500 font-medium">Reorder and customize which hero pets get upgraded in Pet House.</p>
            </div>
            <button
              type="button"
              role="switch"
              aria-checked={autoPets}
              onClick={() => setAutoPets(!autoPets)}
              className={`w-14 h-7 rounded-full transition-all duration-500 relative shrink-0 ${autoPets ? 'bg-emerald-500/80' : 'bg-zinc-200 dark:bg-zinc-800'}`}
            >
              <div className={`absolute top-1 w-5 h-5 rounded-full transition-all duration-500 shadow-lg ${autoPets ? 'left-8 bg-white' : 'left-1 bg-white dark:bg-zinc-500'}`}></div>
            </button>
          </div>

          <div className={`space-y-6 transition-all duration-300 ${!autoPets ? 'opacity-40 pointer-events-none' : ''}`}>
            <div className="space-y-3">
              <label className="text-[11px] font-black text-zinc-500 uppercase tracking-wider block">
                Active Upgrade Priority Order (Top to Bottom)
              </label>
              <div className="space-y-2">
                {petPriorities.map((pet, idx) => (
                  <div
                    key={pet}
                    className="flex items-center justify-between p-3.5 rounded-2xl bg-zinc-50/60 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 transition-all hover:border-zinc-300 dark:hover:border-zinc-700"
                  >
                    <div className="flex items-center gap-3">
                      <span className="w-7 h-7 rounded-xl bg-indigo-500/10 text-indigo-600 dark:text-indigo-400 font-black text-xs flex items-center justify-center border border-indigo-500/20">
                        #{idx + 1}
                      </span>
                      <span className="text-sm font-bold text-zinc-900 dark:text-white">{pet}</span>
                    </div>
                    <div className="flex items-center gap-1.5">
                      <button
                        type="button"
                        disabled={idx === 0}
                        onClick={() => movePet(idx, 'up')}
                        className="w-8 h-8 rounded-xl bg-white dark:bg-zinc-900 border border-zinc-200 dark:border-zinc-800 flex items-center justify-center text-zinc-600 dark:text-zinc-300 disabled:opacity-30 disabled:cursor-not-allowed hover:bg-zinc-100 dark:hover:bg-zinc-800 transition-all"
                        title="Move Up"
                      >
                        <span className="material-symbols-outlined text-base">arrow_upward</span>
                      </button>
                      <button
                        type="button"
                        disabled={idx === petPriorities.length - 1}
                        onClick={() => movePet(idx, 'down')}
                        className="w-8 h-8 rounded-xl bg-white dark:bg-zinc-900 border border-zinc-200 dark:border-zinc-800 flex items-center justify-center text-zinc-600 dark:text-zinc-300 disabled:opacity-30 disabled:cursor-not-allowed hover:bg-zinc-100 dark:hover:bg-zinc-800 transition-all"
                        title="Move Down"
                      >
                        <span className="material-symbols-outlined text-base">arrow_downward</span>
                      </button>
                      <button
                        type="button"
                        onClick={() => removePet(pet)}
                        className="w-8 h-8 rounded-xl bg-white dark:bg-zinc-900 border border-zinc-200 dark:border-zinc-800 flex items-center justify-center text-rose-500 hover:bg-rose-50 dark:hover:bg-rose-950/30 ml-2 transition-all"
                        title="Remove from queue"
                      >
                        <span className="material-symbols-outlined text-base">close</span>
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </div>

            {/* Available pets to add */}
            <div className="space-y-2 pt-2 border-t border-zinc-100 dark:border-zinc-800/60">
              <label className="text-[11px] font-black text-zinc-500 uppercase tracking-wider block">Add Pet to Queue</label>
              <div className="flex flex-wrap gap-2">
                {allKnownPets
                  .filter(p => !petPriorities.includes(p))
                  .map(pet => (
                    <button
                      key={pet}
                      type="button"
                      onClick={() => addPet(pet)}
                      className="px-3 py-1.5 rounded-xl text-xs font-bold bg-white dark:bg-zinc-900 border border-zinc-200 dark:border-zinc-800 text-zinc-600 dark:text-zinc-300 hover:border-indigo-500 hover:text-indigo-600 dark:hover:text-indigo-400 transition-all flex items-center gap-1.5"
                    >
                      <span className="material-symbols-outlined text-sm">add</span>
                      {pet}
                    </button>
                  ))}
                {allKnownPets.every(p => petPriorities.includes(p)) && (
                  <span className="text-xs text-zinc-400 italic">All pets currently in priority queue.</span>
                )}
              </div>
            </div>
          </div>
        </div>
      </div>
    )}


        <div className="flex flex-col items-end gap-3 pt-4">
          {anyInvalid && (
            <div className="px-4 py-2 rounded-xl bg-amber-500/10 border border-amber-500/30 text-[11px] font-bold text-amber-600 dark:text-amber-400 tracking-wider" role="alert">
              <span className="material-symbols-outlined text-sm align-middle mr-1">warning</span>
              Some values are out of range — fix them before saving.
            </div>
          )}
          {lastSaveError && saveStatus === 'error' && (
            <div
              role="status"
              aria-live="polite"
              className="px-4 py-2 rounded-xl bg-rose-500/10 border border-rose-500/30 text-[11px] font-bold text-rose-600 dark:text-rose-400 tracking-wider max-w-md text-right"
              title={lastSaveError}
            >
              <span className="material-symbols-outlined text-sm align-middle mr-1">error</span>
              Save failed: {lastSaveError.length > 80 ? lastSaveError.slice(0, 77) + '…' : lastSaveError}
            </div>
          )}
          <button
            type="submit"
            disabled={saveStatus === 'saving' || anyInvalid}
            aria-label={`${saveButtonLabel} — saves your config to the bot`}
            data-testid="config-save-btn"
            data-save-state={saveStatus}
            className={`group h-16 px-12 font-black text-xs uppercase tracking-[0.3em] rounded-3xl transition-all duration-300 active:scale-[0.98] flex items-center gap-4 border border-transparent dark:border-white/10 shadow-lg disabled:opacity-40 disabled:cursor-not-allowed ${saveButtonClasses}`}
          >
            {saveButtonLabel}
            <span className={`material-symbols-outlined text-lg transition-transform ${saveStatus === 'saving' ? 'animate-spin' : saveStatus === 'idle' ? 'group-hover:translate-x-1' : ''}`}>
              {saveButtonIcon}
            </span>
          </button>
        </div>
      </form>
    </div>
  );
});

export default ConfigView;
