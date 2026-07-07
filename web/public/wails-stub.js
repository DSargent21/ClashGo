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

        window.go = {
            main: {
                App: {
                    GetConfig: resolve({
                        search: { min_loot_gold: 400000, min_loot_elixir: 400000, min_loot_de: 2000, enabled: true },
                        upgrade: { upgrade_walls: false },
                        attack: { strategy_file: 'default.csv', stall_timer_seconds: 30 },
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
                        attacks_completed: 42,
                        search_skips: 7,
                        total_gold: 1854321,
                        total_elixir: 2109876,
                        total_de: 5421,
                        stars_0: 3,
                        stars_1: 5,
                        stars_2: 12,
                        stars_3: 22,
                        uptime: 3600000000000,
                        adb_health: {
                            avg_capture_ms: 73,
                            consecutive_fails: 0,
                            captures_total: 0,
                            errors_total: 0,
                            last_error: '',
                        },
                    }),
                    GetAttackHistory: resolve([]),
                    GetLogs: resolve([
                        'ClashGO mission control online',
                        'Connected to ADB on port 5555',
                        'Ready — press START BOT to begin',
                    ]),
                    GetLiveScreenshot: resolve(''),
                    SaveConfig: resolve(null),
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
