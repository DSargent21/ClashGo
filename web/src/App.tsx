import React, { useState, useEffect, useMemo, useRef } from 'react';
import Sidebar from './components/Sidebar';
import Dashboard from './components/Dashboard';
import Feed from './components/Feed';
import Analytics from './components/Analytics';
import ConfigView from './components/ConfigView';
import SettingsView from './components/SettingsView';
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
  GetStrategies
} from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime';
import { bot, main } from '../wailsjs/go/models';
import { TabType } from './types';
import './App.css';

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
  const [darkMode, setDarkMode] = useState(true);
  const [sidebarExpanded, setSidebarExpanded] = useState(true);

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

    const unsub = EventsOn("state-change", (data: any) => {
      console.log("State changed:", data);
    });

    return () => {
      clearInterval(interval);
      clearInterval(screenInterval);
      unsub();
    };
  }, []);

  useEffect(() => {
    if (darkMode) {
      document.documentElement.classList.add('dark');
    } else {
      document.documentElement.classList.remove('dark');
    }
  }, [darkMode]);

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
            <div className="flex gap-4">
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
            />
          )}
        </div>
      </main>
    </div>
  );
}

export default App;
