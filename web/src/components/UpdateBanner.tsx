import React from 'react';
import { UpdateStatus } from '../types';

// UpdateBanner is the user-facing surface for the in-app updater.
//
// Design intent (v2):
//   - One primary CTA — "Update & Restart" — drives the entire flow
//     (download → SHA verify → helper swap → relaunch).
//   - "Manual install in Finder" stays as a tertiary escape hatch.
//   - The restarting state renders a non-dismissible splash so the
//     user doesn't think the app froze during the ~1-3s helper wait.
//   - All transitions animate; hover/focus have micro-interactions.
//
// State source of truth: the Wails `updater_status` event. We do NOT
// poll GetUpdateStatus from this component — push is cheaper and
// avoids the lock churn.

interface UpdateBannerProps {
  status: UpdateStatus | null;
  appVersion: string;
  isBotRunning: boolean;
  onCheckNow: () => void;
  onDownload: () => Promise<string>;
  onApply: () => Promise<void>;
  onUpdateAndRestart: () => Promise<void>;
  onSkip: () => Promise<void>;
  onClearSkip: () => Promise<void>;
  onDismiss: () => void;
}

const formatBytes = (n: number): string => {
  if (!n || n <= 0) return '';
  const units = ['B', 'KB', 'MB', 'GB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v >= 10 ? 0 : 1)} ${units[i]}`;
};

const stateLabel = (state: string): string => {
  switch (state) {
    case 'idle':
      return 'Up to date';
    case 'checking':
      return 'Checking for updates…';
    case 'available':
      return 'Update available';
    case 'downloading':
      return 'Downloading…';
    case 'ready':
      return 'Ready to install';
    case 'restarting':
      return 'Restarting…';
    case 'error':
      return 'Update error';
    case 'up_to_date':
      return 'Up to date';
    default:
      return state;
  }
};

const stateIcon = (state: string): string => {
  switch (state) {
    case 'error':
      return 'error';
    case 'downloading':
      return 'downloading';
    case 'restarting':
      return 'autorenew';
    case 'ready':
      return 'check_circle';
    default:
      return 'system_update';
  }
};

const UpdateBanner: React.FC<UpdateBannerProps> = ({
  status,
  appVersion,
  isBotRunning,
  onCheckNow,
  onDownload,
  onApply,
  onUpdateAndRestart,
  onSkip,
  onClearSkip,
  onDismiss,
}) => {
  const [open, setOpen] = React.useState(false);
  const [busy, setBusy] = React.useState<string | null>(null);
  const [lastError, setLastError] = React.useState<string | null>(null);
  const [mountedChecksumFlash, setMountedChecksumFlash] = React.useState(false);
  const closeButtonRef = React.useRef<HTMLButtonElement>(null);
  const previousFocusRef = React.useRef<HTMLElement | null>(null);

  // Standard modal a11y: capture the previously-focused element so we
  // can restore it on close, focus the close button on mount, and let
  // ESC dismiss. Listener is now conditionally mounted — when the
  // modal isn't open we don't register a global keydown handler at
  // all, instead of mounting it then short-circuiting on `!open`
  // (the prior pattern fired on every keystroke globally even with
  // no modal up).
  const wasOpenRef = React.useRef(false);
  React.useEffect(() => {
    if (!open) {
      wasOpenRef.current = false;
      return;
    }
    if (!wasOpenRef.current) {
      previousFocusRef.current = (document.activeElement as HTMLElement) ?? null;
      closeButtonRef.current?.focus();
      wasOpenRef.current = true;
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && busy === null) {
        setOpen(false);
        onDismiss();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      if (!wasOpenRef.current) previousFocusRef.current?.focus?.();
    };
  }, [open, busy, onDismiss]);

  // Brief success flash when SHA256 verification just completed.
  // HOOK-POSITION-SENSITIVE: this `useEffect` MUST run before every
  // early-return so React sees the same hook count on every render.
  // The previous layout placed it AFTER `if (!status) return null`,
  // `if (status.state === 'restarting') return ...`, and
  // `if (!visible) return null`, so its hook index varied across
  // renders and React threw "Rendered more hooks than during the
  // previous render", unmounting the tree and producing a blank
  // WebView. The body now guards against null/falsy `status` instead.
  React.useEffect(() => {
    if (!status || status.state !== 'ready') return;
    setMountedChecksumFlash(true);
    const t = setTimeout(() => setMountedChecksumFlash(false), 1800);
    return () => clearTimeout(t);
  }, [status?.state]);

  if (!status) return null;

  // Restoring state renders a full-screen splash instead of the modal/pill.
  if (status.state === 'restarting') {
    return <RestartSplash status={status} />;
  }

  // The pill should stay visible across the entire update journey —
  // including `downloading` (so the user sees progress in the header
  // even if they closed the modal) and not just the explicit
  // "available" / "ready" / "error" snapshots.
  const visible =
    status.available ||
    status.state === 'error' ||
    status.state === 'ready' ||
    status.state === 'downloading';
  if (!visible) return null;

  const progressPct = Math.round((status.progress || 0) * 100);
  const showSkipped =
    status.skip_version && status.skip_version === status.latest_version;
  const canOneClick =
    (status.available || status.state === 'ready') && status.state !== 'downloading';

  const guardedAction = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key);
    setLastError(null);
    try {
      await fn();
    } catch (e) {
      setLastError(String(e));
    } finally {
      setBusy(null);
    }
  };

  const doOneClick = () => guardedAction('oneclick', onUpdateAndRestart);
  const doDownload = () => guardedAction('download', onDownload);
  const doApply = () => guardedAction('apply', onApply);
  const doSkip = async () => {
    await guardedAction('skip', onSkip);
    onDismiss();
    setOpen(false);
  };
  const doClearSkip = () => guardedAction('clear', onClearSkip);
  const doCheck = () => guardedAction('check', async () => { await onCheckNow(); });

  const pillTone =
    status.state === 'error'
      ? 'bg-rose-500/10 border-rose-500/40 text-rose-500'
      : status.state === 'ready'
        ? 'bg-emerald-500/15 border-emerald-500/40 text-emerald-500 shadow-[0_0_24px_-8px_rgba(16,185,129,0.5)]'
        : 'bg-emerald-500/10 border-emerald-500/30 text-emerald-500';

  return (
    <>
      <button
        onClick={() => setOpen(true)}
        className={`no-drag group flex items-center gap-2.5 px-4 py-2 rounded-2xl border text-[10px] font-black uppercase tracking-[0.2em] transition-all duration-300 hover:shadow-premium active:scale-95 ${pillTone}`}
        title={`ClashGO v${appVersion || '0.0.0'} — ${stateLabel(status.state)}`}
      >
        <span
          className={`material-symbols-outlined text-sm ${
            status.state === 'downloading' ? 'animate-pulse' : ''
          }`}
        >
          {stateIcon(status.state)}
        </span>
        {status.state === 'downloading'
          ? `Downloading ${progressPct}%`
          : status.available
            ? `Update ${status.latest_version}`
            : status.state === 'ready'
              ? `v${status.latest_version} — Install`
              : status.state === 'error'
                ? 'Update failed'
                : stateLabel(status.state)}
        {status.available && (
          <span className="w-1 h-1 rounded-full bg-emerald-500 animate-pulse" />
        )}
      </button>

      {open && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center p-6 bg-zinc-950/40 backdrop-blur-md animate-fadeIn"
          onClick={() => setOpen(false)}
          role="presentation"
        >
          <div
            role="dialog"
            aria-modal="true"
            aria-labelledby="update-banner-title"
            className="bg-white dark:bg-zinc-900 rounded-[2rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium-lg max-w-md w-full overflow-hidden animate-scaleIn"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="px-7 pt-7 pb-5 border-b border-zinc-50 dark:border-zinc-800/50">
              <div className="flex items-start justify-between gap-4">
                <div className="flex-1 min-w-0">
                  <div className="text-[10px] font-black text-emerald-500 uppercase tracking-[0.35em] mb-1.5 flex items-center gap-2">
                    <span className="material-symbols-outlined text-xs">rocket_launch</span>
                    ClashGO Update
                  </div>
                  <h2 id="update-banner-title" className="font-headline text-[1.7rem] font-bold tracking-tight text-zinc-950 dark:text-white leading-tight">
                    {status.available
                      ? `${status.latest_version} is here`
                      : stateLabel(status.state)}
                  </h2>
                  <p className="text-[11px] text-zinc-400 dark:text-zinc-500 mt-1.5 font-bold tabular-nums flex items-center gap-2">
                    <span className="text-zinc-400 dark:text-zinc-600 uppercase tracking-widest text-[10px]">Current</span>
                    <span className="font-mono">v{appVersion}</span>
                    {status.min_supported && (
                      <>
                        <span className="text-zinc-300 dark:text-zinc-700">•</span>
                        <span className="text-[10px] uppercase tracking-widest text-zinc-300 dark:text-zinc-700">
                          min v{status.min_supported}
                        </span>
                      </>
                    )}
                  </p>
                </div>
                <button
                  ref={closeButtonRef}
                  onClick={() => {
                    setOpen(false);
                    onDismiss();
                  }}
                  className="w-9 h-9 rounded-xl bg-zinc-50 dark:bg-zinc-800 hover:bg-zinc-100 dark:hover:bg-zinc-700 flex items-center justify-center text-zinc-500 dark:text-zinc-400 transition-colors"
                  aria-label="Close update dialog"
                >
                  <span className="material-symbols-outlined text-base">close</span>
                </button>
              </div>
            </div>

            <div className="px-7 py-5 max-h-72 overflow-y-auto">
              {status.state === 'downloading' ? (
                <DownloadBody
                  status={status}
                  progressPct={progressPct}
                  formatBytes={formatBytes}
                />
              ) : status.state === 'ready' ? (
                <ReadyBody status={status} formatBytes={formatBytes} flash={mountedChecksumFlash} />
              ) : status.state === 'error' ? (
                <ErrorBody status={status} lastError={lastError} />
              ) : status.notes ? (
                <pre className="text-xs text-zinc-600 dark:text-zinc-300 whitespace-pre-wrap font-mono leading-relaxed">
                  {status.notes}
                </pre>
              ) : (
                <div className="text-sm text-zinc-500 dark:text-zinc-400">
                  No release notes were published for this version.
                </div>
              )}

              {lastError && status.state !== 'error' && (
                <div className="mt-3 text-xs text-rose-500/80 font-mono break-all">
                  {lastError}
                </div>
              )}

              {isBotRunning && canOneClick && (
                <div className="mt-4 px-3 py-2 rounded-xl bg-amber-500/10 border border-amber-500/30 text-[11px] font-bold text-amber-600 dark:text-amber-400 tracking-wide leading-relaxed">
                  <span className="material-symbols-outlined text-xs align-middle mr-1">info</span>
                  The bot is currently running. Updating will stop the bot
                  cleanly, drain ADB, and relaunch with the new version.
                </div>
              )}
            </div>

            <div className="px-7 pb-7 pt-2 flex flex-col gap-3">
              {/* PRIMARY CTA — one click does it all. */}
              <button
                onClick={doOneClick}
                disabled={busy !== null || !canOneClick}
                className="group/cta relative h-12 w-full overflow-hidden rounded-2xl bg-zinc-950 dark:bg-white text-white dark:text-zinc-950 font-black text-[11px] uppercase tracking-[0.3em] transition-all duration-300 hover:shadow-premium-lg active:scale-[0.98] disabled:opacity-30 disabled:cursor-not-allowed flex items-center justify-center gap-2"
              >
                <span className="absolute inset-0 bg-gradient-to-r from-emerald-400 via-emerald-500 to-emerald-600 opacity-0 group-hover/cta:opacity-100 transition-opacity duration-500" />
                <span className="relative flex items-center gap-2 group-hover/cta:text-zinc-950 transition-colors duration-500">
                  <span className="material-symbols-outlined text-base">
                    {busy === 'oneclick' ? 'progress_activity' : 'rocket_launch'}
                  </span>
                  {busy === 'oneclick'
                    ? status.state === 'ready'
                      ? 'Installing…'
                      : 'Downloading & installing…'
                    : status.state === 'ready'
                      ? 'Install & Restart'
                      : `Update to v${status.latest_version}`}
                </span>
              </button>

              {/* Tertiary actions: manual install / download only. */}
              <div className="grid grid-cols-2 gap-2">
                <button
                  onClick={() => doDownload()}
                  disabled={busy !== null || status.state === 'downloading' || !status.available}
                  className="h-10 rounded-xl bg-zinc-50 dark:bg-zinc-800/50 hover:bg-zinc-100 dark:hover:bg-zinc-800 disabled:opacity-40 text-zinc-700 dark:text-zinc-300 text-[10px] font-black uppercase tracking-[0.2em] transition-colors flex items-center justify-center gap-1.5"
                >
                  <span className="material-symbols-outlined text-[14px]">download</span>
                  {status.state === 'ready' ? 'Re-download' : 'Download only'}
                </button>
                <button
                  onClick={() => doApply()}
                  disabled={busy !== null || status.state !== 'ready'}
                  className="h-10 rounded-xl bg-zinc-50 dark:bg-zinc-800/50 hover:bg-zinc-100 dark:hover:bg-zinc-800 disabled:opacity-40 text-zinc-700 dark:text-zinc-300 text-[10px] font-black uppercase tracking-[0.2em] transition-colors flex items-center justify-center gap-1.5"
                >
                  <span className="material-symbols-outlined text-[14px]">folder_open</span>
                  Open in Finder
                </button>
              </div>

              {/* Quiet row: re-check, skip, dismiss. */}
              <div className="flex items-center justify-between pt-1">
                <button
                  onClick={() => doCheck()}
                  disabled={busy !== null || status.state === 'checking'}
                  className="text-[10px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em] hover:text-zinc-700 dark:hover:text-zinc-300 transition-colors flex items-center gap-1.5 disabled:opacity-30"
                >
                  <span
                    className={`material-symbols-outlined text-[14px] ${
                      busy === 'check' ? 'animate-spin' : ''
                    }`}
                  >
                    {busy === 'check' ? 'progress_activity' : 'refresh'}
                  </span>
                  Re-check
                </button>
                <div className="flex items-center gap-3">
                  <button
                    onClick={() => doSkip()}
                    disabled={busy !== null || !status.available}
                    className="text-[10px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em] hover:text-zinc-700 dark:hover:text-zinc-300 transition-colors disabled:opacity-30"
                  >
                    Skip version
                  </button>
                  <span className="text-zinc-200 dark:text-zinc-800">•</span>
                  <button
                    onClick={() => {
                      setOpen(false);
                      onDismiss();
                    }}
                    className="text-[10px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em] hover:text-zinc-700 dark:hover:text-zinc-300 transition-colors"
                  >
                    Later
                  </button>
                </div>
              </div>

              {showSkipped && (
                <button
                  onClick={() => doClearSkip()}
                  disabled={busy !== null}
                  className="text-[10px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em] hover:text-zinc-700 dark:hover:text-zinc-300 transition-colors mt-1 text-center w-full"
                >
                  Resume notifications for v{status.skip_version}
                </button>
              )}

              {/* Status chip: surfaces WHICH action is in-flight so a slow
                  user with multiple clicks sees what's happening. */}
              {busy !== null && (
                <div className="flex items-center gap-2 text-[10px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-[0.2em] pt-1">
                  <span className="material-symbols-outlined text-[14px] animate-spin">
                    progress_activity
                  </span>
                  {busyLabel(busy)}
                </div>
              )}
            </div>
          </div>
        </div>
      )}

      <style>{`
        @keyframes fade-in { from { opacity: 0 } to { opacity: 1 } }
        @keyframes scale-in {
          from { opacity: 0; transform: scale(0.94) translateY(8px) }
          to   { opacity: 1; transform: scale(1) translateY(0) }
        }
        /* Use kebab-case class names so we don't collide with React's
           reserved 'data-' / kebab collision surface; the strings below
           are referenced by animate-{name} utilities above. */
        .animate-fadeIn { animation: fade-in 200ms ease-out }
        .animate-scaleIn { animation: scale-in 180ms cubic-bezier(0.34, 1.56, 0.64, 1) }
      `}</style>
    </>
  );
};

