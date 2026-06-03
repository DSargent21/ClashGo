import { useState, useEffect, useRef } from 'react';
import { StartBot, StopBot, GetConfig, GetStats, GetAttackHistory, GetLiveScreenshot, SaveConfig, GetStrategies, IsRunning } from '../wailsjs/go/main/App';
import { EventsOn } from '../wailsjs/runtime/runtime';
import './App.css';
import logo from './assets/logo.jpg';

interface BotStats {
  attacks_completed: number;
  search_skips: number;
  total_gold: number;
  total_elixir: number;
  total_de: number;
  stars_0: number;
  stars_1: number;
  stars_2: number;
  stars_3: number;
  uptime: number; // nanoseconds
  adb_health: {
    avg_capture_ms: number;
    consecutive_fails: number;
  };
}

interface AttackReport {
  timestamp: string;
  strategy: string;
  target_edge: string;
  deploy_success: boolean;
  undeployed_slots: number;
  deploy_error?: string;
  parsed_results: boolean;
  stars: number;
  gold_stolen: number;
  elixir_stolen: number;
  dark_elixir_stolen: number;
  total_attacks_session: number;
}

function App() {
  const [tab, setTab] = useState<'dashboard' | 'feed' | 'analytics' | 'config' | 'settings'>('dashboard');
  const [running, setRunning] = useState(false);
  const [statusMsg, setStatusMsg] = useState('SYSTEM STABLE');
  
  // Config parameters
  const [goldThreshold, setGoldThreshold] = useState(750000);
  const [elixirThreshold, setElixirThreshold] = useState(750000);
  const [deThreshold, setDeThreshold] = useState(2000);
  const [upgradeWalls, setUpgradeWalls] = useState(false);
  const [searchEnabled, setSearchEnabled] = useState(true);
  const [strategies, setStrategies] = useState<string[]>([]);
  const [selectedStrategy, setSelectedStrategy] = useState('');
  const [sidebarExpanded, setSidebarExpanded] = useState(false);

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

  // Load configs on start
  useEffect(() => {
    loadConfigData();
    
    // Subscribe to Wails logs
    const unsubError = EventsOn('bot_error', (msg: string) => {
      setLogs(prev => [...prev.slice(-99), `[ERROR] ${msg}`]);
      setRunning(false);
      setStatusMsg('SYSTEM ERROR');
    });

    const unsubLog = EventsOn('bot_log', (msg: string) => {
      const cleanMsg = msg.replace(/\x1b\[[0-9;]*m/g, '').trim();
      if (cleanMsg) {
        setLogs(prev => [...prev.slice(-99), cleanMsg]);
      }
    });

    return () => {
      unsubError();
      unsubLog();
    };
  }, []);

  // Sync state loop
  useEffect(() => {
    const interval = setInterval(() => {
      GetStats().then((res: any) => {
        if (res) setStats(res);
      });
      GetAttackHistory().then((res: any) => {
        if (res) setHistory(res);
      });
      IsRunning().then((res: boolean) => {
        setRunning(res);
      });
    }, 1000);
    return () => clearInterval(interval);
  }, []);

  // Polling screenshot for feed
  useEffect(() => {
    if (tab !== 'feed') return;
    let active = true;

    const stream = async () => {
      if (!active) return;
      try {
        const base64Img = await GetLiveScreenshot();
        if (base64Img) {
          setScreenshot(`data:image/jpeg;base64,${base64Img}`);
        }
      } catch (err) {
        console.error(err);
      }
      setTimeout(stream, 500);
    };

    stream();
    return () => {
      active = false;
    };
  }, [tab]);

  useEffect(() => {
    terminalEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [logs]);

  const loadConfigData = () => {
    GetConfig().then(cfg => {
      if (cfg) {
        if (cfg.search) {
          setGoldThreshold(cfg.search.min_loot_gold);
          setElixirThreshold(cfg.search.min_loot_elixir);
          setDeThreshold(cfg.search.min_loot_de);
          setSearchEnabled(cfg.search.enabled !== false);
        }
        if (cfg.upgrade) {
          setUpgradeWalls(cfg.upgrade.upgrade_walls);
        }
        if (cfg.attack) {
          setSelectedStrategy(cfg.attack.strategy_file.split('/').pop() || '');
        }
      }
    });
    GetStrategies().then(res => {
      if (res) setStrategies(res);
    });
  };

  const startEngine = async () => {
    const res = await StartBot(goldThreshold, elixirThreshold, deThreshold, upgradeWalls, searchEnabled);
    setRunning(res.running);
    setStatusMsg(res.message.toUpperCase());
  };

  const stopEngine = async () => {
    const res = await StopBot();
    setRunning(res.running);
    setStatusMsg(res.message.toUpperCase());
  };

  const saveSettings = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      const stratPath = selectedStrategy ? `assets/strategies/${selectedStrategy}` : '';
      await SaveConfig(goldThreshold, elixirThreshold, deThreshold, upgradeWalls, stratPath, searchEnabled);
      setStatusMsg('CONFIG SAVED');
      setTimeout(() => setStatusMsg(running ? 'RUNNING' : 'SYSTEM STABLE'), 2000);
    } catch (err: any) {
      setStatusMsg('SAVE FAILED');
    }
  };

  const formatUptime = (ns: number) => {
    if (!ns) return '0h 0m';
    const seconds = Math.floor(ns / 1e9);
    const hrs = Math.floor(seconds / 3600);
    const mins = Math.floor((seconds % 3600) / 60);
    return `${hrs}h ${mins}m`;
  };

  const handleMouseMove = (e: React.MouseEvent<HTMLDivElement>) => {
    const shell = e.currentTarget;
    const rect = shell.getBoundingClientRect();
    const emitters = shell.querySelectorAll('.glow-emitter');
    
    const x = (e.clientX - rect.left) / rect.width;
    const y = (e.clientY - rect.top) / rect.height;
    
    emitters.forEach((emitter: any, index) => {
      const shiftX = (x - 0.5) * 40 * (index + 1);
      const shiftY = (y - 0.5) * 40 * (index + 1);
      emitter.style.transform = `translate(${shiftX}px, ${shiftY}px)`;
    });
  };

  return (
    <div className="app-shell" onMouseMove={handleMouseMove}>
      {/* Ambient Glows */}
      <div className="glow-emitter bg-ethereal-cyan" style={{ top: '-100px', left: '-100px' }}></div>
      <div className="glow-emitter bg-ethereal-violet" style={{ bottom: '-100px', right: '-100px', animationDelay: '-5s' }}></div>
      <div className="glow-emitter bg-ethereal-blue" style={{ top: '50%', left: '50%', animationDelay: '-10s', opacity: 0.1 }}></div>

      {/* Collapsible SideNavBar - Animating Width via Manual Button Toggle */}
      <aside 
        className={`h-full flex flex-col border-r border-border-subtle backdrop-blur-2xl z-50 glass-plane shrink-0 transition-all duration-300 ease-in-out relative ${
          sidebarExpanded ? 'w-64' : 'w-20'
        }`}
      >
        <div className="p-4 flex flex-col items-center overflow-hidden">
          <img 
            src={logo} 
            className={`object-contain transition-all duration-300 ${
              sidebarExpanded ? 'w-20 h-20 mb-2' : 'w-10 h-10 mb-0'
            }`} 
            alt="ClashGo Logo" 
          />
          <h1 className={`font-headline text-headline-sm tracking-tighter text-primary font-bold transition-all duration-300 ${
            sidebarExpanded ? 'opacity-100 max-h-10 mt-2' : 'opacity-0 max-h-0 overflow-hidden'
          }`}>
            ClashGo
          </h1>
          <p className={`font-label-sm text-xs text-on-surface-variant opacity-60 transition-all duration-300 ${
            sidebarExpanded ? 'opacity-100 max-h-10' : 'opacity-0 max-h-0 overflow-hidden'
          }`}>
            Engine v4.2.1
          </p>
        </div>

        <nav className="flex-1 px-2 space-y-1 mt-4 overflow-x-hidden flex flex-col">
          <button 
            onClick={() => setTab('dashboard')}
            className={`w-full py-4 px-4 flex items-center transition-all duration-300 ${
              sidebarExpanded ? 'justify-start gap-4' : 'justify-center gap-0'
            } ${
              tab === 'dashboard' ? 'text-primary font-bold border-r-2 border-primary' : 'text-on-surface-variant hover:bg-surface-container-low hover:text-primary'
            }`}
            title="Dashboard"
          >
            <span className="material-symbols-outlined shrink-0" style={{ fontVariationSettings: tab === 'dashboard' ? "'FILL' 1" : "'FILL' 0" }}>dashboard</span>
            <span className={`font-label-sm transition-all duration-300 ${
              sidebarExpanded ? 'opacity-100 w-auto visible translate-x-0' : 'opacity-0 w-0 invisible -translate-x-2'
            }`}>
              Dashboard
            </span>
          </button>
          
          <button 
            onClick={() => setTab('feed')}
            className={`w-full py-4 px-4 flex items-center transition-all duration-300 ${
              sidebarExpanded ? 'justify-start gap-4' : 'justify-center gap-0'
            } ${
              tab === 'feed' ? 'text-primary font-bold border-r-2 border-primary' : 'text-on-surface-variant hover:bg-surface-container-low hover:text-primary'
            }`}
            title="Tactical Feed"
          >
            <span className="material-symbols-outlined shrink-0" style={{ fontVariationSettings: tab === 'feed' ? "'FILL' 1" : "'FILL' 0" }}>videocam</span>
            <span className={`font-label-sm transition-all duration-300 ${
              sidebarExpanded ? 'opacity-100 w-auto visible translate-x-0' : 'opacity-0 w-0 invisible -translate-x-2'
            }`}>
              Tactical Feed
            </span>
          </button>
          
          <button 
            onClick={() => setTab('analytics')}
            className={`w-full py-4 px-4 flex items-center transition-all duration-300 ${
              sidebarExpanded ? 'justify-start gap-4' : 'justify-center gap-0'
            } ${
              tab === 'analytics' ? 'text-primary font-bold border-r-2 border-primary' : 'text-on-surface-variant hover:bg-surface-container-low hover:text-primary'
            }`}
            title="Analytics"
          >
            <span className="material-symbols-outlined shrink-0" style={{ fontVariationSettings: tab === 'analytics' ? "'FILL' 1" : "'FILL' 0" }}>monitoring</span>
            <span className={`font-label-sm transition-all duration-300 ${
              sidebarExpanded ? 'opacity-100 w-auto visible translate-x-0' : 'opacity-0 w-0 invisible -translate-x-2'
            }`}>
              Analytics
            </span>
          </button>
          
          <button 
            onClick={() => setTab('config')}
            className={`w-full py-4 px-4 flex items-center transition-all duration-300 ${
              sidebarExpanded ? 'justify-start gap-4' : 'justify-center gap-0'
            } ${
              tab === 'config' ? 'text-primary font-bold border-r-2 border-primary' : 'text-on-surface-variant hover:bg-surface-container-low hover:text-primary'
            }`}
            title="Engine Config"
          >
            <span className="material-symbols-outlined shrink-0" style={{ fontVariationSettings: tab === 'config' ? "'FILL' 1" : "'FILL' 0" }}>terminal</span>
            <span className={`font-label-sm transition-all duration-300 ${
              sidebarExpanded ? 'opacity-100 w-auto visible translate-x-0' : 'opacity-0 w-0 invisible -translate-x-2'
            }`}>
              Engine Config
            </span>
          </button>

          <button 
            onClick={() => setTab('settings')}
            className={`w-full py-4 px-4 flex items-center transition-all duration-300 ${
              sidebarExpanded ? 'justify-start gap-4' : 'justify-center gap-0'
            } ${
              tab === 'settings' ? 'text-primary font-bold border-r-2 border-primary' : 'text-on-surface-variant hover:bg-surface-container-low hover:text-primary'
            }`}
            title="Settings"
          >
            <span className="material-symbols-outlined shrink-0" style={{ fontVariationSettings: tab === 'settings' ? "'FILL' 1" : "'FILL' 0" }}>settings</span>
            <span className={`font-label-sm transition-all duration-300 ${
              sidebarExpanded ? 'opacity-100 w-auto visible translate-x-0' : 'opacity-0 w-0 invisible -translate-x-2'
            }`}>
              Settings
            </span>
          </button>
        </nav>
 
        <div className="p-4 transition-all duration-300 overflow-hidden">
          <button 
            onClick={running ? stopEngine : startEngine} 
            className={`w-full font-headline text-label-sm rounded-lg hover:shadow-lg transition-all duration-300 active:scale-95 text-center flex items-center justify-center font-bold ${
              sidebarExpanded ? 'py-3 px-4' : 'py-3 px-0'
            } ${
              running 
                ? 'bg-rose-600 text-white hover:bg-rose-700 shadow-md' 
                : 'bg-primary text-on-primary shadow-md'
            }`}
            title={running ? 'Stop Engine' : 'Start Engine'}
          >
            {sidebarExpanded ? (
              running ? 'Stop Engine' : 'Start Engine'
            ) : (
              <span className="material-symbols-outlined font-bold">
                {running ? 'power_settings_new' : 'play_arrow'}
              </span>
            )}
          </button>
        </div>
      </aside>

      {/* Main Content Canvas */}
      <main className="flex-1 min-h-0 py-6 px-12 overflow-y-auto relative z-10">
        
        {/* TopAppBar */}
        <header className="flex items-start gap-4 mb-6">
          <button 
            onClick={() => setSidebarExpanded(!sidebarExpanded)}
            className="p-2 hover:bg-zinc-100 rounded-lg transition-colors flex items-center justify-center text-on-surface-variant hover:text-primary cursor-pointer shrink-0 mt-0.5"
            title={sidebarExpanded ? "Collapse Sidebar" : "Expand Sidebar"}
          >
            <span className="material-symbols-outlined text-2xl">
              {sidebarExpanded ? 'menu_open' : 'menu'}
            </span>
          </button>
          <div>
            <h2 className="font-headline text-headline-md font-bold text-primary">
              {tab === 'dashboard' && 'Command Center'}
              {tab === 'feed' && 'Tactical Feed'}
              {tab === 'analytics' && 'Operational Analytics'}
              {tab === 'config' && 'Engine Config'}
              {tab === 'settings' && 'Settings'}
            </h2>
            <p className="font-body-md text-on-surface-variant">
              {tab === 'dashboard' && 'Autonomous Strategic Deployment Hub'}
              {tab === 'feed' && 'Live Android Screen Streaming Viewer'}
              {tab === 'analytics' && 'Deployment efficiency and statistics'}
              {tab === 'config' && 'Manage search parameters and strategy selection'}
              {tab === 'settings' && 'General configurations'}
            </p>
          </div>
        </header>

        {/* Dashboard View */}
        {tab === 'dashboard' && (
          <>
            {/* Resource Row */}
            <div className="grid grid-cols-3 gap-gutter mb-6">
              <div className="glass-plane p-6 rounded-2xl antigravity-border overflow-hidden">
                <div className="flex items-center justify-between mb-2">
                  <span className="font-label-sm text-label-sm uppercase tracking-widest text-on-surface-variant">Gold Storage</span>
                  <span className="material-symbols-outlined text-ethereal-cyan">payments</span>
                </div>
                <div className="flex items-baseline gap-2">
                  <span className="font-headline text-4xl font-bold">{stats.total_gold.toLocaleString()}</span>
                  <span className="font-label-sm text-ethereal-cyan">+82k/hr</span>
                </div>
              </div>

              <div className="glass-plane p-6 rounded-2xl antigravity-border overflow-hidden">
                <div className="flex items-center justify-between mb-2">
                  <span className="font-label-sm text-label-sm uppercase tracking-widest text-on-surface-variant">Elixir Reserve</span>
                  <span className="material-symbols-outlined text-ethereal-violet">opacity</span>
                </div>
                <div className="flex items-baseline gap-2">
                  <span className="font-headline text-4xl font-bold">{stats.total_elixir.toLocaleString()}</span>
                  <span className="font-label-sm text-ethereal-violet">+45k/hr</span>
                </div>
              </div>

              <div className="glass-plane p-6 rounded-2xl antigravity-border overflow-hidden">
                <div className="flex items-center justify-between mb-2">
                  <span className="font-label-sm text-label-sm uppercase tracking-widest text-on-surface-variant">Dark Elixir</span>
                  <span className="material-symbols-outlined text-primary">database</span>
                </div>
                <div className="flex items-baseline gap-2">
                  <span className="font-headline text-4xl font-bold">{stats.total_de.toLocaleString()}</span>
                  <span className="font-label-sm text-on-surface-variant">+1.2k/hr</span>
                </div>
              </div>
            </div>

            {/* Attack Log Section */}
            <section className="glass-plane rounded-[2rem] antigravity-border overflow-hidden mb-6">
              <div className="p-6 border-b border-border-subtle flex justify-between items-center bg-white/20">
                <h3 className="font-headline text-headline-md font-bold">Attack Log</h3>
                <div className="flex gap-2">
                  <button className="px-4 py-2 bg-primary text-on-primary rounded-lg text-label-sm font-label-sm">VIEW ALL</button>
                </div>
              </div>
              <div className="overflow-x-auto">
                <table className="w-full text-left">
                  <thead>
                    <tr className="bg-surface-container-low/50">
                      <th className="px-8 py-3 font-label-sm text-xs text-on-surface-variant uppercase tracking-widest">Target Node</th>
                      <th className="px-8 py-3 font-label-sm text-xs text-on-surface-variant uppercase tracking-widest">Loot Gained</th>
                      <th className="px-8 py-3 font-label-sm text-xs text-on-surface-variant uppercase tracking-widest text-center">Stars</th>
                      <th className="px-8 py-3 font-label-sm text-xs text-on-surface-variant uppercase tracking-widest">Timestamp</th>
                      <th className="px-8 py-3 font-label-sm text-xs text-on-surface-variant uppercase tracking-widest text-right">Status</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border-subtle">
                    {history.length === 0 ? (
                      <tr>
                        <td colSpan={5} className="px-8 py-8 text-center text-on-surface-variant italic">No attack reports found. Active operations will populate logs.</td>
                      </tr>
                    ) : (
                      history.slice(0, 4).map((rep, i) => (
                        <tr key={i} className="hover:bg-surface-container-lowest/40 transition-colors">
                          <td className="px-8 py-4">
                            <div className="font-headline font-bold">{rep.strategy}</div>
                            <div className="text-xs text-on-surface-variant opacity-60">Edge: {rep.target_edge}</div>
                          </td>
                          <td className="px-8 py-4">
                            <div className="flex gap-4">
                              <div className="flex items-center gap-1"><span className="w-2 h-2 bg-zinc-800 rounded-full"></span> {rep.gold_stolen.toLocaleString()}</div>
                              <div className="flex items-center gap-1"><span className="w-2 h-2 bg-zinc-500 rounded-full"></span> {rep.elixir_stolen.toLocaleString()}</div>
                              <div className="flex items-center gap-1"><span className="w-2 h-2 bg-zinc-300 rounded-full"></span> {rep.dark_elixir_stolen.toLocaleString()}</div>
                            </div>
                          </td>
                          <td className="px-8 py-4 text-center">
                            <div className="flex justify-center gap-1">
                              {[...Array(3)].map((_, sIdx) => (
                                <span 
                                  key={sIdx} 
                                  className={`material-symbols-outlined scale-75 ${sIdx < rep.stars ? 'text-primary' : 'text-on-surface-variant/20'}`} 
                                  style={{ fontVariationSettings: sIdx < rep.stars ? "'FILL' 1" : "'FILL' 0" }}
                                >
                                  star
                                </span>
                              ))}
                            </div>
                          </td>
                          <td className="px-8 py-4 font-mono text-sm text-on-surface-variant">
                            {new Date(rep.timestamp).toLocaleTimeString()}
                          </td>
                          <td className="px-8 py-4 text-right">
                            <span className="px-3 py-1 bg-zinc-100 border border-zinc-200 text-zinc-900 rounded-full text-xs font-bold">
                              {rep.deploy_success ? 'COMPLETE' : 'FAILED'}
                            </span>
                          </td>
                        </tr>
                      ))
                    )}
                  </tbody>
                </table>
              </div>
            </section>

            {/* Tactical Analytics Nodes */}
            <div className="grid grid-cols-4 gap-gutter mb-6">
              <div className="p-8 border-l border-border-subtle hover:border-primary transition-all duration-300">
                <span className="font-label-sm text-on-surface-variant block mb-2">Search Skips</span>
                <span className="font-headline text-3xl font-bold">{stats.search_skips}</span>
              </div>
              <div className="p-8 border-l border-border-subtle hover:border-primary transition-all duration-300">
                <span className="font-label-sm text-on-surface-variant block mb-2">Number of Attacks</span>
                <span className="font-headline text-3xl font-bold text-primary">{stats.attacks_completed}</span>
              </div>
              <div className="p-8 border-l border-border-subtle hover:border-primary transition-all duration-300">
                <span className="font-label-sm text-on-surface-variant block mb-2">Total Looted</span>
                <span className="font-headline text-3xl font-bold">{((stats.total_gold + stats.total_elixir) / 1e6).toFixed(1)}M</span>
              </div>
              <div className="p-8 border-l border-border-subtle hover:border-primary transition-all duration-300">
                <span className="font-label-sm text-on-surface-variant block mb-2">Uptime</span>
                <span className="font-headline text-3xl font-bold">{formatUptime(stats.uptime)}</span>
              </div>
            </div>

            {/* Live Engine Output Terminal */}
            <section className="mb-6">
              <div className="flex justify-between items-center mb-4">
                <h4 className="font-label-sm text-label-sm uppercase tracking-widest text-on-surface-variant">Neural Logs</h4>
                <div className="flex gap-4">
                  <button onClick={() => setLogs([])} className="text-xs font-label-sm text-primary">CLEAR</button>
                </div>
              </div>
              <div className="glass-plane rounded-2xl p-6 h-48 terminal-scroll overflow-y-auto font-mono text-sm leading-relaxed border border-border-subtle">
                <div className="space-y-1">
                  {logs.length === 0 ? (
                    <div className="text-on-surface-variant opacity-40">Waiting for stream logs...</div>
                  ) : (
                    logs.map((log, i) => (
                      <div key={i} className="flex gap-4">
                        <span className="text-on-surface-variant opacity-40">[{new Date().toLocaleTimeString()}]</span>
                        <span className={
                          log.includes('[ERROR]') ? 'text-zinc-950 font-bold underline' :
                          log.includes('[SUCCESS]') ? 'text-zinc-950 font-bold' :
                          log.includes('[INFO]') ? 'text-zinc-800 font-semibold' : 'text-on-surface-variant'
                        }>
                          {log}
                        </span>
                      </div>
                    ))
                  )}
                  <div ref={terminalEndRef} />
                </div>
              </div>
            </section>
          </>
        )}

        {/* Tactical Feed View */}
        {tab === 'feed' && (
          <div className="glass-plane p-8 rounded-2xl antigravity-border flex flex-col items-center">
            <div className="w-full max-w-[860px] aspect-[860/732] bg-zinc-950 rounded-xl overflow-hidden border border-zinc-800 flex items-center justify-center relative">
              {screenshot ? (
                <img src={screenshot} className="w-full h-full object-contain" alt="Live Feed" />
              ) : (
                <div className="text-zinc-500 font-mono text-sm flex flex-col items-center gap-3">
                  <span className="material-symbols-outlined text-4xl animate-pulse text-ethereal-blue">videocam_off</span>
                  <span>No Screen Capture stream available</span>
                </div>
              )}
            </div>
            <div className="mt-4 flex gap-6 text-xs text-on-surface-variant">
              <div>Average Capture: <span className="font-bold text-primary">{stats.adb_health.avg_capture_ms}ms</span></div>
              <div>Consecutive Fails: <span className="font-bold text-primary">{stats.adb_health.consecutive_fails}</span></div>
            </div>
          </div>
        )}

        {/* Analytics View */}
        {tab === 'analytics' && (
          <div className="grid grid-cols-1 md:grid-cols-2 gap-gutter">
            <div className="glass-plane p-8 rounded-2xl antigravity-border">
              <h3 className="font-headline text-headline-md font-bold mb-6 text-primary">Star Distribution</h3>
              <div className="space-y-4">
                {[
                  { label: '3 Stars (Perfect)', count: stats.stars_3, color: 'bg-ethereal-cyan' },
                  { label: '2 Stars', count: stats.stars_2, color: 'bg-ethereal-blue' },
                  { label: '1 Star', count: stats.stars_1, color: 'bg-ethereal-violet' },
                  { label: '0 Stars (Failed)', count: stats.stars_0, color: 'bg-rose-500' },
                ].map((item, idx) => {
                  const total = stats.stars_3 + stats.stars_2 + stats.stars_1 + stats.stars_0;
                  const percent = total > 0 ? Math.round((item.count / total) * 100) : 0;
                  return (
                    <div key={idx} className="space-y-1">
                      <div className="flex justify-between text-xs font-bold text-zinc-700">
                        <span>{item.label}</span>
                        <span>{item.count} ({percent}%)</span>
                      </div>
                      <div className="w-full bg-zinc-200 h-2 rounded-full overflow-hidden">
                        <div className={`h-full ${item.color}`} style={{ width: `${percent}%` }}></div>
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>

            <div className="glass-plane p-8 rounded-2xl antigravity-border">
              <h3 className="font-headline text-headline-md font-bold mb-6 text-primary">Loot Statistics</h3>
              <div className="space-y-6">
                <div className="flex justify-between items-center border-b border-zinc-200/50 pb-4">
                  <span className="text-xs font-bold text-on-surface-variant uppercase">Total Loot Stolen</span>
                  <span className="text-lg font-black text-primary">{(stats.total_gold + stats.total_elixir).toLocaleString()}</span>
                </div>
                <div className="flex justify-between items-center border-b border-zinc-200/50 pb-4">
                  <span className="text-xs font-bold text-on-surface-variant uppercase">Avg Gold Per Attack</span>
                  <span className="text-lg font-black text-primary">
                    {stats.attacks_completed > 0 ? Math.round(stats.total_gold / stats.attacks_completed).toLocaleString() : '0'}
                  </span>
                </div>
                <div className="flex justify-between items-center">
                  <span className="text-xs font-bold text-on-surface-variant uppercase">Avg Elixir Per Attack</span>
                  <span className="text-lg font-black text-primary">
                    {stats.attacks_completed > 0 ? Math.round(stats.total_elixir / stats.attacks_completed).toLocaleString() : '0'}
                  </span>
                </div>
              </div>
            </div>
          </div>
        )}

        {/* Config view */}
        {tab === 'config' && (
          <div className="glass-plane p-8 rounded-2xl antigravity-border max-w-2xl">
            <form onSubmit={saveSettings} className="space-y-6">
              <div className="grid grid-cols-1 md:grid-cols-2 gap-gutter">
                <div>
                  <label className="block text-xs font-extrabold uppercase text-on-surface-variant mb-2">Min Gold Threshold</label>
                  <input 
                    type="number" 
                    value={goldThreshold} 
                    onChange={e => setGoldThreshold(parseInt(e.target.value) || 0)}
                    disabled={!searchEnabled}
                    className={`w-full bg-white border border-zinc-300 rounded-lg py-2.5 px-4 font-bold text-sm focus:outline-none focus:border-ethereal-blue transition-colors ${!searchEnabled ? 'opacity-50 cursor-not-allowed bg-zinc-50' : ''}`}
                  />
                </div>
                <div>
                  <label className="block text-xs font-extrabold uppercase text-on-surface-variant mb-2">Min Elixir Threshold</label>
                  <input 
                    type="number" 
                    value={elixirThreshold} 
                    onChange={e => setElixirThreshold(parseInt(e.target.value) || 0)}
                    disabled={!searchEnabled}
                    className={`w-full bg-white border border-zinc-300 rounded-lg py-2.5 px-4 font-bold text-sm focus:outline-none focus:border-ethereal-blue transition-colors ${!searchEnabled ? 'opacity-50 cursor-not-allowed bg-zinc-50' : ''}`}
                  />
                </div>
                <div>
                  <label className="block text-xs font-extrabold uppercase text-on-surface-variant mb-2">Min Dark Elixir Threshold</label>
                  <input 
                    type="number" 
                    value={deThreshold} 
                    onChange={e => setDeThreshold(parseInt(e.target.value) || 0)}
                    disabled={!searchEnabled}
                    className={`w-full bg-white border border-zinc-300 rounded-lg py-2.5 px-4 font-bold text-sm focus:outline-none focus:border-ethereal-blue transition-colors ${!searchEnabled ? 'opacity-50 cursor-not-allowed bg-zinc-50' : ''}`}
                  />
                </div>
                <div>
                  <label className="block text-xs font-extrabold uppercase text-on-surface-variant mb-2">Attack Strategy</label>
                  <select
                    value={selectedStrategy}
                    onChange={e => setSelectedStrategy(e.target.value)}
                    className="w-full bg-white border border-zinc-300 rounded-lg py-2.5 px-4 font-bold text-sm focus:outline-none focus:border-ethereal-blue transition-colors"
                  >
                    <option value="">Default Strategy</option>
                    {strategies.map((s, idx) => (
                      <option key={idx} value={s}>{s}</option>
                    ))}
                  </select>
                </div>
              </div>

              <div className="flex items-center justify-between border-t border-zinc-200 pt-6">
                <div>
                  <span className="block text-sm font-bold text-primary">Search Loot Filters</span>
                  <span className="block text-xs text-on-surface-variant">Enable minimum gold, elixir, and dark elixir requirements for attacks</span>
                </div>
                <label className="relative inline-flex items-center cursor-pointer">
                  <input 
                    type="checkbox" 
                    checked={searchEnabled} 
                    onChange={e => setSearchEnabled(e.target.checked)}
                    className="sr-only peer"
                  />
                  <div className="w-11 h-6 bg-zinc-200 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-zinc-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-ethereal-blue"></div>
                </label>
              </div>

              <div className="flex items-center justify-between border-t border-zinc-200 pt-6">
                <div>
                  <span className="block text-sm font-bold text-primary">Upgrade Walls</span>
                  <span className="block text-xs text-on-surface-variant">Perform wall upgrades automatically when builder is free</span>
                </div>
                <label className="relative inline-flex items-center cursor-pointer">
                  <input 
                    type="checkbox" 
                    checked={upgradeWalls} 
                    onChange={e => setUpgradeWalls(e.target.checked)}
                    className="sr-only peer"
                  />
                  <div className="w-11 h-6 bg-zinc-200 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-zinc-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-ethereal-blue"></div>
                </label>
              </div>

              <div className="border-t border-zinc-200 pt-6 flex justify-end">
                <button 
                  type="submit"
                  className="py-3 px-8 bg-zinc-950 text-white font-bold text-sm rounded-lg hover:bg-zinc-800 transition-colors shadow-md"
                >
                  Save Configuration
                </button>
              </div>
            </form>
          </div>
        )}

        {/* General Settings View */}
        {tab === 'settings' && (
          <div className="glass-plane p-8 rounded-2xl antigravity-border max-w-2xl">
            <h3 className="font-headline text-headline-md font-bold mb-6 text-primary">Connection Info</h3>
            <div className="space-y-4 text-sm">
              <div className="flex justify-between border-b pb-2">
                <span className="text-on-surface-variant">ADB Connected</span>
                <span className={stats.adb_health.consecutive_fails === 0 ? 'text-emerald-500 font-bold' : 'text-rose-500 font-bold'}>
                  {stats.adb_health.consecutive_fails === 0 ? 'CONNECTED' : 'DISCONNECTED'}
                </span>
              </div>
              <div className="flex justify-between border-b pb-2">
                <span className="text-on-surface-variant">Device State</span>
                <span className="text-primary font-bold">STABLE</span>
              </div>
              <div className="flex justify-between">
                <span className="text-on-surface-variant">API Port</span>
                <span className="text-primary font-mono font-bold">8080</span>
              </div>
            </div>
          </div>
        )}

      </main>
    </div>
  );
}

export default App;
