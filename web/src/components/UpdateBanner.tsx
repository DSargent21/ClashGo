import React from 'react';
import { createPortal } from 'react-dom';
import { UpdateStatus } from '../types';
import Icon, { IconName } from './ui/Icon';

// UpdateBanner is the user-facing surface for the in-app updater.
//
// One primary CTA drives download → verify → swap → relaunch. The pill
// stays in the top bar for the whole journey; the modal carries release
// notes, progress, and the quieter escape hatches. A non-dismissible
// splash covers the helper swap so the app never looks frozen.

interface UpdateBannerProps {
  status: UpdateStatus | null;
  appVersion: string;
  isBotRunning: boolean;
  isOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
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
  let index = 0;
  let value = n;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index += 1;
  }
  return `${value.toFixed(value >= 10 ? 0 : 1)} ${units[index]}`;
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

const stateIcon = (state: string): IconName => {
  switch (state) {
    case 'error':
      return 'alert';
    case 'downloading':
      return 'download';
    case 'restarting':
      return 'refresh';
    case 'ready':
      return 'check';
    default:
      return 'download';
  }
};

const busyLabel = (busy: string): string => {
  switch (busy) {
    case 'oneclick':
      return 'Downloading · verifying · installing…';
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

const UpdateBanner: React.FC<UpdateBannerProps> = ({
  status,
  appVersion,
  isBotRunning,
  isOpen,
  onOpenChange,
  onCheckNow,
  onDownload,
  onApply,
  onUpdateAndRestart,
  onSkip,
  onClearSkip,
  onDismiss,
}) => {
  const [internalOpen, setInternalOpen] = React.useState(false);
  const open = isOpen !== undefined ? isOpen : internalOpen;
  const setOpen = React.useCallback((value: boolean) => {
    setInternalOpen(value);
    onOpenChange?.(value);
  }, [onOpenChange]);

  const [busy, setBusy] = React.useState<string | null>(null);
  const [lastError, setLastError] = React.useState<string | null>(null);
  const [mountedChecksumFlash, setMountedChecksumFlash] = React.useState(false);
  const closeButtonRef = React.useRef<HTMLButtonElement>(null);
  const busyRef = React.useRef(busy);
  const dismissRef = React.useRef(onDismiss);
  busyRef.current = busy;
  dismissRef.current = onDismiss;

  // ESC + focus restore. Refs keep the listener stable while busy/onDismiss
  // change, so focus is only restored when the dialog truly closes.
  React.useEffect(() => {
    if (!open) return;
    const previousFocus = document.activeElement as HTMLElement | null;
    closeButtonRef.current?.focus();
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && busyRef.current === null) {
        setOpen(false);
        dismissRef.current();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      previousFocus?.focus?.();
    };
  }, [open, setOpen]);

  // Brief success flash when SHA-256 verification just completed.
  React.useEffect(() => {
    if (!status || status.state !== 'ready') return;
    setMountedChecksumFlash(true);
    const timer = setTimeout(() => setMountedChecksumFlash(false), 1800);
    return () => clearTimeout(timer);
  }, [status?.state]);

  // Auto-open once per available version. The 2s status ticker must not
  // reopen a dialog the user already dismissed.
  const autoPoppedVersion = React.useRef<string | null>(null);
  React.useEffect(() => {
    if (!status || !status.available) return;
    if (autoPoppedVersion.current === status.latest_version) return;
    autoPoppedVersion.current = status.latest_version;
    const timer = setTimeout(() => setOpen(true), 300);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status?.available, status?.latest_version]);

  if (!status) return null;
  if (status.state === 'restarting') return <RestartSplash status={status} />;

  const visible =
    status.available ||
    status.state === 'error' ||
    status.state === 'ready' ||
    status.state === 'downloading';
  if (!visible) return null;

  const progressPct = Math.round((status.progress || 0) * 100);
  const showSkipped = status.skip_version && status.skip_version === status.latest_version;
  const canOneClick = (status.available || status.state === 'ready') && status.state !== 'downloading';

  const guardedAction = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key);
    setLastError(null);
    try {
      await fn();
    } catch (err) {
      setLastError(String(err));
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

  const pillText =
    status.state === 'downloading' ? `Downloading ${progressPct}%`
      : status.available ? `Update ${status.latest_version}`
        : status.state === 'ready' ? `v${status.latest_version} · install`
          : status.state === 'error' ? 'Update failed'
            : stateLabel(status.state);

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className={`update-chip no-drag ${status.state === 'error' ? 'error' : ''}`.trim()}
        title={`ClashGO v${appVersion || '0.0.0'} — ${stateLabel(status.state)}`}
      >
        <Icon
          name={stateIcon(status.state)}
          size={13}
          className={status.state === 'downloading' ? 'spin' : undefined}
        />
        {pillText}
        {status.available ? <span className="update-dot" /> : null}
      </button>

      {open ? createPortal((
        <div
          className="modal-scrim"
          role="presentation"
          onClick={() => setOpen(false)}
        >
          <div
            role="dialog"
            aria-modal="true"
            aria-labelledby="update-banner-title"
            className="modal"
            onClick={(event) => event.stopPropagation()}
          >
            <div className="modal-head">
              <div style={{ minWidth: 0 }}>
                <div className="modal-kicker">
                  <Icon name="download" size={12} />
                  ClashGO update
                </div>
                <h2 id="update-banner-title">
                  {status.available ? `${status.latest_version} is ready` : stateLabel(status.state)}
                </h2>
                <div className="modal-meta">
                  <span className="tag">Current v{appVersion || '0.0.0'}</span>
                  {status.min_supported ? <span className="tag">min v{status.min_supported}</span> : null}
                </div>
              </div>
              <button
                ref={closeButtonRef}
                type="button"
                onClick={() => {
                  setOpen(false);
                  onDismiss();
                }}
                className="icon-btn plain"
                aria-label="Close update dialog"
              >
                <Icon name="x" size={16} />
              </button>
            </div>

            <div className="modal-body">
              {status.state === 'downloading' ? (
                <DownloadBody status={status} progressPct={progressPct} formatBytes={formatBytes} />
              ) : status.state === 'ready' ? (
                <ReadyBody status={status} formatBytes={formatBytes} flash={mountedChecksumFlash} />
              ) : status.state === 'error' ? (
                <ErrorBody status={status} lastError={lastError} />
              ) : status.notes ? (
                <pre className="update-notes">{status.notes}</pre>
              ) : (
                <p className="update-note">No release notes were published for this version.</p>
              )}

              {lastError && status.state !== 'error' ? (
                <div className="error-banner" style={{ marginTop: 12 }}>
                  <Icon name="alert" size={13} />
                  <span className="mono" style={{ fontSize: 10.5, wordBreak: 'break-all' }}>{lastError}</span>
                </div>
              ) : null}

              {isBotRunning && canOneClick ? (
                <div className="update-warn">
                  <Icon name="info" size={12} style={{ display: 'inline', verticalAlign: '-2px', marginRight: 5 }} />
                  The bot is running. Updating stops it cleanly, drains ADB, and relaunches the app.
                </div>
              ) : null}
            </div>

            <div className="modal-foot">
              <button
                type="button"
                onClick={doOneClick}
                disabled={busy !== null || !canOneClick}
                className="btn primary block"
                style={{ height: 40 }}
              >
                <Icon
                  name={busy === 'oneclick' ? 'refresh' : 'download'}
                  size={14}
                  className={busy === 'oneclick' ? 'spin' : undefined}
                />
                {busy === 'oneclick'
                  ? status.state === 'ready' ? 'Installing & restarting…'
                    : progressPct > 0 ? `Downloading & installing (${progressPct}%)…`
                      : 'Downloading & installing…'
                  : status.state === 'ready' ? 'Install & restart'
                    : `Update to v${status.latest_version}`}
              </button>

              <div className="modal-actions">
                <button
                  type="button"
                  onClick={() => doDownload()}
                  disabled={busy !== null || status.state === 'downloading' || !status.available}
                  className="btn sm"
                >
                  <Icon name="download" size={12} />
                  {status.state === 'ready' ? 'Re-download' : 'Download only'}
                </button>
                <button
                  type="button"
                  onClick={() => doApply()}
                  disabled={busy !== null || status.state !== 'ready'}
                  className="btn sm"
                >
                  <Icon name="external" size={12} />
                  Open in Finder
                </button>
              </div>

              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, paddingTop: 2 }}>
                <button
                  type="button"
                  onClick={() => doCheck()}
                  disabled={busy !== null || status.state === 'checking'}
                  className="btn ghost sm"
                >
                  <Icon name="refresh" size={12} className={busy === 'check' ? 'spin' : undefined} />
                  Re-check
                </button>
                <div style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                  <button
                    type="button"
                    onClick={() => doSkip()}
                    disabled={busy !== null || !status.available}
                    className="btn ghost sm"
                  >
                    Skip version
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setOpen(false);
                      onDismiss();
                    }}
                    className="btn ghost sm"
                  >
                    Later
                  </button>
                </div>
              </div>

              {showSkipped ? (
                <button
                  type="button"
                  onClick={() => doClearSkip()}
                  disabled={busy !== null}
                  className="btn ghost sm"
                >
                  Resume notifications for v{status.skip_version}
                </button>
              ) : null}

              {busy !== null ? (
                <div className="modal-meta" style={{ justifyContent: 'flex-start' }}>
                  <Icon name="refresh" size={12} className="spin" />
                  {busyLabel(busy)}
                </div>
              ) : null}
            </div>
          </div>
        </div>
      ), document.body) : null}
    </>
  );
};

