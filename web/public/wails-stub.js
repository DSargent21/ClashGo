/**
 * Browser-only stub for the Wails IPC bridge.
 *
 * Loaded via `<script defer src="/wails-stub.js">` in
 * `/index.html` BEFORE the React module script. Vite serves
 * `/web/public/*` as static root so `/wails-stub.js` is the served URL.
 *
 * Why this exists
 * ─────────────────────────────────────────────────────────────────
 * When you run `npm run dev` in `web/` and open
 * `http://localhost:5173/` in plain Chrome, neither `window.go`
 * (the Wails-generated bindings namespace) nor `window.runtime`
 * (the Wails event/log bridge) exist. Every IPC call
 * (`GetConfig`, `GetStats`, `EventsOn`, …) then throws
 * `Cannot read properties of undefined (reading 'main')` or
 * `(reading 'EventsOnMultiple')`. The React tree unmounts on the
 * synchronous `EventsOn` throw and the resulting "black screen"
 * (transparent WkWebView over Wails' dark-zinc window BG) is
 * indistinguishable from a Crash to the user.
 *
 * This stub injects minimal stand-ins so the React app boots, paints
 * the UI, and stays interactive during **frontend-only** work
 * (layout, styling, components). Anything that needs the real ClashGO
 * bot still requires `wails dev` or the production .app.
 *
 * Gating
 * ─────────────────────────────────────────────────────────────────
 * Triple-gated so we never shadow a real Wails session (a packaged
 * .app or `wails dev` WkWebView) with fake data:
 *
 *   1. **wails dev proxy port** — `window.location.port === '34115'`
 *      matches `http://localhost:34115/`, the URL the Wails CLI's
 *      Go-side dev proxy serves the WkWebView at (visible as
 *      `Using DevServer URL: http://localhost:34115` in `wails dev`
 *      output).
 *   2. **wails production scheme** — `window.location.protocol === 'wails:'`
 *      matches the custom scheme the packaged .app uses
 *      (`options.App{Scheme: "wails"}` in main.go sets the bundle id).
 *   3. **Localhost-only fallback** — outside `localhost` and
 *      `127.0.0.1` (a hosted CDN, for example) we let real
 *      Wails/runtime errors surface instead of pretending all is
 *      well with mocks.
 *
 * Wails v2 injects the actual `window.go.main.App.X` proxy functions
 * (and `window.runtime`) into the WkWebView once page navigation
 * completes, but that injection timing is racy with our
 * `<script defer>` install in v2.12.0 — the stub has been observed
 * firing inside a real `wails dev` session where the namespace
 * hasn't yet been populated. The link-locality gates above are what
 * actually keep a real Wails session safe regardless of that race.
 *
 * Order vs the React module
 * ─────────────────────────────────────────────────────────────────
 * `defer` scripts run in document order BEFORE `<script type="module">`
 * executes (HTML spec). So this stub installs `window.go` /
 * `window.runtime` before `web/src/main.tsx` evaluates its
 * `import { EventsOn } from '../wailsjs/runtime';` and
 * `import { GetStats, … } from '../wailsjs/go/main/App';`. The
 * generated runtime.js / App.js then read our stub instead of
 * crashing on `window.runtime.EventsOnMultiple`.
 */
