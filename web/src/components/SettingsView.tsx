import React from 'react';
import { TestDeviceMenu } from '../../wailsjs/go/main/App';
import { BotStats, TabType, UpdateStatus } from '../types';
import Icon from './ui/Icon';
import { SwitchRow } from './ui/Switch';

interface SettingsViewProps {
  stats: BotStats;
  adbPort: number;
  darkMode: boolean;
  setDarkMode: (val: boolean) => void;
  reduceMotion: boolean;
  setReduceMotion: (val: boolean) => void;
  compactMode: boolean;
  setCompactMode: (val: boolean) => void;
  clock24h: boolean;
  setClock24h: (val: boolean) => void;
  defaultTab: TabType;
  setDefaultTab: (val: TabType) => void;
  confirmStop: boolean;
  setConfirmStop: (val: boolean) => void;
  onResetStats: () => void;
  appVersion: string;
  updateStatus: UpdateStatus;
  onCheckUpdates: () => void;
  onClearSkip: () => void;
  onOpenUpdateModal?: () => void;
  onOpenSetupWizard: () => void;
  isBotRunning: boolean;
}

const DEFAULT_PAGE_OPTIONS: { value: TabType; label: string }[] = [
  { value: 'dashboard', label: 'Dashboard' },
  { value: 'army', label: 'Army' },
  { value: 'analytics', label: 'Analytics' },
  { value: 'accounts', label: 'Accounts' },
  { value: 'config', label: 'Config' },
  { value: 'settings', label: 'Settings' },
];