const DownloadBody: React.FC<{
  status: UpdateStatus;
  progressPct: number;
  formatBytes: (n: number) => string;
}> = ({ status, progressPct, formatBytes }) => (
  <div>
    <p className="update-note" style={{ marginBottom: 10 }}>
      Downloading <span className="mono" style={{ color: 'var(--text)' }}>{status.asset_name}</span>
      {status.expected_size > 0 ? ` (${formatBytes(status.expected_size)})` : ''}…
    </p>
    <div className="progress">
      <i style={{ width: `${progressPct}%` }} />
    </div>
    <div className="modal-meta" style={{ justifyContent: 'space-between', marginTop: 8 }}>
      <span className="mono">{progressPct}% downloaded</span>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
        <Icon name="shield" size={11} />
        SHA-256 on completion
      </span>
    </div>
  </div>
);

const ReadyBody: React.FC<{
  status: UpdateStatus;
  formatBytes: (n: number) => string;
  flash: boolean;
}> = ({ status, formatBytes, flash }) => (
  <div>
    <div
      className="update-verified"
      style={flash ? { boxShadow: '0 0 24px -8px rgba(76,195,138,.6)' } : undefined}
    >
      <Icon name="shield" size={13} />
      Verified. Ready to install
      {status.expected_size > 0 ? <span className="mono" style={{ marginLeft: 'auto', fontSize: 10.5 }}>{formatBytes(status.expected_size)}</span> : null}
    </div>
    {status.download_path ? <div className="update-path">{status.download_path}</div> : null}
    <p className="update-note" style={{ marginTop: 10 }}>
      Install &amp; restart swaps the running bundle and relaunches ClashGO. The window closes briefly.
    </p>
  </div>
);

const ErrorBody: React.FC<{ status: UpdateStatus; lastError: string | null }> = ({ status, lastError }) => (
  <div>
    <div className="error-banner">
      <Icon name="alert" size={13} />
      <span>Update check did not complete.</span>
    </div>
    {status.error ? <div className="update-path">{status.error}</div> : null}
    {lastError ? <div className="update-path" style={{ color: 'var(--red)' }}>{lastError}</div> : null}
  </div>
);

// Non-dismissible splash while the helper swaps the bundle.
const RestartSplash: React.FC<{ status: UpdateStatus }> = ({ status }) => createPortal(
  <div className="splash">
    <div>
      <div className="splash-mark">
        <Icon name="refresh" size={26} className="spin" />
      </div>
      <h2>Restarting ClashGO</h2>
      <p>
        Installing <span className="mono" style={{ color: 'var(--accent)' }}>v{status.latest_version}</span>.
        The new build will appear in your dock in a moment.
      </p>
      <small>Don’t quit this window</small>
    </div>
  </div>,
  document.body,
);

export default UpdateBanner;
