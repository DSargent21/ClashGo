import { useState, useEffect, useMemo } from 'react';
import Sidebar from './components/Sidebar';
import Dashboard from './components/Dashboard';
import Analytics from './components/Analytics';
import ConfigView from './components/ConfigView';
import SettingsView from './components/SettingsView';
import { EventsOn } from '../wailsjs/runtime';
import {
  GetStats,
  GetAttackHistory,
  GetLogs,
  SaveConfig,
  SetStorageConfig,
  SetScheduleConfig,
  SetAutoUpgradeConfig,
  SetStrategyArmySlot,
  SetStrategyAutoEnd,
  StartBot,
  StopBot,
  IsRunning,
  ResetStats,
  GetConfig,
  GetStrategies,
  GetUpdateStatus,
  GetAppVersion,
  CheckForUpdate,
  DownloadUpdate,
  ApplyUpdate,
  InstallAndRestart,
  SkipCurrentVersion,
  ClearSkippedVersion,
} from '../wailsjs/go/main/App';
import { bot } from '../wailsjs/go/models';
import { TabType, UpdateStatus, DEFAULT_UPDATE_STATUS } from './types';
import UpdateBanner from './components/UpdateBanner';
import './App.css';

/**
 * Defensive wrapper around the Wails-generated `EventsOn` that returns
 * a no-op unsubscribe when the Wails runtime bridge isn't available.
 *
 * Why this exists
 * ─────────────────────────────────────────────────────────────────
 * The generated `web/wailsjs/runtime/runtime.js` short-circuits via
 * `window.runtime?.EventsOnMultiple(eventName, callback)` — but when
 * `window.runtime` itself is undefined (e.g. running the React app
 * directly in Chrome via `npm run dev`, or a Wails WkWebView that
 * hasn't received its bridge yet), the inner `EventsOnMultiple`
 * function reads `.EventsOnMultiple` off `undefined` and throws an
 * `Uncaught TypeError`. That throw propagates out of the synchronous
 * useEffect body, React unmounts the whole `<App/>` (no error
 * boundary in the original tree), the `#root` container becomes
 * empty, `WebviewIsTransparent: true` lets Wails' dark-zinc
 * BackgroundColour (#09090b) show through, and the user reports a
 * "black screen" with no actionable signal.
 *
 * Wrapping EventsOn at the call site keeps the defensive measure
 * scoped to where it's needed and avoids introducing a class
 * component (the codebase uses React.FC + React.memo throughout).
 */
function safeEventsOn(
  eventName: string,
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  callback: (...args: any[]) => void,
): () => void {
  try {
    return EventsOn(eventName, callback);
  } catch {
    // Wails runtime bridge isn't ready yet (development outside wails
    // dev, or the bridge hasn't injected). Return a no-op unsubscribe
    // so the cleanup chain in the useEffect still runs cleanly.
    return () => {};
  }
}

const getInitialDarkMode = (): boolean => {
  try {
    const stored = localStorage.getItem('darkMode');
    if (stored !== null) return stored === 'true';
    // Default to LIGHT, not OS preference.
    //
    // macOS's system appearance is dark by default, so honoring
    // `prefers-color-scheme: dark` here stacks dark on top of dark:
    // Tailwind `bg-zinc-950` (#09090b) over Wails' macOS dark window
    // chrome over the WkWebView's transparent layer. The visual result
    // reads as "black screen" to the user even though every component
    // is technically painted. The SettingsView toggle still round-trips
    // the user's explicit choice via `localStorage`.
    return false;
  } catch {
    return false;
  }
};

const getInitialSidebarExpanded = (): boolean => {
  try {
    const stored = localStorage.getItem('sidebarExpanded');
    if (stored !== null) return stored === 'true';
    return true;
  } catch {
    return true;
  }
};

