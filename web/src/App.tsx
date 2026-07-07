import React, { useState, useEffect, useMemo, useRef, useCallback } from 'react';
import Sidebar from './components/Sidebar';
import Dashboard from './components/Dashboard';
import Feed from './components/Feed';
import Analytics from './components/Analytics';
import ConfigView from './components/ConfigView';
import SettingsView from './components/SettingsView';
import { EventsOn } from '../wailsjs/runtime';
import {
  GetStats,
  GetAttackHistory,
  GetLogs,
  GetLiveScreenshot,
  SaveConfig,
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
import { bot, main } from '../wailsjs/go/models';
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
  const [screenshot, setScreenshot] = useState('');
  const [adbPort, setAdbPort] = useState(5555);
  const [darkMode, setDarkMode] = useState(getInitialDarkMode);
  const [sidebarExpanded, setSidebarExpanded] = useState(getInitialSidebarExpanded);

  // Updater state — pushed via `updater_status` event from Go.
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus>(DEFAULT_UPDATE_STATUS);
  const [appVersion, setAppVersion] = useState('');
  const [updateDismissed, setUpdateDismissed] = useState(false);

  // Config states
  const [goldThreshold, setGoldThreshold] = useState(400000);
  const [elixirThreshold, setElixirThreshold] = useState(400000);
  const [deThreshold, setDeThreshold] = useState(2000);
  const [selectedStrategy, setSelectedStrategy] = useState('default');
  const [strategies, setStrategiesList] = useState<string[]>([]);
  const [searchEnabled, setSearchEnabled] = useState(true);
  const [upgradeWalls, setUpgradeWalls] = useState(false);
  const [stallTimer, setStallTimer] = useState(30);

  const terminalEndRef = useRef<HTMLDivElement>(null);

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
        setIsRunning(running);
        setStrategiesList(strats);
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

  const updateScreenshot = async () => {
      try {
        const img = await GetLiveScreenshot();
        if (img) setScreenshot('data:image/jpeg;base64,' + img);
      } catch (err) {
        // Silently fail screenshots
      }
    };

    const screenInterval = setInterval(updateScreenshot, 1000);

    // Subscribe to Wails-pushed events. `safeEventsOn` wraps the runtime
    // bridge so a missing `window.runtime` (e.g. `npm run dev` opened in
    // Chrome directly, or a WkWebView whose bridge hasn't injected yet)
    // doesn't throw out of this synchronous useEffect body — that throw
    // propagates and unmounts the React tree, which under
    // `WebviewIsTransparent: true` presents as the dark-zinc window frame.
    const unsub = safeEventsOn("state-change", (data: any) => {
      console.log("State changed:", data);
    });

    // Subscribe to updater_status events pushed every 2s from Go.
    const unsubUpdater = safeEventsOn("updater_status", (data: UpdateStatus) => {
      if (data && typeof data === 'object') {
        setUpdateStatus(data);
      }
    });

    return () => {
      clearInterval(interval);
      clearInterval(screenInterval);
      unsub();
      unsubUpdater();
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
    try {
      await SaveConfig(
        goldThreshold,
        elixirThreshold,
        deThreshold,
        upgradeWalls,
        selectedStrategy,
        searchEnabled,
        stallTimer
      );
    } catch (err) {
      console.error('Save failed:', err);
    }
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
      setUpdateDismissed(false);
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
    // The Go-side InstallAndRestart takes care of stopping the bot,
    // saving stats, marking the restarting state, spawning the helper,
    // and exiting the process after a 1s IPC flush window.
    await InstallAndRestart();
    // The status will flip to 'restarting' and the React side will
    // switch to the non-dismissible splash automatically via the
    // updater_status event listener.
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
    onClearLogs: () => setLogs([]), // Local clear for UI if needed, but GetLogs will refill
    terminalEndRef
  }), [stats, history, logs]);

  const configProps = useMemo(() => ({
    goldThreshold, setGoldThreshold,
    elixirThreshold, setElixirThreshold,
    deThreshold, setDeThreshold,
    selectedStrategy, setSelectedStrategy,
    strategies,
    searchEnabled, setSearchEnabled,
    upgradeWalls, setUpgradeWalls,
    stallTimer, setStallTimer,
    onSave: (e: React.FormEvent) => {
      e.preventDefault();
      saveSettings();
    }
  }), [
    goldThreshold, elixirThreshold, deThreshold,
    selectedStrategy, strategies, searchEnabled, upgradeWalls, stallTimer
  ]);

  return (
    <div className="app-shell bg-zinc-50 dark:bg-zinc-950 text-zinc-950 dark:text-zinc-50 transition-colors duration-500" style={{ display: 'flex', width: '100vw', height: '100vh' }}>
      <Sidebar 
        tab={tab} 
        setTab={setTab}
        expanded={sidebarExpanded}
        setExpanded={setSidebarExpanded}
        statusMsg={isRunning ? "Running" : "Idle"}
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
                <h2 className="text-[11px] text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.4em] font-black">ClashGO System</h2>
              </div>
              <h1 className="font-headline text-5xl font-bold tracking-tight capitalize text-zinc-950 dark:text-white">{tab}</h1>
            </div>
            <div className="flex gap-4 items-center">
              {!updateDismissed && (
                <UpdateBanner
                  status={updateStatus}
                  appVersion={appVersion}
                  isBotRunning={isRunning}
                  onCheckNow={handleUpdaterCheck}
                  onDownload={handleUpdaterDownload}
                  onApply={handleUpdaterApply}
                  onUpdateAndRestart={handleUpdaterOneClick}
                  onSkip={handleUpdaterSkip}
                  onClearSkip={handleUpdaterClearSkip}
                  onDismiss={() => setUpdateDismissed(true)}
                />
              )}
              <div className="bg-white dark:bg-zinc-900 px-6 py-3.5 rounded-2xl border border-zinc-100/50 dark:border-zinc-800/50 flex items-center gap-4 shadow-premium dark:shadow-none no-drag backdrop-blur-md">
                <div className="relative">
                  <div className={`w-2.5 h-2.5 rounded-full ${stats.adb_health.consecutive_fails === 0 ? 'bg-emerald-500' : 'bg-rose-500 animate-pulse'}`}></div>
                  {stats.adb_health.consecutive_fails === 0 && <div className="absolute inset-0 w-2.5 h-2.5 rounded-full bg-emerald-500 animate-ping opacity-20"></div>}
                </div>
                <span className="text-[11px] font-black uppercase tracking-widest text-zinc-500 dark:text-zinc-400">Node: localhost:{adbPort}</span>
              </div>
            </div>
          </header>

          {tab === 'dashboard' && <Dashboard {...dashboardProps} />}
          {tab === 'feed' && <Feed screenshot={screenshot} stats={stats} />}
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
            />
          )}
        </div>
      </main>
    </div>
  );
}

export default App;
