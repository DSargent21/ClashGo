import { useState, useEffect, useRef, useCallback, useMemo } from 'react';
import { 
  StartBot, StopBot, GetConfig, GetStats, 
  GetAttackHistory, GetLiveScreenshot, SaveConfig, 
  GetStrategies, IsRunning, GetLogs
} from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';
import { BotStats, AttackReport, TabType } from './types';
import { cleanLogMessage } from './utils';
import Sidebar from './components/Sidebar';
import Dashboard from './components/Dashboard';
import Feed from './components/Feed';
import Analytics from './components/Analytics';
import ConfigView from './components/ConfigView';
import SettingsView from './components/SettingsView';
import './App.css';

function App() {
  const [tab, setTab] = useState<TabType>('dashboard');
  const [running, setRunning] = useState(false);
  const [statusMsg, setStatusMsg] = useState('SYSTEM STABLE');
  const [sidebarExpanded, setSidebarExpanded] = useState(false);
  const [darkMode, setDarkMode] = useState(() => {
    return localStorage.getItem('darkMode') === 'true' || 
      (!('darkMode' in localStorage) && window.matchMedia('(prefers-color-scheme: dark)').matches);
  });
  
  useEffect(() => {
    if (darkMode) {
      document.documentElement.classList.add('dark');
      localStorage.setItem('darkMode', 'true');
    } else {
      document.documentElement.classList.remove('dark');
      localStorage.setItem('darkMode', 'false');
    }
  }, [darkMode]);

  // Config parameters
  const [goldThreshold, setGoldThreshold] = useState(750000);
  const [elixirThreshold, setElixirThreshold] = useState(750000);
  const [deThreshold, setDeThreshold] = useState(2000);
  const [upgradeWalls, setUpgradeWalls] = useState(false);
  const [searchEnabled, setSearchEnabled] = useState(true);
  const [strategies, setStrategies] = useState<string[]>([]);
  const [selectedStrategy, setSelectedStrategy] = useState('');
  const [adbHost, setAdbHost] = useState('127.0.0.1');
  const [adbPort, setAdbPort] = useState(5037);

  // Live Stats & Logs
  const [stats, setStats] = useState<BotStats>({
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
    adb_health: { avg_capture_ms: 0, consecutive_fails: 0 }
  });
  const [history, setHistory] = useState<AttackReport[]>([]);
  const [logs, setLogs] = useState<string[]>([]);
  const [screenshot, setScreenshot] = useState<string>('');
  
  const terminalEndRef = useRef<HTMLDivElement>(null);

  const loadConfigData = useCallback(() => {
    GetConfig().then(cfg => {
      if (cfg) {
        if (cfg.search) {
          setGoldThreshold(cfg.search.min_loot_gold);
          setElixirThreshold(cfg.search.min_loot_elixir);
          setDeThreshold(cfg.search.min_loot_de);
          setSearchEnabled(cfg.search.enabled !== false);
        }
        if (cfg.upgrade) setUpgradeWalls(cfg.upgrade.upgrade_walls);
        if (cfg.attack) setSelectedStrategy(cfg.attack.strategy_file.split('/').pop() || '');
        if (cfg.device) {
          setAdbHost(cfg.device.adb_host || '127.0.0.1');
          setAdbPort(cfg.device.adb_port || 5037);
        }
      }
    });
    GetStrategies().then(res => { if (res) setStrategies(res as string[]); });
  }, []);

  // Load configs and initial logs on start
  useEffect(() => {
    loadConfigData();
    GetLogs().then(res => {
      if (res) {
        const cleanLogs = res.map(cleanLogMessage).filter(Boolean) as string[];
        setLogs(cleanLogs);
      }
    });
    
    const unsubError = EventsOn('bot_error', (msg: string) => {
      setLogs(prev => [...prev.slice(-99), `[ERROR] ${msg}`]);
      setRunning(false);
      setStatusMsg('SYSTEM ERROR');
    });

    const unsubLog = EventsOn('bot_log', (msg: string) => {
      const cleanMsg = cleanLogMessage(msg);
      if (cleanMsg) {
        setLogs(prev => [...prev.slice(-99), cleanMsg]);
      }
    });

    return () => {
      unsubError();
      unsubLog();
    };
  }, [loadConfigData]);

  // Sync state loop - Optimized polling
  useEffect(() => {
    const fetchBatch = async () => {
      try {
        const [statsRes, historyRes, runningRes] = await Promise.all([
          GetStats(),
          GetAttackHistory(),
          IsRunning()
        ]);
        if (statsRes) setStats(statsRes as BotStats);
        if (historyRes) setHistory(historyRes as AttackReport[]);
        setRunning(runningRes as boolean);
      } catch (err) {
        console.error("Sync loop error:", err);
      }
    };

    const interval = setInterval(fetchBatch, 1000);
    fetchBatch(); // Initial call
    return () => clearInterval(interval);
  }, []);

  // Polling screenshot for feed - Optimized with requestAnimationFrame for smoothness if needed, 
  // but for 500ms polling, keeping it simple but robust.
  useEffect(() => {
    if (tab !== 'feed') return;
    let timer: number;
    let active = true;

    // Listen for live feed events from the bot (High FPS path)
    const unoff = EventsOn("live_feed", (base64Img: string) => {
      if (active && base64Img) {
        setScreenshot(`data:image/jpeg;base64,${base64Img}`);
      }
    });

    const stream = async () => {
      if (!active) return;
      try {
        const base64Img = await GetLiveScreenshot();
        if (base64Img && active) {
          setScreenshot(`data:image/jpeg;base64,${base64Img}`);
        }
      } catch (err) {
        console.error("Screenshot capture failed:", err);
      }
      // If the bot is running, we rely more on the event stream, 
      // but keep polling at a slower rate (or if events aren't firing)
      if (active) timer = window.setTimeout(stream, 1000); 
    };

    stream();
    return () => { 
      active = false; 
      clearTimeout(timer);
      unoff();
    };
  }, [tab]);

  const startEngine = useCallback(async () => {
    setRunning(true);
    setStatusMsg('INITIALIZING...');
    try {
      const res = await StartBot(goldThreshold, elixirThreshold, deThreshold, upgradeWalls, searchEnabled);
      setRunning(res.running);
      setStatusMsg(res.message.toUpperCase());
    } catch (err) {
      setRunning(false);
      setStatusMsg('START FAILED');
    }
  }, [goldThreshold, elixirThreshold, deThreshold, upgradeWalls, searchEnabled]);


  const stopEngine = useCallback(async () => {
    const res = await StopBot();
    setRunning(res.running);
    setStatusMsg(res.message.toUpperCase());
  }, []);

  const saveSettings = useCallback(async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      const stratPath = selectedStrategy ? `assets/strategies/${selectedStrategy}` : '';
      await SaveConfig(goldThreshold, elixirThreshold, deThreshold, upgradeWalls, stratPath, searchEnabled);
      setStatusMsg('CONFIG SAVED');
      setTimeout(() => setStatusMsg(prev => prev === 'CONFIG SAVED' ? (running ? 'RUNNING' : 'SYSTEM STABLE') : prev), 2000);
    } catch (err: any) {
      setStatusMsg('SAVE FAILED');
    }
  }, [goldThreshold, elixirThreshold, deThreshold, upgradeWalls, selectedStrategy, searchEnabled, running]);

  const clearLogs = useCallback(() => setLogs([]), []);

  const sidebarProps = useMemo(() => ({
    tab, setTab, 
    expanded: sidebarExpanded, setExpanded: setSidebarExpanded,
    statusMsg, running,
    onStart: startEngine, onStop: stopEngine
  }), [tab, sidebarExpanded, statusMsg, running, startEngine, stopEngine]);

  const dashboardProps = useMemo(() => ({
    stats, history, logs, screenshot,
    goldThreshold, setGoldThreshold,
    elixirThreshold, setElixirThreshold,
    deThreshold, setDeThreshold,
    selectedStrategy, setSelectedStrategy,
    strategies,
    searchEnabled, setSearchEnabled,
    upgradeWalls, setUpgradeWalls,
    onSave: saveSettings,
    onClearLogs: clearLogs,
    terminalEndRef
  }), [
    stats, history, logs, screenshot,
    goldThreshold, elixirThreshold, deThreshold,
    selectedStrategy, strategies, searchEnabled, upgradeWalls,
    saveSettings, clearLogs
  ]);

  return (

    <div className="app-shell bg-zinc-50 dark:bg-zinc-950 text-zinc-950 dark:text-zinc-50 transition-colors duration-500" style={{ display: 'flex', width: '100vw', height: '100vh' }}>
      <Sidebar {...sidebarProps} />

      <main 
        className={`flex-1 bg-zinc-50 dark:bg-zinc-950 transition-[margin-left,background-color] duration-300 ease-[cubic-bezier(0.4,0,0.2,1)] min-h-screen overflow-y-auto ${sidebarExpanded ? 'ml-64' : 'ml-20'}`}
      >
        <div className="draggable sticky top-0 left-0 right-0 h-12 z-40 bg-transparent pointer-events-auto" />
        <div className="max-w-[1600px] mx-auto p-4 md:p-6 lg:p-8 xl:p-12 pt-0 -mt-12">
          <header className="mb-8 flex justify-between items-end draggable">

          <div className="space-y-1">
            <div className="flex items-center gap-3">
              <div className="flex gap-1">
                <span className="w-1.5 h-1.5 bg-zinc-950 dark:bg-zinc-400 rounded-full animate-pulse"></span>
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
              <span className="text-[11px] font-black uppercase tracking-widest text-zinc-500 dark:text-zinc-400">Node: {adbHost}</span>
            </div>
          </div>
        </header>

        {tab === 'dashboard' && <Dashboard {...dashboardProps} />}

        {tab === 'feed' && <Feed screenshot={screenshot} stats={stats} />}

        {tab === 'analytics' && <Analytics stats={stats} />}

        {tab === 'config' && (
          <ConfigView 
            {...dashboardProps}
            onSave={saveSettings}
          />
        )}

        {tab === 'settings' && <SettingsView stats={stats} adbPort={adbPort} darkMode={darkMode} setDarkMode={setDarkMode} />}
        </div>
      </main>


    </div>
  );
}

export default App;
