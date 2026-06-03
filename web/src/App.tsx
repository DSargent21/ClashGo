import {useState, useEffect} from 'react';
import {StartBot, StopBot, GetConfig} from '../wailsjs/go/main/App';
import {EventsOn} from '../wailsjs/runtime/runtime';

function App() {
    const [running, setRunning] = useState(false);
    const [gold, setGold] = useState(500000);
    const [elixir, setElixir] = useState(500000);
    const [darkElixir, setDarkElixir] = useState(5000);
    const [upgradeWalls, setUpgradeWalls] = useState(false);
    const [logs, setLogs] = useState<string[]>([]);
    const [statusMsg, setStatusMsg] = useState('System Ready');

    useEffect(() => {
        GetConfig().then(cfg => {
            if (cfg) {
                if (cfg.search) {
                    setGold(cfg.search.min_loot_gold);
                    setElixir(cfg.search.min_loot_elixir);
                    setDarkElixir(cfg.search.min_loot_de);
                }
                if (cfg.upgrade) {
                    setUpgradeWalls(cfg.upgrade.upgrade_walls);
                }
            }
        });

        EventsOn('bot_error', (msg: string) => {
            setLogs(prev => [...prev.slice(-99), `[ERROR] ${msg}`]);
            setRunning(false);
        });

        EventsOn('bot_log', (msg: string) => {
            // Remove ANSI escape codes if any (though ConsoleWriter handles it if configured)
            const cleanMsg = msg.replace(/\x1b\[[0-9;]*m/g, '').trim();
            if (cleanMsg) {
                setLogs(prev => [...prev.slice(-99), cleanMsg]);
            }
        });
    }, []);

    const toggleBot = async () => {
        if (running) {
            const res = await StopBot();
            setRunning(res.running);
            setStatusMsg(res.message);
        } else {
            const res = await StartBot(gold, elixir, darkElixir, upgradeWalls);
            setRunning(res.running);
            setStatusMsg(res.message);
        }
    };

    return (
        <div className="flex h-screen bg-[#09090b] text-zinc-100 font-sans overflow-hidden">
            {/* Sidebar */}
            <div className="w-64 border-r border-zinc-800 p-6 flex flex-col">
                <div className="flex items-center gap-3 mb-10">
                    <div className="w-8 h-8 bg-emerald-500 rounded-lg flex items-center justify-center shadow-lg shadow-emerald-500/20">
                        <span className="font-bold text-zinc-950">V</span>
                    </div>
                    <h1 className="text-xl font-bold tracking-tight">Vanguard</h1>
                </div>

                <nav className="flex-1 space-y-1">
                    <button className="w-full text-left px-4 py-2 rounded-md bg-zinc-800/50 text-emerald-400 font-medium">Dashboard</button>
                    <button className="w-full text-left px-4 py-2 rounded-md text-zinc-400 hover:text-zinc-100 transition-colors">Settings</button>
                    <button className="w-full text-left px-4 py-2 rounded-md text-zinc-400 hover:text-zinc-100 transition-colors">Analytics</button>
                </nav>

                <div className="pt-6 border-t border-zinc-800">
                    <div className="text-xs text-zinc-500 uppercase font-bold tracking-wider mb-2">Status</div>
                    <div className="flex items-center gap-2">
                        <div className={`w-2 h-2 rounded-full ${running ? 'bg-emerald-500 animate-pulse' : 'bg-rose-500'}`} />
                        <span className="text-sm">{statusMsg}</span>
                    </div>
                </div>
            </div>

            {/* Main Content */}
            <main className="flex-1 p-10 flex flex-col gap-8 overflow-y-auto">
                <header className="flex justify-between items-center">
                    <div>
                        <h2 className="text-3xl font-bold">Cockpit</h2>
                        <p className="text-zinc-500">Mission control for your automated operations.</p>
                    </div>
                    
                    <button 
                        onClick={toggleBot}
                        className={`px-8 py-3 rounded-xl font-bold text-lg transition-all transform active:scale-95 shadow-2xl ${
                            running 
                            ? 'bg-rose-600 hover:bg-rose-500 shadow-rose-900/20 text-white' 
                            : 'bg-emerald-600 hover:bg-emerald-500 shadow-emerald-900/20 text-white'
                        }`}
                    >
                        {running ? 'STOP MISSION' : 'START MISSION'}
                    </button>
                </header>

                {/* Thresholds Grid */}
                <div className="grid grid-cols-1 md:grid-cols-4 gap-6">
                    <div className="bg-zinc-900/50 border border-zinc-800 rounded-2xl p-6 hover:border-zinc-700 transition-colors">
                        <div className="text-zinc-500 text-sm font-bold uppercase tracking-wider mb-4">Gold Threshold</div>
                        <div className="flex items-end gap-2">
                            <input 
                                type="number" 
                                value={gold} 
                                onChange={(e) => setGold(parseInt(e.target.value))}
                                className="bg-transparent text-4xl font-bold w-full outline-none focus:text-emerald-400 transition-colors"
                            />
                            <span className="text-zinc-600 font-bold mb-1">k</span>
                        </div>
                    </div>

                    <div className="bg-zinc-900/50 border border-zinc-800 rounded-2xl p-6 hover:border-zinc-700 transition-colors">
                        <div className="text-zinc-500 text-sm font-bold uppercase tracking-wider mb-4">Elixir Threshold</div>
                        <div className="flex items-end gap-2">
                            <input 
                                type="number" 
                                value={elixir} 
                                onChange={(e) => setElixir(parseInt(e.target.value))}
                                className="bg-transparent text-4xl font-bold w-full outline-none focus:text-emerald-400 transition-colors"
                            />
                            <span className="text-zinc-600 font-bold mb-1">k</span>
                        </div>
                    </div>

                    <div className="bg-zinc-900/50 border border-zinc-800 rounded-2xl p-6 hover:border-zinc-700 transition-colors">
                        <div className="text-zinc-500 text-sm font-bold uppercase tracking-wider mb-4">Dark Elixir</div>
                        <div className="flex items-end gap-2">
                            <input 
                                type="number" 
                                value={darkElixir} 
                                onChange={(e) => setDarkElixir(parseInt(e.target.value))}
                                className="bg-transparent text-4xl font-bold w-full outline-none focus:text-emerald-400 transition-colors"
                            />
                            <span className="text-zinc-600 font-bold mb-1">DE</span>
                        </div>
                    </div>

                    <div className="bg-zinc-900/50 border border-zinc-800 rounded-2xl p-6 hover:border-zinc-700 transition-colors flex flex-col justify-between">
                        <div className="text-zinc-500 text-sm font-bold uppercase tracking-wider mb-2">Wall Upgrade</div>
                        <div className="flex items-center justify-between mt-auto">
                            <span className="text-zinc-400 font-medium">{upgradeWalls ? 'ENABLED' : 'DISABLED'}</span>
                            <label className="relative inline-flex items-center cursor-pointer">
                                <input 
                                    type="checkbox" 
                                    checked={upgradeWalls} 
                                    onChange={(e) => setUpgradeWalls(e.target.checked)}
                                    className="sr-only peer"
                                />
                                <div className="w-11 h-6 bg-zinc-700 peer-focus:outline-none rounded-full peer peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-zinc-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all peer-checked:bg-emerald-500"></div>
                            </label>
                        </div>
                    </div>
                </div>

                {/* Log Terminal */}
                <div className="flex-1 bg-zinc-950 border border-zinc-800 rounded-2xl flex flex-col overflow-hidden">
                    <div className="bg-zinc-900/50 px-4 py-2 border-b border-zinc-800 flex items-center justify-between">
                        <span className="text-xs font-bold text-zinc-500 uppercase tracking-widest">Telemetry Log</span>
                        <div className="flex gap-1.5">
                            <div className="w-2.5 h-2.5 rounded-full bg-zinc-800" />
                            <div className="w-2.5 h-2.5 rounded-full bg-zinc-800" />
                            <div className="w-2.5 h-2.5 rounded-full bg-zinc-800" />
                        </div>
                    </div>
                    <div className="flex-1 p-4 font-mono text-sm overflow-y-auto space-y-1">
                        {logs.length === 0 && <div className="text-zinc-700 italic">Waiting for telemetry...</div>}
                        {logs.map((log, i) => (
                            <div key={i} className="text-zinc-400">
                                <span className="text-zinc-600">[{new Date().toLocaleTimeString()}]</span> {log}
                            </div>
                        ))}
                    </div>
                </div>
            </main>
        </div>
    );
}

export default App;
