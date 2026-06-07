import { useState, useEffect, useRef, useCallback, useMemo } from 'react';
import { 
  StartBot, StopBot, GetConfig, GetStats, 
  GetAttackHistory, GetLiveScreenshot, SaveConfig, 
  GetStrategies, IsRunning 
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

  // Load configs on start
  useEffect(() => {
    loadConfigData();
    
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

    <div className="app-shell" style={{ display: 'flex', width: '100vw', height: '100vh', backgroundColor: '#FAFAFA' }}>
      <Sidebar {...sidebarProps} />

      <main 
        className={`flex-1 transition-[margin-left] duration-300 ease-[cubic-bezier(0.4,0,0.2,1)] min-h-screen overflow-y-auto ${sidebarExpanded ? 'ml-64' : 'ml-20'}`}
      >
        <div className="draggable sticky top-0 left-0 right-0 h-12 z-40 bg-transparent pointer-events-auto" />
        <div className="max-w-[1600px] mx-auto p-4 md:p-8 lg:p-12 xl:p-20 pt-0 -mt-12">
          <header className="mb-14 flex justify-between items-end draggable">

          <div className="space-y-1">
            <div className="flex items-center gap-3">
              <div className="flex gap-1">
                <span className="w-1 h-1 bg-zinc-950 rounded-full animate-pulse"></span>
                <span className="w-1 h-1 bg-zinc-300 rounded-full"></span>
              </div>
              <h2 className="text-[10px] text-zinc-400 uppercase tracking-[0.3em] font-black">Operation: ClashGO</h2>
            </div>
            <h1 className="font-headline text-4xl font-bold tracking-tight capitalize text-zinc-950">{tab}</h1>
          </div>
          <div className="flex gap-4">
            <div className="bg-white px-5 py-3 rounded-2xl border border-zinc-100/50 flex items-center gap-3 shadow-premium no-drag">
              <div className={`w-2 h-2 rounded-full shadow-[0_0_8px_rgba(16,185,129,0.5)] ${stats.adb_health.consecutive_fails === 0 ? 'bg-emerald-500' : 'bg-rose-500 animate-pulse'}`}></div>
              <span className="text-[10px] font-black uppercase tracking-widest text-zinc-500">Node: {adbHost}</span>
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

        {tab === 'settings' && <SettingsView stats={stats} adbPort={adbPort} />}
        </div>
      </main>


    </div>
  );
}

export default App;