const busyLabel = (b: string): string => {
  switch (b) {
    case 'oneclick':
      return 'Downloading • verifying • installing…';
    case 'download':
      return 'Downloading…';
    case 'apply':
      return 'Opening Finder…';
    case 'check':
      return 'Checking release feed…';
    case 'skip':
      return 'Saving preference…';
    case 'clear':
      return 'Clearing preference…';
    default:
      return 'Working…';
  }
};

const DownloadBody: React.FC<{
  status: UpdateStatus;
  progressPct: number;
  formatBytes: (n: number) => string;
}> = ({ status, progressPct, formatBytes }) => (
  <div className="space-y-3">
    <div className="text-sm text-zinc-600 dark:text-zinc-300">
      Downloading{' '}
      <span className="font-mono font-bold text-zinc-950 dark:text-white">
        {status.asset_name}
      </span>
      {status.expected_size > 0 ? (
        <>
          {' '}
          <span className="text-[10px] font-black uppercase tracking-widest text-zinc-400 dark:text-zinc-500 tabular-nums">
            ({formatBytes(status.expected_size)})
          </span>
        </>
      ) : null}
      …
    </div>
    <div className="relative h-2 rounded-full bg-zinc-100 dark:bg-zinc-800 overflow-hidden">
      <div
        className="absolute inset-y-0 left-0 bg-gradient-to-r from-emerald-400 via-emerald-500 to-emerald-600 rounded-full transition-[width] duration-300 ease-out"
        style={{ width: `${progressPct}%` }}
      />
      <div
        className="absolute inset-y-0 left-0 w-1/3 bg-gradient-to-r from-transparent via-white/40 to-transparent animate-[shimmer_1.5s_infinite] rounded-full"
        style={{ transform: `translateX(${progressPct * 3}%)` }}
      />
    </div>
    <div className="flex items-center justify-between text-[10px] font-black text-zinc-400 dark:text-zinc-500 uppercase tracking-widest tabular-nums">
      <span>{progressPct}% downloaded</span>
      <span className="flex items-center gap-1.5">
        <span className="material-symbols-outlined text-[12px]">verified_user</span>
        SHA256 on completion
      </span>
    </div>
  </div>
);

