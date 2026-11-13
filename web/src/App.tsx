import { useState, useEffect, useMemo, useCallback } from 'react';
import Sidebar from './components/Sidebar';
import Dashboard from './components/Dashboard';
import AccountsView from './components/AccountsView';
import Analytics from './components/Analytics';
import ArmyView from './components/ArmyView';
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
  SaveUpgradeSetup,
  SaveLaunchSettings,
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
  GetLaunchOptions,
  ChooseArmy,
} from '../wailsjs/go/main/App';
import { bot, main } from '../wailsjs/go/models';
import {
  TabType,
  UpdateStatus,
  DEFAULT_UPDATE_STATUS,
  AccountInput,
  AccountsState,
  Rotation,
} from './types';
import * as accountsApi from './accountsApi';
import * as armyApi from './armyApi';
import type { ArmyPlan } from './armyApi';
import UpdateBanner from './components/UpdateBanner';
import LaunchSetup, { type LaunchTuning, type LaunchUpgrades } from './components/LaunchSetup';
import Icon from './components/ui/Icon';
import { formatClock } from './utils';

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
    // Command is a dark-first interface. An explicit light choice persists
    // through the Settings toggle; new installs start dark to match the
    // app's window chrome and the painted --bg token (no OS guessing).
    return true;
  } catch {
    return true;
  }
};

const getInitialSidebarExpanded = (): boolean => {
  try {
    const stored = localStorage.getItem('sidebarExpanded');
    if (stored !== null) return stored === 'true';
    // Start as a rail on narrow windows; the explicit toggle still wins.
    return window.innerWidth > 1150;
  } catch {
    return true;
  }
};

const TAB_LABELS: Record<TabType, string> = {
  dashboard: 'Dashboard',
  army: 'Army',
  accounts: 'Accounts',
  analytics: 'Analytics',
  config: 'Config',
  settings: 'Settings',
};

// The launch screen is a first-run affair: it appears until a choice has been
// made (or manual setup was picked), then never again.
const getInitialLaunchOpen = (): boolean => {
  // ?setup=1 replays the wizard without touching the saved "seen it" flag.
  try {
    if (new URLSearchParams(window.location.search).get('setup') === '1') return true;
  } catch {
    // Ignore malformed URLs and fall back to the stored flag.
  }
  try {
    return localStorage.getItem('launchSetupDone') !== '1';
  } catch {
    return false;
  }
};

const getInitialTab = (): TabType => {
  try {
    const stored = localStorage.getItem('defaultTab');
    if (stored && Object.prototype.hasOwnProperty.call(TAB_LABELS, stored)) {
      return stored as TabType;
    }
  } catch {
    // fall through
  }
  return 'dashboard';
};

// Shallow value checks used to reject no-op IPC payloads (see fetchData).
const sameStringList = (a: string[], b: string[]): boolean => {
  if (a === b) return true;
  if (!Array.isArray(b) || a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
  return true;
};

const samePayload = (a: unknown, b: unknown): boolean => {
  if (a === b) return true;
  try {
    return JSON.stringify(a) === JSON.stringify(b);
  } catch {
    return false;
  }
};

const getInitialBoolean = (key: string, fallback: boolean): boolean => {
  try {
    const stored = localStorage.getItem(key);
    if (stored !== null) return stored === 'true';
  } catch {
    // fall through
  }
  return fallback;
};

/** Self-contained ticker so the 1 Hz clock cannot re-render the views. */
const Clock = ({ use24Hour }: { use24Hour: boolean }) => {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = window.setInterval(() => setNow(new Date()), 1000);
    return () => window.clearInterval(id);
  }, []);
  return <span className="clock">{formatClock(now, use24Hour)}</span>;
};