(function installWailsDevStubs() {
    'use strict';
    try {
        // Gating runs BEFORE we mutate `window` so a real Wails
        // session is never shadowed by fake data. Logic below; the
        // long-form WHY is in the file header's "Gating" block.

        const port = String(window.location.port || '');
        const proto = String(window.location.protocol || '');
        const host = String(window.location.hostname || '');

        // Helper for the four skip paths below. Single console.debug
        // call site keeps the `// eslint-disable-next-line no-console`
        // noise down to one (sibling of `console.info` /
        // `console.warn` elsewhere). Tag prefix `[ClashGO dev]` so
        // these are easy to filter; reason is interpolated at the
        // call site. `return skip(...)` short-circuits the IIFE.
        var skip = function (reason) {
            // eslint-disable-next-line no-console
            console.debug('[ClashGO dev] stub skipped: ' + reason);
            return true;
        };

        // 1. PRIMARY — IPC fingerprint. Wails v2's Go-side bridge
        //    posts RPCs to the WkWebView's
        //    `window.webkit.messageHandlers.wails` channel, so any
        //    `window.go.main.App.X` it injects will have that symbol
        //    in its `Function.prototype.toString()` source. If we
        //    detect it, we KNOW the real bridge is live and skip.
        //    (This works regardless of URL: dev / prod / embed.)
        try {
            const appIpc = window.go && window.go.main && window.go.main.App;
            const sampleMethod = appIpc && (appIpc.GetConfig || appIpc.IsRunning);
            if (typeof sampleMethod === 'function' &&
                /webkit\.messageHandlers\.wails/.test(String(sampleMethod))) {
                return skip('real Wails IPC fingerprint on window.go.main.App');
            }
        } catch (_e) { /* fingerprint threw — fall through to URL gates */ }

        // 2. SECONDARY — wails dev proxy port. The Wails CLI defaults
        //    to `http://localhost:34115/` for `wails dev` (Go-side dev
        //    proxy; proxies asset requests to Vite at 5173). Visible as
        //    `Using DevServer URL: http://localhost:34115` in `wails dev`
        //    output.
        //    NOTE: this port is Wails CLI's default — users can override
        //    via `wails.json -> serverUrl` or programmatic
        //    `options.App{DevServer: "..."}`. The IPC fingerprint check
        //    above is the safety net for custom ports, so a custom
        //    devServerUrl will still skip the stub correctly.
        if (port === '34115') return skip('wails dev proxy port 34115');

        // 3. SECONDARY — wails production scheme. The packaged .app
        //    uses Wails' custom `wails://` scheme
        //    (see main.go `options.App{Scheme: "wails"}`).
        if (proto === 'wails:' || proto === 'wails') return skip('wails prod scheme (' + proto + ')');

        // 4. TERTIARY — outside localhost we have no expectation of
        //    `window.go` (a hosted CDN, for example); let real
        //    errors surface instead of mocking up a UI that hides
        //    the actual problem.
        if (host !== 'localhost' && host !== '127.0.0.1') return skip('non-localhost host "' + host + '"');

        var resolve = function (v) { return function () { return Promise.resolve(v); }; };
        var noopUnsubscribe = function () { return function () {}; };
        var tagLog = function (level) {
            return function () {
                var args = Array.prototype.slice.call(arguments);
                args.unshift('[Wails stub]');
                // eslint-disable-next-line no-console
                console[level].apply(console, args);
            };
        };

        // Localhost-only fixtures so frontend work can review the populated
        // dashboard, table, and report drawer. Never installed outside
        // localhost (see the resulting gates above).
        var minutesAgo = function (minutes) {
            return new Date(Date.now() - minutes * 60000).toISOString();
        };
        var sampleHistory = [
            {
                timestamp: minutesAgo(4), strategy: 'assets/strategies/auto_edrag_rush.yaml',
                target_edge: 'right', deploy_success: true, undeployed_slots: 0,
                parsed_results: true, stars: 3, gold_stolen: 684210, elixir_stolen: 592140,
                dark_elixir_stolen: 4120, bonus_gold: 108000, bonus_elixir: 108000, bonus_de: 0,
                total_attacks_session: 42,
            },
            {
                timestamp: minutesAgo(19), strategy: 'assets/strategies/valk_spam.yaml',
                target_edge: 'bottom', deploy_success: true, undeployed_slots: 0,
                parsed_results: true, stars: 2, gold_stolen: 431800, elixir_stolen: 388600,
                dark_elixir_stolen: 2210, bonus_gold: 0, bonus_elixir: 0, bonus_de: 0,
                total_attacks_session: 41,
            },
            {
                timestamp: minutesAgo(37), strategy: 'assets/strategies/auto_edrag_rush.yaml',
                target_edge: 'left', deploy_success: true, undeployed_slots: 1,
                parsed_results: true, stars: 2, gold_stolen: 512400, elixir_stolen: 286100,
                dark_elixir_stolen: 1840, bonus_gold: 0, bonus_elixir: 0, bonus_de: 0,
                total_attacks_session: 40,
            },
            {
                timestamp: minutesAgo(58), strategy: 'assets/strategies/balloon_edrag_rush.yaml',
                target_edge: 'top', deploy_success: true, undeployed_slots: 0,
                parsed_results: true, stars: 3, gold_stolen: 721390, elixir_stolen: 648220,
                dark_elixir_stolen: 5120, bonus_gold: 114000, bonus_elixir: 114000, bonus_de: 0,
                total_attacks_session: 39,
            },
            {
                timestamp: minutesAgo(96), strategy: 'assets/strategies/valk_spam.yaml',
                target_edge: 'right', deploy_success: false, undeployed_slots: 3,
                deploy_error: 'army card slot not readable after deploy window',
                parsed_results: false, stars: 0, gold_stolen: 0, elixir_stolen: 0,
                dark_elixir_stolen: 0, bonus_gold: 0, bonus_elixir: 0, bonus_de: 0,
                total_attacks_session: 38,
            },
            {
                timestamp: minutesAgo(140), strategy: 'assets/strategies/auto_edrag_rush.yaml',
                target_edge: 'left', deploy_success: true, undeployed_slots: 0,
                parsed_results: true, stars: 1, gold_stolen: 198400, elixir_stolen: 164700,
                dark_elixir_stolen: 620, bonus_gold: 0, bonus_elixir: 0, bonus_de: 0,
                total_attacks_session: 37,
            },
            {
                timestamp: minutesAgo(188), strategy: 'assets/strategies/top_edrag_rush.yaml',
                target_edge: 'top', deploy_success: true, undeployed_slots: 0,
                parsed_results: true, stars: 3, gold_stolen: 556300, elixir_stolen: 604900,
                dark_elixir_stolen: 3180, bonus_gold: 96000, bonus_elixir: 96000, bonus_de: 0,
                total_attacks_session: 36,
            },
            {
                timestamp: minutesAgo(242), strategy: 'assets/strategies/bottom_edrag_rush.yaml',
                target_edge: 'bottom', deploy_success: true, undeployed_slots: 0,
                parsed_results: true, stars: 2, gold_stolen: 364200, elixir_stolen: 301500,
                dark_elixir_stolen: 1440, bonus_gold: 0, bonus_elixir: 0, bonus_de: 0,
                total_attacks_session: 35,
            },
        ];

        // Older, localhost-only reports so the activity heatmap and strategy
        // table can be reviewed with multi-week data. Newest stays first.
        for (var day = 1; day <= 300; day += 1) {
            if (day % 4 === 0) continue;
            var hits = day % 9 === 0 ? 4 : day % 5 === 0 ? 2 : 1;
            for (var hit = 0; hit < hits; hit += 1) {
                var stars = (day * 3 + hit) % 4;
                var failed = day % 29 === 0 && hit === 0;
                sampleHistory.push({
                    timestamp: minutesAgo(day * 1440 + hit * 45),
                    strategy: day % 2 === 0
                        ? 'assets/strategies/auto_edrag_rush.yaml'
                        : 'assets/strategies/valk_spam.yaml',
                    target_edge: ['left', 'right', 'top', 'bottom'][(day + hit) % 4],
                    deploy_success: !failed,
                    undeployed_slots: failed ? 2 : 0,
                    deploy_error: failed ? 'army card slot not readable after deploy window' : '',
                    parsed_results: !failed,
                    stars: failed ? 0 : stars,
                    gold_stolen: failed ? 0 : 160000 + stars * 120000 + (day % 7) * 9000,
                    elixir_stolen: failed ? 0 : 140000 + stars * 105000 + (day % 5) * 8000,
                    dark_elixir_stolen: failed ? 0 : stars * 900 + (day % 3) * 120,
                    bonus_gold: stars === 3 ? 96000 : 0,
                    bonus_elixir: stars === 3 ? 96000 : 0,
                    bonus_de: 0,
                    total_attacks_session: 20 + (day % 12),
                });
            }
        }

        var sampleAccounts = [
            {
                id: 'acc-main', name: 'Main village', player_tag: '#2P0Y8LQGR', town_hall: 13,
                notes: 'Primary push account. Heroes down until Friday.',
                device: { adb_host: '127.0.0.1', adb_port: 5555, device_id: 'localhost:5555' },
                enabled: true, priority: 100, cooldown_minutes: 10,
                max_attacks_per_day: 0, max_attacks_per_session: 0,
                created_at: minutesAgo(43200), last_used_at: minutesAgo(4),
                usage: { date: new Date().toISOString().slice(0, 10), attacks: 42, switches: 3 },
                active: true, running: false, today_attacks: 42, status: 'ready',
                stats: {
                    attacks_completed: 42, search_skips: 7, total_gold: 1854321,
                    total_elixir: 2109876, total_de: 5421,
                    stars_0: 3, stars_1: 5, stars_2: 12, stars_3: 22,
                    uptime: 3600000000000, cpu_time_sec: 1380, cpu_cores: 0.14,
                    adb_health: { avg_capture_ms: 73, consecutive_fails: 0, last_error: '' },
                },
            },
            {
                id: 'acc-alt', name: 'Alt · TH11', player_tag: '#8QJ2L0PV', town_hall: 11,
                notes: 'Farming base. Switch only after main hits its daily cap.',
                device: { adb_host: '127.0.0.1', adb_port: 5557, device_id: 'localhost:5557' },
                enabled: true, priority: 60, cooldown_minutes: 25,
                max_attacks_per_day: 40, max_attacks_per_session: 12,
                created_at: minutesAgo(28800), last_used_at: minutesAgo(95),
                usage: { date: new Date().toISOString().slice(0, 10), attacks: 12, switches: 1 },
                active: false, running: false, today_attacks: 12, status: 'cooldown',
                stats: {
                    attacks_completed: 12, search_skips: 3, total_gold: 402100,
                    total_elixir: 388400, total_de: 890,
                    stars_0: 1, stars_1: 2, stars_2: 4, stars_3: 5,
                    uptime: 0, cpu_time_sec: 0, cpu_cores: 0,
                    adb_health: { avg_capture_ms: 0, consecutive_fails: 0, last_error: '' },
                },
            },
        ];

        window.go = {
            main: {
                App: {
                    GetArmyPlans: resolve([
                        {
                            file: 'auto_edrag_rush.yaml',
                            name: 'Auto EDrag Rush',
                            description: 'Professional Balloon + EDrag attack with Double Diamond precision.',
                            emblem: 'Balloon',
                            target_edge: 'Random',
                            army_slot: 1,
                            end_at_percent: 0,
                            active: true,
                            phases: [
                                { name: 'Balloons', units: [{ name: 'Balloon', amount: 'All' }] },
                                { name: 'Electro Dragons', units: [{ name: 'Electro Dragon', amount: 'All' }] },
                                { name: 'Siege Machine', units: [{ name: 'Stone Slammer', amount: 'All' }] },
                                { name: 'Spells', units: [{ name: 'Rage Spell', amount: '5' }, { name: 'Ice Spell', amount: '11' }] },
                                { name: 'Heroes', units: [{ name: 'Barbarian King', amount: 'All' }, { name: 'Archer Queen', amount: 'All' }, { name: 'Grand Warden', amount: 'All' }, { name: 'Minion Prince', amount: 'All' }, { name: 'Dragon Duke', amount: 'All' }, { name: 'Royal Champion', amount: 'All' }] },
                                { name: 'Abilities', units: [{ name: 'Barbarian King', amount: 'All' }, { name: 'Archer Queen', amount: 'All' }, { name: 'Grand Warden', amount: 'All' }, { name: 'Minion Prince', amount: 'All' }, { name: 'Dragon Duke', amount: 'All' }, { name: 'Royal Champion', amount: 'All' }] },
                            ],
                        },
                        {
                            file: 'valk_spam.yaml',
                            name: 'Valkyrie Earthquake Spam',
                            description: 'Spam Valkyries on all 4 sides, followed by Heroes/Siege and Earthquakes.',
                            emblem: 'Valkyrie',
                            target_edge: 'Random',
                            army_slot: 4,
                            end_at_percent: 50,
                            active: false,
                            phases: [
                                { name: 'Valkyrie Spam', units: [{ name: 'Valkyrie', amount: 'All' }] },
                                { name: 'Earthquakes', units: [{ name: 'Earthquake Spell', amount: 'All' }] },
                                { name: 'Siege & Heroes', units: [{ name: 'Stone Slammer', amount: 'All' }, { name: 'Barbarian King', amount: 'All' }, { name: 'Archer Queen', amount: 'All' }, { name: 'Grand Warden', amount: 'All' }, { name: 'Dragon Duke', amount: 'All' }] },
                                { name: 'Abilities', units: [{ name: 'Barbarian King', amount: 'All' }, { name: 'Archer Queen', amount: 'All' }, { name: 'Grand Warden', amount: 'All' }] },
                            ],
                        },
                    ]),
                    GetConfig: resolve({
                        search: { min_loot_gold: 400000, min_loot_elixir: 400000, min_loot_de: 2000, enabled: true },
                        upgrade: { upgrade_walls: false },
                        attack: { strategy_file: 'assets/strategies/auto_edrag_rush.yaml', stall_timer_seconds: 30 },
                        device: { adb_port: 5555 },
                    }),
                    IsRunning: resolve(false),
                    GetStrategies: resolve([
                        'default.csv',
                        'bottom_edrag_rush.yaml',
                        'left_edrag_rush.yaml',
                        'right_edrag_rush.yaml',
                        'top_edrag_rush.yaml',
                        'balloon_edrag_rush.yaml',
                        'valk_spam.yaml',
                        'auto_edrag_rush.yaml',
                    ]),
                    ResetStats: resolve(null),
                    GetStats: resolve({
                        attacks_completed: sampleHistory.length,
                        search_skips: 7,
                        total_gold: sampleHistory.reduce(function (sum, r) { return sum + r.gold_stolen; }, 0),
                        total_elixir: sampleHistory.reduce(function (sum, r) { return sum + r.elixir_stolen; }, 0),
                        total_de: sampleHistory.reduce(function (sum, r) { return sum + r.dark_elixir_stolen; }, 0),
                        stars_0: sampleHistory.filter(function (r) { return r.stars === 0; }).length,
                        stars_1: sampleHistory.filter(function (r) { return r.stars === 1; }).length,
                        stars_2: sampleHistory.filter(function (r) { return r.stars === 2; }).length,
                        stars_3: sampleHistory.filter(function (r) { return r.stars === 3; }).length,
                        uptime: 3600000000000,
                        cpu_time_sec: 1380,
                        cpu_cores: 0.14,
                        adb_health: {
                            avg_capture_ms: 73,
                            consecutive_fails: 0,
                            captures_total: 0,
                            errors_total: 0,
                            last_error: '',
                        },
                    }),
                    GetAttackHistory: resolve(sampleHistory),
                    GetAccounts: resolve({
                        accounts: sampleAccounts,
                        active_id: 'acc-main',
                        rotation: { enabled: false, order: 'round_robin', auto_start: false, attacks_per_account: 20 },
                    }),
                    SetRotation: resolve(null),
                    SwitchAccount: resolve({ account: sampleAccounts[0], started: false, message: 'browser-stub' }),
                    GetLogs: resolve([
                        'ClashGO mission control online',
                        'Connected to ADB on port 5555',
                        'Ready — press START BOT to begin',
                    ]),
                    GetLiveScreenshot: resolve(''),
                    SaveConfig: resolve(null),
                    SetStorageConfig: resolve(null),
                    SetScheduleConfig: resolve(null),
                    SetAutoUpgradeConfig: resolve(null),
                    SetStrategyArmySlot: resolve(null),
                    SetStrategyAutoEnd: resolve(null),
                    GetLaunchOptions: resolve({
                        options: [
                            {
                                file: 'strategies/auto_edrag_rush.yaml',
                                key: 'edrag',
                                name: 'Auto EDrag Rush',
                                pitch: 'Balloon and E-Drag with authored per-unit drops.',
                                loot_per_hour: 5800000,
                                measured: false,
                                raids: 0,
                                recommended: true,
                                army_slot: 1,
                                end_at_percent: 0,
                                loadout: {
                                    army_camp: 352,
                                    spell_capacity: 11,
                                    siege_capacity: 1,
                                    troops: [
                                        { name: 'Electro Dragon', count: 10, slots: 30 },
                                        { name: 'Balloon', count: 10, slots: 5 },
                                    ],
                                    spells: [
                                        { name: 'Rage Spell', count: 5, slots: 2 },
                                        { name: 'Ice Spell', count: 1, slots: 1 },
                                    ],
                                    siege: [{ name: 'Stone Slammer', count: 1 }],
                                },
                            },
                            {
                                file: 'strategies/valk_spam.yaml',
                                key: 'valk',
                                name: 'Valkyrie Earthquake Spam',
                                pitch: 'Valkyries ring the base, earthquakes crack it open, heroes finish.',
                                loot_per_hour: 4900000,
                                measured: false,
                                raids: 0,
                                recommended: false,
                                army_slot: 4,
                                end_at_percent: 50,
                                loadout: {
                                    army_camp: 352,
                                    spell_capacity: 11,
                                    siege_capacity: 1,
                                    troops: [{ name: 'Valkyrie', count: 44, slots: 8 }],
                                    spells: [{ name: 'Earthquake Spell', count: 11, slots: 1 }],
                                    siege: [{ name: 'Stone Slammer', count: 1 }],
                                },
                            },
                        ],
                        adaptive_loot: false,
                        adaptive_ready: false,
                        samples: 0,
                        min_samples: 40,
                        max_army_slots: 4,
                        pet_priorities: ['Phoenix', 'Spirit Fox', 'Sneezy', 'Angry Jelly', 'Frosty', 'Diggy', 'Greedy Raven', 'Unicorn', 'Electro Owl', 'L.A.S.S.I', 'Mighty Yak', 'Poison Lizard'],
                    }),
                    ChooseArmy: resolve(null),
                    GetLootAutotune: resolve({
                        enabled: false,
                        samples: 0,
                        recorded: 0,
                        ready: false,
                        reason: 'learning: 0 of 40 bases read so far',
                        current: { Gold: 400000, Elixir: 400000, DE: 2000 },
                        suggested: { Gold: 400000, Elixir: 400000, DE: 2000 },
                        advice: [],
                    }),
                    ApplyLootAutotune: resolve(null),
                    SetAutoLoot: resolve(null),
                    ResetLootAutotune: resolve(null),
                    TestDeviceMenu: resolve(null),
                    StartBot: resolve({ running: false, message: 'browser-stub' }),
                    StopBot: resolve({ running: false, message: 'browser-stub' }),
                    GetUpdateStatus: resolve({
                        // Empty `latest_version` + `state: 'idle'` so the
                        // `UpdateBanner` stays dormant in browser dev
                        // (the RestartSplash gate checks state ===
                        // 'restarting', which we never set).
                        current_version: '0.1.0-dev',
                        latest_version: '',
                        available: false,
                        state: 'idle',
                        progress: 0,
                        notes: '',
                        release_url: '',
                        asset_name: '',
                        download_path: '',
                        expected_size: 0,
                        downloaded_size: 0,
                        error: '',
                        last_checked_unix: 0,
                        skip_version: '',
                        min_supported: '',
                    }),
                    GetAppVersion: resolve('0.1.0-dev'),
                    CheckForUpdate: resolve({
                        current_version: '0.1.0-dev',
                        latest_version: '',
                        available: false,
                        state: 'up_to_date',
                        progress: 0,
                        notes: '',
                        release_url: '',
                        asset_name: '',
                        download_path: '',
                        expected_size: 0,
                        downloaded_size: 0,
                        error: '',
                        last_checked_unix: 0,
                        skip_version: '',
                        min_supported: '',
                    }),
                    DownloadUpdate: resolve(''),
                    ApplyUpdate: resolve(null),
                    InstallAndRestart: resolve(null),
                    SkipCurrentVersion: resolve(null),
                    ClearSkippedVersion: resolve(null),
                },
            },
        };

        window.runtime = {
            EventsOnMultiple: noopUnsubscribe,
            EventsOn: noopUnsubscribe,
            EventsOff: function () {},
            EventsEmit: function () {},
            LogPrint: tagLog('log'),
            LogTrace: tagLog('log'),
            LogDebug: tagLog('debug'),
            LogInfo: tagLog('info'),
            LogWarning: tagLog('warn'),
            LogError: tagLog('error'),
            LogFatal: tagLog('error'),
        };

        // eslint-disable-next-line no-console
        console.info(
            '[ClashGO dev] window.go + window.runtime stubbed for browser dev on ' + host +
            '. IPC calls return stub data; the React app renders but the bot is NOT wired up.' +
            ' For real IPC: run `wails dev`.',
        );
    } catch (err) {
        // Fail soft: if anything in the stub injection throws (e.g.
        // a future browser blocks console.info or location access),
        // log it but don't block page load.
        // eslint-disable-next-line no-console
        console.warn('[ClashGO dev] stub install failed:', err);
    }
})();