function App() {
  const [tab, setTab] = useState<TabType>('dashboard');
  const [stats, setStats] = useState<bot.BotStats>(new bot.BotStats({
    attacks_completed: 0,
    search_skips: 0,
    total_gold: 0,
    total_elixir: 0,
    total_de: 0,
    stars_0: 0,
    stars_1: 0,
    stars_2: 0,
    stars_3: 0,
    uptime: 0,
    adb_health: {
      avg_capture_ms: 0,
      consecutive_fails: 0,
      captures_total: 0,
      errors_total: 0,
      last_error: ""
    }
  }));
  const [isRunning, setIsRunning] = useState(false);
  const [history, setHistory] = useState<bot.AttackReport[]>([]);
  const [logs, setLogs] = useState<string[]>([]);
  const [adbPort, setAdbPort] = useState(5555);
  const [darkMode, setDarkMode] = useState(getInitialDarkMode);
  const [sidebarExpanded, setSidebarExpanded] = useState(getInitialSidebarExpanded);

  // Updater state — pushed via `updater_status` event from Go.
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus>(DEFAULT_UPDATE_STATUS);
  const [appVersion, setAppVersion] = useState('');
  const [isUpdateModalOpen, setIsUpdateModalOpen] = useState(false);

  // Config states
  const [goldThreshold, setGoldThreshold] = useState(400000);
  const [elixirThreshold, setElixirThreshold] = useState(400000);
  const [deThreshold, setDeThreshold] = useState(2000);
  const [selectedStrategy, setSelectedStrategy] = useState('default');
  const [strategies, setStrategiesList] = useState<string[]>([]);
  const [searchEnabled, setSearchEnabled] = useState(true);
  const [upgradeWalls, setUpgradeWalls] = useState(false);
  const [stallTimer, setStallTimer] = useState(30);
  const [strategySlots, setStrategySlots] = useState<Record<string, number>>({});
  const [strategyAutoEnd, setStrategyAutoEnd] = useState<Record<string, boolean>>({});

  // Storage states
  const [haltGold, setHaltGold] = useState(true);
  const [haltElixir, setHaltElixir] = useState(true);
  const [haltDE, setHaltDE] = useState(false);
  const [storageThreshold, setStorageThreshold] = useState(95);
  const [dumpToWallsBeforeHalt, setDumpToWallsBeforeHalt] = useState(true);

  // Schedule states
  const [scheduleEnabled, setScheduleEnabled] = useState(false);
  const [activeHoursStart, setActiveHoursStart] = useState("08:00");
  const [activeHoursEnd, setActiveHoursEnd] = useState("23:00");
  const [sessionDuration, setSessionDuration] = useState(60);
  const [breakDuration, setBreakDuration] = useState(20);
  const [forceStopOnBreak, setForceStopOnBreak] = useState(true);
  const [maxPerHour, setMaxPerHour] = useState(6);
  const [maxPerDay, setMaxPerDay] = useState(40);

  // Auto-upgrade states & filters
  const [autoBuilders, setAutoBuilders] = useState(false);
  const [reserveBuilders, setReserveBuilders] = useState(1);
  const [upgradeStrategy, setUpgradeStrategy] = useState("heroes_first");
  const [upgradeDefenses, setUpgradeDefenses] = useState(true);
  const [upgradeResources, setUpgradeResources] = useState(true);
  const [upgradeArmy, setUpgradeArmy] = useState(true);
  const [upgradeTraps, setUpgradeTraps] = useState(true);
  const [upgradeHeroes, setUpgradeHeroes] = useState(true);
  const [maxGoldPerUpgrade, setMaxGoldPerUpgrade] = useState(0);
  const [maxElixirPerUpgrade, setMaxElixirPerUpgrade] = useState(0);
  const [maxDEPerUpgrade, setMaxDEPerUpgrade] = useState(0);
  const [autoLab, setAutoLab] = useState(false);
  const [labStrategy, setLabStrategy] = useState("cheapest_first");
  const [labAllowDE, setLabAllowDE] = useState(true);
  const [autoPets, setAutoPets] = useState(false);
  const [petPriorities, setPetPriorities] = useState<string[]>(["Diggy", "Unicorn", "Frosty", "Phoenix", "Spirit Fox"]);
  const [wallResource, setWallResource] = useState("both");
  const [wallMinGoldReserve, setWallMinGoldReserve] = useState(2000000);
  const [wallMinElixirReserve, setWallMinElixirReserve] = useState(2000000);
  const [autoCollect, setAutoCollect] = useState(true);
  const [dryRun, setDryRun] = useState(false);

  useEffect(() => {
    const init = async () => {
      try {
        const [conf, running, strats] = await Promise.all([
          GetConfig(),
          IsRunning(),
          GetStrategies()
        ]);

        setGoldThreshold(conf.search.min_loot_gold);
        setElixirThreshold(conf.search.min_loot_elixir);
        setDeThreshold(conf.search.min_loot_de);
        setSearchEnabled(conf.search.enabled);
        setUpgradeWalls(conf.upgrade.upgrade_walls);
        setSelectedStrategy(conf.attack.strategy_file);
        setStallTimer(conf.attack.stall_timer_seconds);
        if (conf.attack.strategy_slots) {
          setStrategySlots(conf.attack.strategy_slots);
        }
        if (conf.attack.strategy_auto_end) {
          setStrategyAutoEnd(conf.attack.strategy_auto_end);
        }
        if (conf.storage) {
          setHaltGold(conf.storage.halt_when_gold_full ?? true);
          setHaltElixir(conf.storage.halt_when_elixir_full ?? true);
          setHaltDE(conf.storage.halt_when_de_full ?? false);
          setStorageThreshold(conf.storage.full_threshold_percent ?? 95);
          setDumpToWallsBeforeHalt(conf.storage.dump_to_walls_before_halt ?? true);
          setAutoCollect(conf.storage.auto_collect ?? true);
        }
        if (conf.schedule) {
          setScheduleEnabled(conf.schedule.enabled ?? false);
          setActiveHoursStart(conf.schedule.active_hours_start || "08:00");
          setActiveHoursEnd(conf.schedule.active_hours_end || "23:00");
          setSessionDuration(conf.schedule.session_duration_minutes ?? 60);
          setBreakDuration(conf.schedule.break_duration_minutes ?? 20);
          setForceStopOnBreak(conf.schedule.force_stop_on_break ?? true);
          setMaxPerHour(conf.schedule.max_attacks_per_hour ?? 6);
          setMaxPerDay(conf.schedule.max_attacks_per_day ?? 40);
        }
        if (conf.upgrade) {
          setAutoBuilders(conf.upgrade.auto_builders ?? false);
          setReserveBuilders(conf.upgrade.reserve_builders ?? 1);
          setUpgradeStrategy(conf.upgrade.upgrade_strategy || "heroes_first");
          setUpgradeDefenses(conf.upgrade.upgrade_defenses ?? true);
          setUpgradeResources(conf.upgrade.upgrade_resources ?? true);
          setUpgradeArmy(conf.upgrade.upgrade_army ?? true);
          setUpgradeTraps(conf.upgrade.upgrade_traps ?? true);
          setUpgradeHeroes(conf.upgrade.upgrade_heroes ?? true);
          setMaxGoldPerUpgrade(conf.upgrade.max_gold_per_upgrade ?? 0);
          setMaxElixirPerUpgrade(conf.upgrade.max_elixir_per_upgrade ?? 0);
          setMaxDEPerUpgrade(conf.upgrade.max_de_per_upgrade ?? 0);
          setAutoLab(conf.upgrade.auto_lab ?? false);
          setLabStrategy(conf.upgrade.lab_strategy || "cheapest_first");
          setLabAllowDE(conf.upgrade.lab_allow_de ?? true);
          setAutoPets(conf.upgrade.auto_pets ?? false);
          if (conf.upgrade.pet_priorities && conf.upgrade.pet_priorities.length > 0) {
            setPetPriorities(conf.upgrade.pet_priorities);
          }
          setWallResource(conf.upgrade.wall_resource || "both");
          setWallMinGoldReserve(conf.upgrade.wall_min_gold_reserve ?? 2000000);
          setWallMinElixirReserve(conf.upgrade.wall_min_elixir_reserve ?? 2000000);
          setDryRun(conf.upgrade.dry_run ?? false);
        }
        setIsRunning(running);
        // Never let a null from the Go side reach the Config page — a
        // nil slice marshals to JSON null, and ConfigView dereferences
        // `strategies.length`. `?? []` keeps the UI resilient even if a
        // future binding regresses to null.
        setStrategiesList(strats ?? []);
        setAdbPort(conf.device.adb_port);
      } catch (err) {
        console.error('Init failed:', err);
      }

      // Pull the embedded app version + initial updater snapshot.
      try {
        const v = await GetAppVersion();
        setAppVersion(v);
      } catch (err) {
        console.warn('GetAppVersion failed:', err);
      }
      try {
        const s = await GetUpdateStatus();
        setUpdateStatus(s);
      } catch (err) {
        console.warn('GetUpdateStatus failed:', err);
      }
    };
    init();

    const fetchData = async () => {
      try {
        const [s, h, l] = await Promise.all([
          GetStats(),
          GetAttackHistory(),
          GetLogs()
        ]);
        setStats(s);
        setHistory(h);
        setLogs(l);
      } catch (err) {
        console.error('Data fetch failed:', err);
      }
    };

    fetchData();
    const interval = setInterval(fetchData, 2000);

    // The 1 Hz screenshot poll used to live here. It moved into
    // <Feed/>'s own useEffect so it only runs when the Live View tab
    // is mounted, instead of burning the WailsIPC bridge every second
    // regardless of tab. The Live View tab has been removed entirely
    // (see the 'feed' tab deletion in Sidebar.tsx / types.ts).

    // Subscribe to updater_status events pushed every 2s from Go.
    // `safeEventsOn` wraps the runtime bridge so a missing
    // `window.runtime` (e.g. `npm run dev` opened in Chrome directly,
    // or a WkWebView whose bridge hasn't injected yet) doesn't throw
    // out of this synchronous useEffect body — that throw propagates
    // and unmounts the React tree, which under `WebviewIsTransparent:
    // true` presents as the dark-zinc window frame.
    const unsubUpdater = safeEventsOn("updater_status", (data: UpdateStatus) => {
      if (data && typeof data === 'object') {
        setUpdateStatus(data);
      }
    });

    // StartBot returns running=true immediately while the boot runs in
    // the background (BlueStacks launch + ADB connect can take minutes
    // on a cold start). When the boot fails, Go emits bot_error /
    // bot_init_failed — without listening, the sidebar stays on
    // "STOP BOT" forever and Stop becomes a confusing no-op (there's
    // no bot to stop). Flip the button back to START on either event.
    const unsubBotError = safeEventsOn("bot_error", () => {
      setIsRunning(false);
    });
    const unsubBotInitFailed = safeEventsOn("bot_init_failed", () => {
      setIsRunning(false);
    });

    return () => {
      clearInterval(interval);
      unsubUpdater();
      unsubBotError();
      unsubBotInitFailed();
    };
  }, []);

  useEffect(() => {
    if (darkMode) {
      document.documentElement.classList.add('dark');
    } else {
      document.documentElement.classList.remove('dark');
    }
    try {
      localStorage.setItem('darkMode', String(darkMode));
    } catch (e) {
      console.warn('Failed to save darkMode preference:', e);
    }
  }, [darkMode]);

  useEffect(() => {
    try {
      localStorage.setItem('sidebarExpanded', String(sidebarExpanded));
    } catch (e) {
      console.warn('Failed to save sidebarExpanded preference:', e);
    }
  }, [sidebarExpanded]);

  const saveSettings = async () => {
    await SaveConfig(
      goldThreshold,
      elixirThreshold,
      deThreshold,
      upgradeWalls,
      selectedStrategy,
      searchEnabled,
      stallTimer,
    );
    await SetStorageConfig(
      haltGold,
      haltElixir,
      haltDE,
      "ANY",
      storageThreshold,
      80.0,
      dumpToWallsBeforeHalt,
      autoCollect,
    );
    await SetScheduleConfig(
      scheduleEnabled,
      activeHoursStart,
      activeHoursEnd,
      sessionDuration,
      15,
      breakDuration,
      5,
      forceStopOnBreak,
      maxPerHour,
      maxPerDay,
    );
    await SetAutoUpgradeConfig(
      autoBuilders,
      reserveBuilders,
      upgradeStrategy,
      upgradeDefenses,
      upgradeResources,
      upgradeArmy,
      upgradeTraps,
      upgradeHeroes,
      maxGoldPerUpgrade,
      maxElixirPerUpgrade,
      maxDEPerUpgrade,
      autoLab,
      labStrategy,
      labAllowDE,
      autoPets,
      petPriorities,
      wallResource,
      wallMinGoldReserve,
      wallMinElixirReserve,
      dryRun,
    );
  };

  const handleStart = async () => {
    try {
      const res = await StartBot(goldThreshold, elixirThreshold, deThreshold, upgradeWalls, searchEnabled);
      setIsRunning(res.running);
    } catch (err) {
      console.error('Start failed:', err);
    }
  };

  const handleStop = async () => {
    try {
      const res = await StopBot();
      setIsRunning(res.running);
    } catch (err) {
      console.error('Stop failed:', err);
    }
  };

  const handleReset = async () => {
    try {
      await ResetStats();
      const s = await GetStats();
      setStats(s);
    } catch (err) {
      console.error('Reset failed:', err);
    }
  };

  // --- Updater handlers (bound to Wails methods on App) ---
  const handleUpdaterCheck = async () => {
    try {
      const s = await CheckForUpdate();
      setUpdateStatus(s);
      if (s.available || s.state === 'ready') {
        setIsUpdateModalOpen(true);
      }
    } catch (err) {
      console.error('CheckForUpdate failed:', err);
    }
  };
  const handleUpdaterDownload = async (): Promise<string> => {
    // Returns the local path; UpdateStatus will reflect via the
    // 2s event ticker (state will flip to 'ready').
    const path = await DownloadUpdate();
    const s = await GetUpdateStatus();
    setUpdateStatus(s);
    return path;
  };
  const handleUpdaterApply = async () => {
    await ApplyUpdate();
  };
  const handleUpdaterOneClick = async () => {
    if (updateStatus && updateStatus.state !== 'ready') {
      await DownloadUpdate();
    }
    await InstallAndRestart();
  };
  const handleUpdaterSkip = async () => {
    await SkipCurrentVersion();
    const s = await GetUpdateStatus();
    setUpdateStatus(s);
  };
  const handleUpdaterClearSkip = async () => {
    await ClearSkippedVersion();
    const s = await GetUpdateStatus();
    setUpdateStatus(s);
  };

  const dashboardProps = useMemo(() => ({
    stats,
    history,
    logs,
  }), [stats, history, logs]);

  // ADB connection state — drives the header status pill. Labels stop
  // calling the local ADB server "localhost:{port}" because the bot
  // can legitimately attach to remote devices via `adb connect
  // <ip>:{port}`; the port number is just the local ADB server's
  // listen port, not the device host regardless.
  const adbState: 'connected' | 'degraded' | 'disconnected' | 'awaiting' =
    stats.attacks_completed + stats.search_skips > 0
      ? stats.adb_health.consecutive_fails === 0
        ? 'connected'
        : stats.adb_health.consecutive_fails < 5
          ? 'degraded'
          : 'disconnected'
      : 'awaiting';
  const adbStateLabel =
    adbState === 'connected'
      ? 'Connected'
      : adbState === 'degraded'
        ? 'Degraded'
        : adbState === 'disconnected'
          ? 'Disconnected'
          : 'Awaiting';
  const configProps = useMemo(() => {
    // Default slot based on strategy if not explicitly set in config
    const stratBase = selectedStrategy.split('/').pop() || '';
    const currentArmySlot = strategySlots[stratBase] ?? (stratBase.includes('valk') ? 4 : 1);
    const isValk = stratBase.toLowerCase().includes('valk');
    const currentAutoEnd = strategyAutoEnd[stratBase] ?? isValk;

    const handleSetArmySlot = async (slot: number) => {
      setStrategySlots(prev => ({ ...prev, [stratBase]: slot }));
      try {
        await SetStrategyArmySlot(selectedStrategy, slot);
      } catch (err) {
        console.error('Failed to set strategy army slot:', err);
      }
    };

    const handleSetAutoEnd = async (enabled: boolean) => {
      setStrategyAutoEnd(prev => ({ ...prev, [stratBase]: enabled }));
      try {
        await SetStrategyAutoEnd(selectedStrategy, enabled);
      } catch (err) {
        console.error('Failed to set strategy auto end:', err);
      }
    };

    return {
      goldThreshold, setGoldThreshold,
      elixirThreshold, setElixirThreshold,
      deThreshold, setDeThreshold,
      selectedStrategy, setSelectedStrategy,
      strategies,
      searchEnabled, setSearchEnabled,
      upgradeWalls, setUpgradeWalls,
      stallTimer, setStallTimer,
      armySlot: currentArmySlot,
      onSetArmySlot: handleSetArmySlot,
      autoEnd: currentAutoEnd,
      onSetAutoEnd: handleSetAutoEnd,
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
      onSave: async () => {
        await saveSettings();
      }
    };
  }, [
    goldThreshold, elixirThreshold, deThreshold,
    selectedStrategy, strategies, searchEnabled, upgradeWalls, stallTimer, strategySlots, strategyAutoEnd,
    haltGold, haltElixir, haltDE, storageThreshold, dumpToWallsBeforeHalt, autoCollect,
    scheduleEnabled, activeHoursStart, activeHoursEnd, sessionDuration, breakDuration, forceStopOnBreak, maxPerHour, maxPerDay,
    autoBuilders, reserveBuilders, upgradeStrategy, upgradeDefenses, upgradeResources, upgradeArmy, upgradeTraps, upgradeHeroes,
    maxGoldPerUpgrade, maxElixirPerUpgrade, maxDEPerUpgrade,
    autoLab, labStrategy, labAllowDE, autoPets, petPriorities,
    wallResource, wallMinGoldReserve, wallMinElixirReserve,
    dryRun,
  ]);

  return (
    <div className="app-shell bg-zinc-50 dark:bg-zinc-950 text-zinc-950 dark:text-zinc-50 transition-colors duration-500" style={{ display: 'flex', width: '100vw', height: '100vh' }}>
      <Sidebar 
        tab={tab} 
        setTab={setTab}
        expanded={sidebarExpanded}
        setExpanded={setSidebarExpanded}
        running={isRunning}
        onStart={handleStart}
        onStop={handleStop}
      />

      <main 
        className={`flex-1 bg-zinc-50 dark:bg-zinc-950 transition-[margin-left,background-color] duration-300 ease-[cubic-bezier(0.4,0,0.2,1)] min-h-screen overflow-y-auto ${sidebarExpanded ? 'ml-64' : 'ml-20'}`}
      >
        <div className="draggable sticky top-0 left-0 right-0 h-12 z-40 bg-transparent pointer-events-auto" />
        <div className="max-w-[1600px] mx-auto p-4 md:p-6 lg:p-8 xl:p-12 pt-0 -mt-12">
          <header className="mb-8 flex justify-between items-end draggable">
            <div className="space-y-1">
              <div className="flex items-center gap-3">
                <div className="flex gap-1">
                  <span className={`w-1.5 h-1.5 rounded-full ${isRunning ? 'bg-emerald-500 animate-pulse' : 'bg-zinc-300 dark:bg-zinc-800'}`}></span>
                  <span className="w-1.5 h-1.5 bg-zinc-300 dark:bg-zinc-800 rounded-full"></span>
                </div>
                <h2 className="text-[11px] text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.4em] font-black">ClashGO System</h2>
              </div>
              <h1 className="font-headline text-5xl font-bold tracking-tight capitalize text-zinc-950 dark:text-white">{tab}</h1>
            </div>
            <div className="flex gap-4 items-center">
              <UpdateBanner
                status={updateStatus}
                appVersion={appVersion}
                isBotRunning={isRunning}
                isOpen={isUpdateModalOpen}
                onOpenChange={setIsUpdateModalOpen}
                onCheckNow={handleUpdaterCheck}
                onDownload={handleUpdaterDownload}
                onApply={handleUpdaterApply}
                onUpdateAndRestart={handleUpdaterOneClick}
                onSkip={handleUpdaterSkip}
                onClearSkip={handleUpdaterClearSkip}
                onDismiss={() => setIsUpdateModalOpen(false)}
              />
              <div className="bg-white dark:bg-zinc-900 px-6 py-3.5 rounded-2xl border border-zinc-100/50 dark:border-zinc-800/50 flex items-center gap-4 shadow-premium dark:shadow-none no-drag backdrop-blur-md" title={`ADB server port ${adbPort} — ${adbStateLabel}`}>
                <div className="relative">
                  <div className={`w-2.5 h-2.5 rounded-full ${
                    adbState === 'connected' ? 'bg-emerald-500'
                    : adbState === 'disconnected' ? 'bg-rose-500 animate-pulse'
                    : 'bg-amber-500 animate-pulse'
                  }`}></div>
                  {adbState === 'connected' && <div className="absolute inset-0 w-2.5 h-2.5 rounded-full bg-emerald-500 animate-ping opacity-20"></div>}
                </div>
                <span className={`text-[11px] font-black uppercase tracking-widest ${
                  adbState === 'connected' ? 'text-zinc-500 dark:text-zinc-400'
                  : adbState === 'disconnected' ? 'text-rose-500'
                  : 'text-amber-600 dark:text-amber-400'
                }`}>ADB: {adbPort} · {adbStateLabel}</span>
              </div>
            </div>
          </header>

          {tab === 'dashboard' && <Dashboard {...dashboardProps} />}
          {tab === 'analytics' && <Analytics stats={stats} />}
          {tab === 'config' && <ConfigView {...configProps} />}
          {tab === 'settings' && (
            <SettingsView
              stats={stats}
              adbPort={adbPort}
              darkMode={darkMode}
              setDarkMode={setDarkMode}
              onResetStats={handleReset}
              appVersion={appVersion}
              updateStatus={updateStatus}
              onCheckUpdates={handleUpdaterCheck}
              onClearSkip={handleUpdaterClearSkip}
              onOpenUpdateModal={() => setIsUpdateModalOpen(true)}
            />
          )}
        </div>
      </main>
    </div>
  );
}

export default App;