function App() {
  const [tab, setTab] = useState<TabType>(getInitialTab);
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
  // The launch timestamp separates this session's attacks from the persistent
  // history used by Analytics > Attack activity.
  const [sessionStart] = useState(() => Date.now());
  const [logs, setLogs] = useState<string[]>([]);
  const [adbPort, setAdbPort] = useState(5555);
  const [darkMode, setDarkMode] = useState(getInitialDarkMode);
  const [sidebarExpanded, setSidebarExpanded] = useState(getInitialSidebarExpanded);
  const [reduceMotion, setReduceMotion] = useState(() => getInitialBoolean('reduceMotion', false));
  const [compactMode, setCompactMode] = useState(() => getInitialBoolean('compactMode', false));
  const [clock24h, setClock24h] = useState(() => getInitialBoolean('clock24h', true));
  const [confirmStop, setConfirmStop] = useState(() => getInitialBoolean('confirmStop', false));
  const [defaultTab, setDefaultTab] = useState<TabType>(getInitialTab);

  // Updater state — pushed via `updater_status` event from Go.
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus>(DEFAULT_UPDATE_STATUS);
  const [appVersion, setAppVersion] = useState('');
  const [isUpdateModalOpen, setIsUpdateModalOpen] = useState(false);

  // Launch quick-setup state.
  const [launchOpen, setLaunchOpen] = useState(getInitialLaunchOpen);
  const [launchOptions, setLaunchOptions] = useState<main.LaunchOptions | null>(null);
  const [launchBusy, setLaunchBusy] = useState(false);

  // Multi-account switcher state. Kept at App level because the sidebar,
  // header and Accounts page all read the same active account.
  const [accountsState, setAccountsState] = useState<AccountsState | null>(null);
  const [accountsLoading, setAccountsLoading] = useState(true);
  const [accountsError, setAccountsError] = useState<string | null>(null);

  const sessionHistory = useMemo(
    () => history.filter((report) => {
      const observed = new Date(report.timestamp).getTime();
      return Number.isFinite(observed) && observed >= sessionStart;
    }),
    [history, sessionStart],
  );

  const loadAccounts = useCallback(async (silent = false) => {
    if (!silent) setAccountsLoading(true);
    try {
      const state = await accountsApi.getAccounts();
      setAccountsState(state);
      setAccountsError(null);
    } catch (err) {
      setAccountsError(err instanceof Error ? err.message : String(err));
    } finally {
      if (!silent) setAccountsLoading(false);
    }
  }, []);

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

  // Real strategy YAML plans plus the effective slots/auto-end values.
  const [armyPlans, setArmyPlans] = useState<ArmyPlan[]>([]);
  const [armyLoading, setArmyLoading] = useState(true);
  const [armyError, setArmyError] = useState<string | null>(null);

  // Storage states
  const [haltGold, setHaltGold] = useState(true);
  const [haltElixir, setHaltElixir] = useState(true);
  const [haltDE, setHaltDE] = useState(false);
  const [haltRule, setHaltRule] = useState("ANY");
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

  const refreshArmyPlans = useCallback(async () => {
    setArmyLoading(true);
    try {
      const plans = await armyApi.getArmyPlans();
      setArmyPlans(plans);
      setArmyError(null);
    } catch (err) {
      setArmyError(err instanceof Error ? err.message : String(err));
      throw err;
    } finally {
      setArmyLoading(false);
    }
  }, []);

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
          setHaltRule(conf.storage.halt_rule || "ANY");
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
      try {
        await refreshArmyPlans();
      } catch {
        // The Army page renders the exact bridge/YAML error.
      }
    };
    init();

    // Nothing polls while the window is minimised: the poll does three IPC
    // round-trips plus a React commit every 2s, and none of it is visible.
    // Coming back triggers one immediate fetch so the UI is never stale.
    const fetchData = async () => {
      if (document.hidden) return;
      try {
        const [s, h, l] = await Promise.all([
          GetStats(),
          GetAttackHistory(),
          GetLogs()
        ]);
        // Keep the previous object when the payload is identical: the IPC
        // boundary hands back fresh objects every poll, so without this the
        // whole tree re-commits every 2s even when nothing changed.
        setStats((prev) => samePayload(prev, s) ? prev : s);
        setHistory((prev) => samePayload(prev, h) ? prev : h);
        setLogs((prev) => sameStringList(prev, l) ? prev : l);
      } catch (err) {
        console.error('Data fetch failed:', err);
      }
    };

    fetchData();
    const interval = setInterval(fetchData, 2000);
    const onVisibility = () => {
      if (!document.hidden) void fetchData();
    };
    document.addEventListener('visibilitychange', onVisibility);

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

    // The Go side emits the full switcher document after every account
    // mutation or switch, so cards, the sidebar chip and the header
    // converge without waiting for the next poll.
    const unsubAccounts = safeEventsOn("accounts_changed", (data: AccountsState) => {
      if (data && Array.isArray(data.accounts)) {
        setAccountsState(data);
        setAccountsError(null);
      }
    });

    return () => {
      clearInterval(interval);
      document.removeEventListener('visibilitychange', onVisibility);
      unsubUpdater();
      unsubBotError();
      unsubBotInitFailed();
      unsubAccounts();
    };
  }, [refreshArmyPlans]);

  // Strategy slots or the active file can change outside this page
  // (Config save, direct config.json edit), so refresh when Army opens.
  useEffect(() => {
    if (tab === 'army') void refreshArmyPlans().catch(() => {});
  }, [tab, refreshArmyPlans]);

  // Account state is cheap (one small JSON read per profile) and drives the
  // header chip, so poll it even when the Accounts tab is closed. Mutations
  // still arrive immediately through the accounts_changed event.
  useEffect(() => {
    void loadAccounts();
    const id = window.setInterval(() => {
      if (!document.hidden) void loadAccounts(true);
    }, 5000);
    return () => window.clearInterval(id);
  }, [loadAccounts]);

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

  useEffect(() => {
    document.documentElement.classList.toggle('reduce-motion', reduceMotion);
    try {
      localStorage.setItem('reduceMotion', String(reduceMotion));
    } catch (e) {
      console.warn('Failed to save reduceMotion preference:', e);
    }
  }, [reduceMotion]);

  useEffect(() => {
    document.documentElement.classList.toggle('compact', compactMode);
    try {
      localStorage.setItem('compactMode', String(compactMode));
    } catch (e) {
      console.warn('Failed to save compactMode preference:', e);
    }
  }, [compactMode]);

  useEffect(() => {
    try {
      localStorage.setItem('clock24h', String(clock24h));
    } catch (e) {
      console.warn('Failed to save clock24h preference:', e);
    }
  }, [clock24h]);

  useEffect(() => {
    try {
      localStorage.setItem('confirmStop', String(confirmStop));
    } catch (e) {
      console.warn('Failed to save confirmStop preference:', e);
    }
  }, [confirmStop]);

  useEffect(() => {
    try {
      localStorage.setItem('defaultTab', defaultTab);
    } catch (e) {
      console.warn('Failed to save defaultTab preference:', e);
    }
  }, [defaultTab]);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'b') {
        event.preventDefault();
        setSidebarExpanded((value) => !value);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

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
      haltRule,
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
    try {
      await refreshArmyPlans();
    } catch {
      // The plan refresher already stored the error for the Army page.
    }
  };

  const handleStart = async () => {
    try {
      const res = await StartBot(goldThreshold, elixirThreshold, deThreshold, upgradeWalls, searchEnabled);
      setIsRunning(res.running);
      await loadAccounts(true);
    } catch (err) {
      console.error('Start failed:', err);
    }
  };

  const handleStop = async () => {
    if (isRunning && confirmStop && !window.confirm('Stop the running bot?')) return;
    try {
      const res = await StopBot();
      setIsRunning(res.running);
      await loadAccounts(true);
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

  // --- Account switcher handlers -----------------------------------------
  // Errors intentionally propagate: AccountsView catches them and shows the
  // exact Go error instead of a generic toast.
  const handleSwitchAccount = async (id: string, start: boolean) => {
    const res = await accountsApi.switchAccount(id, start);
    setIsRunning(res.started || (await IsRunning()));
    await loadAccounts(true);
  };

  const handleSaveAccount = async (id: string | null, input: AccountInput) => {
    if (id) {
      await accountsApi.updateAccount(id, input);
    } else {
      await accountsApi.createAccount(input);
    }
    await loadAccounts(true);
  };

  const handleDeleteAccount = async (id: string) => {
    await accountsApi.deleteAccount(id);
    await loadAccounts(true);
  };

  const handleResetAccountUsage = async (id: string) => {
    await accountsApi.resetAccountUsage(id);
    await loadAccounts(true);
  };

  const handleRotation = async (rotation: Rotation) => {
    await accountsApi.setRotation(rotation);
    await loadAccounts(true);
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
    history: sessionHistory,
    logs,
  }), [stats, sessionHistory, logs]);

  const finishLaunch = useCallback(() => {
    try {
      localStorage.setItem('launchSetupDone', '1');
    } catch {
      // No storage: the screen simply shows again next launch.
    }
    setLaunchOpen(false);
  }, []);

  useEffect(() => {
    if (!launchOpen || launchOptions) return;
    let cancelled = false;
    GetLaunchOptions()
      .then((options) => {
        if (!cancelled) setLaunchOptions(options);
      })
      .catch((err) => {
        // Without the binding there is no real choice to render; fall through
        // to the normal UI instead of trapping the user behind a spinner.
        console.warn('GetLaunchOptions failed:', err);
        if (!cancelled) finishLaunch();
      });
    return () => {
      cancelled = true;
    };
  }, [launchOpen, launchOptions, finishLaunch]);

  const handleChooseArmy = useCallback(async (file: string, armySlot: number, upgrades: LaunchUpgrades, tuning: LaunchTuning) => {
    setLaunchBusy(true);
    try {
      await ChooseArmy(file, tuning.adaptive, armySlot);
      await SaveUpgradeSetup(
        upgrades.enabled,
        upgrades.mode,
        upgrades.heroes,
        upgrades.army,
        upgrades.defenses,
        upgrades.resources,
        upgrades.traps,
        upgrades.wallBuilder,
      );
      await SaveLaunchSettings(tuning.minGold, tuning.minElixir, tuning.minDark, tuning.dryRun);
      setGoldThreshold(tuning.minGold);
      setElixirThreshold(tuning.minElixir);
      setDeThreshold(tuning.minDark);
      setDryRun(tuning.dryRun);
      setAutoBuilders(upgrades.enabled);
      setUpgradeStrategy(upgrades.mode);
      setUpgradeHeroes(upgrades.heroes);
      setUpgradeArmy(upgrades.army);
      setUpgradeDefenses(upgrades.defenses);
      setUpgradeResources(upgrades.resources);
      setUpgradeTraps(upgrades.traps);
      setUpgradeWalls(upgrades.wallBuilder);
      setReserveBuilders(upgrades.wallBuilder ? 1 : 0);
      // Mirror what was written into config.json so the Config form and the
      // Army page agree with the choice immediately.
      const conf = await GetConfig();
      setSelectedStrategy(conf.attack.strategy_file);
      setSearchEnabled(conf.search.enabled);
      await refreshArmyPlans().catch(() => undefined);
      finishLaunch();
      setTab('dashboard');
    } catch (err) {
      console.error('ChooseArmy failed:', err);
    } finally {
      setLaunchBusy(false);
    }
  }, [finishLaunch, refreshArmyPlans]);

  const handleManualSetup = useCallback(() => {
    finishLaunch();
    setTab('config');
  }, [finishLaunch]);

  const configProps = useMemo(() => {
    // Prefer the effective values reported by Go so the config page agrees
    // with the recipe and auto-end the bot actually arms.
    const stratBase = selectedStrategy.split('/').pop() || '';
    const activePlan = armyPlans.find((plan) => plan.active && plan.file === stratBase);
    const currentArmySlot = activePlan?.army_slot
      ?? strategySlots[stratBase]
      ?? (stratBase.includes('valk') ? 4 : 1);
    const currentAutoEnd = activePlan
      ? activePlan.end_at_percent > 0
      : (strategyAutoEnd[stratBase] ?? stratBase.toLowerCase().includes('valk'));

    const handleSetArmySlot = async (slot: number) => {
      setStrategySlots(prev => ({ ...prev, [stratBase]: slot }));
      try {
        await SetStrategyArmySlot(selectedStrategy, slot);
        await refreshArmyPlans();
      } catch (err) {
        console.error('Failed to set strategy army slot:', err);
        setArmyError(err instanceof Error ? err.message : String(err));
      }
    };

    const handleSetAutoEnd = async (enabled: boolean) => {
      setStrategyAutoEnd(prev => ({ ...prev, [stratBase]: enabled }));
      try {
        await SetStrategyAutoEnd(selectedStrategy, enabled);
        await refreshArmyPlans();
      } catch (err) {
        console.error('Failed to set strategy auto end:', err);
        setArmyError(err instanceof Error ? err.message : String(err));
      }
    };

    const handleUseArmy = async (file: string) => {
      try {
        await SaveConfig(
          goldThreshold,
          elixirThreshold,
          deThreshold,
          upgradeWalls,
          file,
          searchEnabled,
          stallTimer,
        );
        setSelectedStrategy(file);
        await refreshArmyPlans();
      } catch (err) {
        setArmyError(err instanceof Error ? err.message : String(err));
        throw err;
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
      onActivateArmy: handleUseArmy,
      haltGold, setHaltGold,
      haltElixir, setHaltElixir,
      haltDE, setHaltDE,
      haltRule, setHaltRule,
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
    selectedStrategy, strategies, searchEnabled, upgradeWalls, stallTimer, strategySlots, strategyAutoEnd, armyPlans, refreshArmyPlans,
    haltGold, haltElixir, haltDE, haltRule, storageThreshold, dumpToWallsBeforeHalt, autoCollect,
    scheduleEnabled, activeHoursStart, activeHoursEnd, sessionDuration, breakDuration, forceStopOnBreak, maxPerHour, maxPerDay,
    autoBuilders, reserveBuilders, upgradeStrategy, upgradeDefenses, upgradeResources, upgradeArmy, upgradeTraps, upgradeHeroes,
    maxGoldPerUpgrade, maxElixirPerUpgrade, maxDEPerUpgrade,
    autoLab, labStrategy, labAllowDE, autoPets, petPriorities,
    wallResource, wallMinGoldReserve, wallMinElixirReserve,
    dryRun,
  ]);

  return (
    <div className="app-shell">
      <Sidebar
        tab={tab}
        setTab={setTab}
        expanded={sidebarExpanded}
        running={isRunning}
        onStart={handleStart}
        onStop={handleStop}
      />

      <main className={`main ${sidebarExpanded ? '' : 'main--collapsed'}`}>
        <header className="topbar draggable">
          <div className="topbar-left">
            <button
              type="button"
              onClick={() => setSidebarExpanded(!sidebarExpanded)}
              className="icon-btn plain no-drag"
              aria-label={sidebarExpanded ? 'Collapse sidebar' : 'Expand sidebar'}
              aria-expanded={sidebarExpanded}
              aria-controls="app-sidebar"
              title={`${sidebarExpanded ? 'Collapse' : 'Expand'} sidebar (⌘B)`}
            >
              <Icon name="sidebar" size={16} />
            </button>
            <div className="crumb no-drag">
              <span>ClashGO</span>
              <Icon name="chevronRight" size={12} />
              <b>{TAB_LABELS[tab]}</b>
            </div>
          </div>

          <div className="top-actions">
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
            <Clock use24Hour={clock24h} />
          </div>
        </header>

        <div className="content">
          {tab === 'dashboard' && (
            <Dashboard
              {...dashboardProps}
              running={isRunning}
              strategy={selectedStrategy}
              adbPort={adbPort}
            />
          )}
          {tab === 'army' && (
            <ArmyView
              plans={armyPlans}
              loading={armyLoading}
              error={armyError}
              onSetSlot={configProps.onSetArmySlot}
              onActivate={configProps.onActivateArmy}
            />
          )}
          {tab === 'accounts' && (
            <AccountsView
              state={accountsState}
              loading={accountsLoading}
              error={accountsError}
              onRefresh={() => void loadAccounts()}
              onSwitch={handleSwitchAccount}
              onStop={handleStop}
              onSave={handleSaveAccount}
              onDelete={handleDeleteAccount}
              onResetUsage={handleResetAccountUsage}
              onRotation={handleRotation}
            />
          )}
          {tab === 'analytics' && (
            <Analytics stats={stats} history={sessionHistory} activityHistory={history} />
          )}
          {tab === 'config' && <ConfigView {...configProps} />}
          {tab === 'settings' && (
            <SettingsView
              stats={stats}
              adbPort={adbPort}
              darkMode={darkMode}
              setDarkMode={setDarkMode}
              reduceMotion={reduceMotion}
              setReduceMotion={setReduceMotion}
              compactMode={compactMode}
              setCompactMode={setCompactMode}
              clock24h={clock24h}
              setClock24h={setClock24h}
              defaultTab={defaultTab}
              setDefaultTab={setDefaultTab}
              confirmStop={confirmStop}
              setConfirmStop={setConfirmStop}
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

      {launchOpen ? (
        <LaunchSetup
          options={launchOptions}
          busy={launchBusy}
          onChoose={(file, slot, upgrades, tuning) => void handleChooseArmy(file, slot, upgrades, tuning)}
          onManual={handleManualSetup}
        />
      ) : null}
    </div>
  );
}

export default App;