const ReadyBody: React.FC<{
  status: UpdateStatus;
  formatBytes: (n: number) => string;
  flash: boolean;
}> = ({ status, flash }) => (
  <div className="space-y-3">
    <div
      className={`flex items-center gap-2.5 px-3 py-2 rounded-xl border transition-all duration-500 ${
        flash
          ? 'bg-emerald-500/15 border-emerald-500/40 shadow-[0_0_24px_-8px_rgba(16,185,129,0.5)]'
          : 'bg-zinc-50 dark:bg-zinc-800/40 border-zinc-100 dark:border-zinc-800'
      }`}
    >
      <span className="material-symbols-outlined text-emerald-500 text-base">
        verified_user
      </span>
      <span className="text-xs font-bold text-zinc-950 dark:text-white">
        Verified. Ready to install.
      </span>
    </div>
    {status.download_path && (
      <div className="text-[10px] font-mono text-zinc-400 dark:text-zinc-500 break-all px-1">
        {status.download_path}
      </div>
    )}
    <p className="text-[11px] text-zinc-500 dark:text-zinc-400 leading-relaxed">
      Click <strong className="text-zinc-950 dark:text-white">Install &amp; Restart</strong>{' '}
      to apply now. The helper script will swap the running bundle
      and relaunch. ClashGO will exit briefly during the swap.
    </p>
    <style>{`@keyframes shimmer { from { transform: translateX(-100%) } to { transform: translateX(300%) } }`}</style>
  </div>
);