const SettingsView: React.FC<SettingsViewProps> = React.memo(({
  stats,
  adbPort,
  darkMode,
  setDarkMode,
  reduceMotion,
  setReduceMotion,
  compactMode,
  setCompactMode,
  clock24h,
  setClock24h,
  defaultTab,
  setDefaultTab,
  confirmStop,
  setConfirmStop,
  onResetStats,
  appVersion,
  updateStatus,
  onCheckUpdates,
  onClearSkip,
  onOpenUpdateModal,
  onOpenSetupWizard,
  isBotRunning,
}) => {
  // Destructive action protection: first click arms, second click within 4s
  // fires. Prevents fat-finger stat wipes.
  const [resetArmed, setResetArmed] = React.useState(false);
  const [prefsArmed, setPrefsArmed] = React.useState(false);
  const [exported, setExported] = React.useState(false);
  const [testBusy, setTestBusy] = React.useState('');
  const [testNote, setTestNote] = React.useState('');
  const resetTimerRef = React.useRef<number | null>(null);
  const prefsTimerRef = React.useRef<number | null>(null);
  const exportTimerRef = React.useRef<number | null>(null);

  React.useEffect(() => () => {
    if (resetTimerRef.current) window.clearTimeout(resetTimerRef.current);
    if (prefsTimerRef.current) window.clearTimeout(prefsTimerRef.current);
    if (exportTimerRef.current) window.clearTimeout(exportTimerRef.current);
  }, []);

  const handleResetClick = () => {
    if (!resetArmed) {
      setResetArmed(true);
      resetTimerRef.current = window.setTimeout(() => setResetArmed(false), 4000);
      return;
    }
    if (resetTimerRef.current) window.clearTimeout(resetTimerRef.current);
    setResetArmed(false);
    onResetStats();
  };

  const handleResetPreferences = () => {
    if (!prefsArmed) {
      setPrefsArmed(true);
      prefsTimerRef.current = window.setTimeout(() => setPrefsArmed(false), 4000);
      return;
    }
    if (prefsTimerRef.current) window.clearTimeout(prefsTimerRef.current);
    ['darkMode', 'sidebarExpanded', 'reduceMotion', 'compactMode', 'clock24h', 'confirmStop', 'defaultTab']
      .forEach((key) => localStorage.removeItem(key));
    window.location.reload();
  };

  const runDeviceTest = async (action: 'open' | 'filters' | 'close', label: string) => {
    setTestBusy(action);
    setTestNote(`${label}…`);
    try {
      await TestDeviceMenu(action);
      setTestNote(`${label} sent.`);
    } catch (err) {
      setTestNote(err instanceof Error ? err.message : String(err));
    } finally {
      setTestBusy('');
    }
  };

  const handleExportDiagnostics = () => {
    const generatedAt = new Date();
    const payload = {
      generated_at: generatedAt.toISOString(),
      app_version: appVersion || '0.0.0',
      build_channel: appVersion.includes('-dev') ? 'development' : 'release',
      platform: navigator.platform || 'unknown',
      adb: {
        port: adbPort,
        consecutive_fails: stats.adb_health.consecutive_fails,
        captures_total: stats.adb_health.captures_total,
        errors_total: stats.adb_health.errors_total,
        avg_capture_ms: stats.adb_health.avg_capture_ms,
        last_error: stats.adb_health.last_error || '',
      },
      stats: {
        attacks_completed: stats.attacks_completed,
        search_skips: stats.search_skips,
        total_gold: stats.total_gold,
        total_elixir: stats.total_elixir,
        total_de: stats.total_de,
        stars_0: stats.stars_0,
        stars_1: stats.stars_1,
        stars_2: stats.stars_2,
        stars_3: stats.stars_3,
        uptime_ns: stats.uptime,
        cpu_time_sec: stats.cpu_time_sec,
        cpu_cores: stats.cpu_cores,
      },
      updater: {
        state: updateStatus.state,
        available: updateStatus.available,
        latest_version: updateStatus.latest_version,
        skip_version: updateStatus.skip_version,
      },
      preferences: {
        dark_mode: darkMode,
        reduce_motion: reduceMotion,
        compact_mode: compactMode,
        clock_24h: clock24h,
        default_tab: defaultTab,
        confirm_stop: confirmStop,
      },
    };

    const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = `clashgo-diagnostics-${generatedAt.toISOString().slice(0, 10)}.json`;
    document.body.appendChild(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);

    setExported(true);
    if (exportTimerRef.current) window.clearTimeout(exportTimerRef.current);
    exportTimerRef.current = window.setTimeout(() => setExported(false), 2200);
  };

  const connectionOk = stats.adb_health.consecutive_fails === 0;
  const latency = Number.isFinite(stats.adb_health.avg_capture_ms)
    ? `${stats.adb_health.avg_capture_ms.toFixed(1)} ms`
    : '0 ms';
  const cpuUsage = Number.isFinite(stats.cpu_cores)
    ? `${(stats.cpu_cores * (navigator.hardwareConcurrency || 1) * 100).toFixed(1)}%`
    : '0%';
  const cpuTime = Number.isFinite(stats.cpu_time_sec)
    ? `${stats.cpu_time_sec.toFixed(1)}s`
    : '—';
  const updateReady = updateStatus.available || updateStatus.state === 'ready';

  return (
    <div>
      <div className="page-head">
        <div>
          <h1>Settings</h1>
          <p>Preferences, connection health, diagnostics, and application updates.</p>
        </div>
        <div className="page-side">
          <span className="tag">v{appVersion || '0.0.0'}</span>
        </div>
      </div>

      <div className="settings-grid">
        <section className="card settings-appearance">
          <div className="card-head">
            <div>
              <h3>Appearance</h3>
              <p className="card-sub">Display preferences persist on this machine</p>
            </div>
          </div>
          <div className="card-body">
            <SwitchRow
              checked={darkMode}
              onChange={setDarkMode}
              label="Dark theme"
              description="Command surfaces stay dark; switch off for the light palette."
            />
            <SwitchRow
              checked={compactMode}
              onChange={setCompactMode}
              label="Compact density"
              description="Tighter page, card, and settings spacing for smaller screens."
            />
            <SwitchRow
              checked={reduceMotion}
              onChange={setReduceMotion}
              label="Reduce motion"
              description="Stops decorative animation and shortens interface transitions."
            />
            <SwitchRow
              checked={clock24h}
              onChange={setClock24h}
              label="24-hour clock"
              description="Use 24-hour time in the top bar instead of AM/PM."
            />
          </div>
        </section>

        <section className="card settings-behavior">
          <div className="card-head">
            <div>
              <h3>Behavior</h3>
              <p className="card-sub">Launch and engine safety preferences</p>
            </div>
          </div>
          <div className="card-body">
            <div className="setting-row">
              <div className="setting-label">
                <b>Default page</b>
                <span>Opened when ClashGO starts</span>
              </div>
              <div className="setting-value">
                <select
                  className="field setting-select"
                  value={defaultTab}
                  onChange={(event) => setDefaultTab(event.target.value as TabType)}
                  aria-label="Default page"
                >
                  {DEFAULT_PAGE_OPTIONS.map((option) => (
                    <option key={option.value} value={option.value}>{option.label}</option>
                  ))}
                </select>
              </div>
            </div>
            <SwitchRow
              checked={confirmStop}
              onChange={setConfirmStop}
              label="Confirm before stopping"
              description="Ask before stopping a running bot from any page."
            />
          </div>
        </section>

        <section className="card settings-system">
          <div className="card-head">
            <div>
              <h3>System</h3>
              <p className="card-sub">Connection and runtime diagnostics</p>
            </div>
          </div>
          <div className="card-body">
            <div className="setting-row">
              <div className="setting-label"><b>Connection</b><span>ADB bridge state</span></div>
              <div className="setting-value">
                <span className={`status-pill ${connectionOk ? 'green' : 'error'}`}>
                  <i />{connectionOk ? 'Optimal' : 'Interrupted'}
                </span>
              </div>
            </div>
            <div className="setting-row">
              <div className="setting-label"><b>ADB port</b><span>Local ADB server listen port</span></div>
              <div className="setting-value"><b>{adbPort}</b></div>
            </div>
            <div className="setting-row">
              <div className="setting-label"><b>Capture latency</b><span>Rolling average per frame</span></div>
              <div className="setting-value">
                <b>{latency}</b>
                <span className="status-pill" style={{ marginTop: 4, display: 'inline-flex' }}>
                  <i />{stats.adb_health.avg_capture_ms < 200 ? 'Healthy' : 'Slow'}
                </span>
              </div>
            </div>
            <div className="setting-row">
              <div className="setting-label"><b>CPU time</b><span>Process CPU seconds since start</span></div>
              <div className="setting-value"><b>{cpuTime}</b></div>
            </div>
            <div className="setting-row">
              <div className="setting-label"><b>CPU usage</b><span>Across all logical cores</span></div>
              <div className="setting-value"><b>{cpuUsage}</b></div>
            </div>
            {stats.adb_health.last_error ? (
              <div className="error-banner" style={{ marginTop: 12 }}>
                <Icon name="alert" size={14} />
                <span className="mono" style={{ fontSize: 10.5, wordBreak: 'break-word' }}>
                  {stats.adb_health.last_error}
                </span>
              </div>
            ) : null}
            <div className="settings-actions">
              <button type="button" className="btn sm" onClick={handleExportDiagnostics}>
                <Icon name={exported ? 'check' : 'download'} size={12} />
                {exported ? 'Exported' : 'Export diagnostics'}
              </button>
              <span className="muted">JSON · no account credentials</span>
            </div>
          </div>
        </section>

        <section className="card settings-updates">
          <div className="card-head">
            <div>
              <h3>Updates</h3>
              <p className="card-sub">Signed in-app updates from GitHub releases</p>
            </div>
          </div>
          <div className="card-body">
            <div className="setting-row">
              <div className="setting-label">
                <b>App version</b>
                <span>
                  {updateStatus.available
                    ? `v${updateStatus.latest_version} available`
                    : updateStatus.state === 'ready'
                      ? `v${updateStatus.latest_version} ready to install`
                      : 'You are on the installed build'}
                </span>
              </div>
              <div className="setting-value">
                <b>v{appVersion || '0.0.0'}</b>
              </div>
            </div>
            <div className="setting-row">
              <div className="setting-label"><b>Release channel</b><span>Manual checks plus a 6h background poll</span></div>
              <div className="setting-value">
                <button type="button" className="btn sm" onClick={onCheckUpdates}>
                  <Icon name="refresh" size={12} />
                  Check now
                </button>
              </div>
            </div>
            {updateReady && onOpenUpdateModal ? (
              <div className="setting-row">
                <div className="setting-label">
                  <b>Install update</b>
                  <span>Downloads, verifies, swaps the bundle, and restarts.</span>
                </div>
                <div className="setting-value">
                  <button type="button" className="btn primary sm" onClick={onOpenUpdateModal}>
                    Review update
                  </button>
                </div>
              </div>
            ) : null}
            {updateStatus.skip_version ? (
              <div className="setting-row">
                <div className="setting-label">
                  <b>Muted version</b>
                  <span>v{updateStatus.skip_version} is skipped</span>
                </div>
                <div className="setting-value">
                  <button type="button" className="btn sm" onClick={onClearSkip}>
                    Resume
                  </button>
                </div>
              </div>
            ) : null}
          </div>
        </section>

        <section className="card settings-testing">
          <div className="card-head">
            <div>
              <h3>Testing</h3>
              <p className="card-sub">Pop up the in-game build menu without starting the bot</p>
            </div>
          </div>
          <div className="card-body">
            <p className="hint">
              Sit in the main village first. These only tap navigation — no upgrade is confirmed or bought.
            </p>
            <div className="settings-actions">
              <button
                type="button"
                className="btn sm"
                onClick={onOpenSetupWizard}
                disabled={isBotRunning}
              >
                <Icon name="sliders" size={12} />
                Open setup wizard
              </button>
              <button
                type="button"
                className="btn sm"
                onClick={() => void runDeviceTest('open', 'Opening builder menu')}
                disabled={isBotRunning || testBusy !== ''}
              >
                <Icon name="device" size={12} />
                {testBusy === 'open' ? 'Opening…' : 'Open builder menu'}
              </button>
              <button
                type="button"
                className="btn sm"
                onClick={() => void runDeviceTest('filters', 'Opening build filters')}
                disabled={isBotRunning || testBusy !== ''}
              >
                <Icon name="sliders" size={12} />
                {testBusy === 'filters' ? 'Opening…' : 'Open filters'}
              </button>
              <button
                type="button"
                className="btn sm ghost"
                onClick={() => void runDeviceTest('close', 'Closing builder menu')}
                disabled={isBotRunning || testBusy !== ''}
              >
                Close menu
              </button>
            </div>
            {isBotRunning ? (
              <p className="hint">Stop the bot before using test tools.</p>
            ) : testNote ? (
              <p className="hint" aria-live="polite">{testNote}</p>
            ) : null}
          </div>
        </section>

        <section className="card danger-card settings-danger">
          <div className="card-head">
            <div>
              <h3>Danger zone</h3>
              <p className="card-sub">Irreversible actions for this profile</p>
            </div>
          </div>
          <div className="card-body">
            <button
              type="button"
              onClick={handleResetClick}
              aria-live="polite"
              className={`reset-btn ${resetArmed ? 'armed' : ''}`.trim()}
            >
              <Icon name={resetArmed ? 'alert' : 'trash'} size={16} />
              <span>
                <b>{resetArmed ? 'Click again to confirm' : 'Reset all statistics'}</b>
                <span>{resetArmed ? 'This wipes attacks, loot, stars, search skips, and uptime.' : 'Clears attack, loot, star, search, and uptime totals.'}</span>
              </span>
            </button>
            <button
              type="button"
              onClick={handleResetPreferences}
              aria-live="polite"
              className={`reset-btn secondary ${prefsArmed ? 'armed' : ''}`.trim()}
            >
              <Icon name={prefsArmed ? 'alert' : 'refresh'} size={16} />
              <span>
                <b>{prefsArmed ? 'Click again to confirm' : 'Reset interface preferences'}</b>
                <span>{prefsArmed ? 'Theme, density, motion, clock, launch page, and stop confirmation.' : 'Restores defaults without touching stats, config, or accounts.'}</span>
              </span>
            </button>
          </div>
        </section>
      </div>
    </div>
  );
});

SettingsView.displayName = 'SettingsView';

export default SettingsView;