const ErrorBody: React.FC<{ status: UpdateStatus; lastError: string | null }> = ({
  status,
  lastError,
}) => (
  <div className="space-y-2">
    <div className="flex items-center gap-2.5 px-3 py-2 rounded-xl bg-rose-500/10 border border-rose-500/30">
      <span className="material-symbols-outlined text-rose-500 text-base">error</span>
      <span className="text-xs font-bold text-rose-600 dark:text-rose-400">
        Update check did not complete.
      </span>
    </div>
    {status.error && (
      <div className="text-xs text-zinc-500 dark:text-zinc-400 font-mono break-all leading-relaxed">
        {status.error}
      </div>
    )}
    {lastError && (
      <div className="text-xs text-rose-500/80 font-mono break-all mt-2">{lastError}</div>
    )}
  </div>
);

// RestartSplash is rendered instead of the modal/pill when the user
// pressed Install & Restart. It covers the screen for the brief
// window between the helper reading -> running and the new bundle
// appearing in the dock; non-dismissible on purpose.
const RestartSplash: React.FC<{ status: UpdateStatus }> = ({ status }) => (
  <div className="fixed inset-0 z-[100] flex items-center justify-center bg-zinc-950/95 backdrop-blur-2xl animate-[fade-in_300ms_ease-out]">
    <div className="text-center space-y-7 max-w-sm px-6">
      <div className="relative inline-flex items-center justify-center w-24 h-24 mx-auto">
        <div className="absolute inset-0 rounded-full border-2 border-emerald-500/30 animate-[ping_2s_infinite]" />
        <div className="absolute inset-2 rounded-full border-2 border-emerald-500/50 animate-[pulse_1.4s_infinite]" />
        <div className="relative w-16 h-16 rounded-full bg-gradient-to-br from-emerald-400 to-emerald-600 flex items-center justify-center shadow-[0_0_40px_-8px_rgba(16,185,129,0.6)]">
          <span className="material-symbols-outlined text-white text-3xl animate-[spin_2.5s_linear_infinite]">
            autorenew
          </span>
        </div>
      </div>
      <div className="space-y-2.5">
        <h3 className="font-headline text-2xl font-bold tracking-tight text-white">
          Restarting ClashGO
        </h3>
        <p className="text-sm text-zinc-400 leading-relaxed">
          Installing{' '}
          <span className="font-mono font-bold text-emerald-400">v{status.latest_version}</span>
          . The new build will appear in your dock in a moment.
        </p>
      </div>        <div className="text-[10px] font-black text-zinc-600 uppercase tracking-[0.4em]">
          don&rsquo;t quit this window
        </div>
    </div>
  </div>
);

export default UpdateBanner;
