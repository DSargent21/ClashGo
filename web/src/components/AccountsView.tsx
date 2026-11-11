import React from 'react';
import type {
  AccountInput,
  AccountView,
  AccountsState,
  AccountStatus,
  Rotation,
} from '../types';

const DEFAULT_ROTATION: Rotation = {
  enabled: false,
  order: 'round_robin',
  auto_start: true,
  attacks_per_account: 20,
};

const EMPTY_INPUT: AccountInput = {
  name: '',
  player_tag: '',
  town_hall: 0,
  email: '',
  notes: '',
  device: { adb_host: '127.0.0.1', adb_port: 5037, device_id: 'localhost:5555' },
  enabled: true,
  priority: 100,
  cooldown_minutes: 10,
  max_attacks_per_day: 0,
  max_attacks_per_session: 0,
};

interface AccountsViewProps {
  state: AccountsState | null;
  loading: boolean;
  error: string | null;
  onRefresh: () => void;
  onSwitch: (id: string, start: boolean) => Promise<void>;
  onStop: () => Promise<void>;
  onSave: (id: string | null, input: AccountInput) => Promise<void>;
  onDelete: (id: string) => Promise<void>;
  onResetUsage: (id: string) => Promise<void>;
  onRotation: (rotation: Rotation) => Promise<void>;
}

const STATUS_META: Record<AccountStatus, { label: string; dot: string; chip: string }> = {
  running: {
    label: 'Running',
    dot: 'bg-emerald-500 animate-pulse',
    chip: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/30',
  },
  ready: {
    label: 'Ready',
    dot: 'bg-zinc-400',
    chip: 'bg-zinc-100 dark:bg-zinc-800 text-zinc-600 dark:text-zinc-300 border-zinc-200 dark:border-zinc-700',
  },
  cooldown: {
    label: 'Cooldown',
    dot: 'bg-amber-500',
    chip: 'bg-amber-500/10 text-amber-600 dark:text-amber-400 border-amber-500/30',
  },
  daily_limit: {
    label: 'Daily limit',
    dot: 'bg-orange-500',
    chip: 'bg-orange-500/10 text-orange-600 dark:text-orange-400 border-orange-500/30',
  },
  disabled: {
    label: 'Disabled',
    dot: 'bg-zinc-300 dark:bg-zinc-700',
    chip: 'bg-zinc-100 dark:bg-zinc-800/60 text-zinc-400 dark:text-zinc-600 border-zinc-200 dark:border-zinc-800',
  },
};

function compact(n: number): string {
  if (!Number.isFinite(n) || n === 0) return '0';
  if (Math.abs(n) >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (Math.abs(n) >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (Math.abs(n) >= 1e3) return `${(n / 1e3).toFixed(0)}k`;
  return `${n}`;
}

function relativeTime(iso: string): string {
  if (!iso) return 'never';
  const then = new Date(iso).getTime();
  if (Number.isNaN(then) || then <= 0) return 'never';
  const mins = Math.max(0, Math.floor((Date.now() - then) / 60000));
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

function toInput(account: AccountView | null): AccountInput {
  if (!account) return { ...EMPTY_INPUT, device: { ...EMPTY_INPUT.device } };
  return {
    id: account.id,
    name: account.name,
    player_tag: account.player_tag ?? '',
    town_hall: account.town_hall ?? 0,
    email: account.email ?? '',
    notes: account.notes ?? '',
    device: { ...account.device },
    enabled: account.enabled,
    priority: account.priority,
    cooldown_minutes: account.cooldown_minutes,
    max_attacks_per_day: account.max_attacks_per_day,
    max_attacks_per_session: account.max_attacks_per_session,
  };
}

const fieldClass =
  'w-full h-11 px-4 rounded-xl bg-zinc-50 dark:bg-zinc-950/60 border border-zinc-200 dark:border-zinc-800 text-sm font-semibold text-zinc-900 dark:text-zinc-100 placeholder:text-zinc-400 dark:placeholder:text-zinc-600 focus:outline-none focus:border-emerald-500/50 focus:ring-2 focus:ring-emerald-500/10 transition-all';
const labelClass =
  'block text-[10px] font-black text-zinc-500 dark:text-zinc-500 uppercase tracking-[0.2em] mb-2';

const AccountForm: React.FC<{
  account: AccountView | null;
  onCancel: () => void;
  onSave: (input: AccountInput) => Promise<void>;
}> = ({ account, onCancel, onSave }) => {
  const [form, setForm] = React.useState<AccountInput>(() => toInput(account));
  const [saving, setSaving] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  const patch = (updates: Partial<AccountInput>) => setForm((prev) => ({ ...prev, ...updates }));
  const patchDevice = (updates: Partial<AccountInput['device']>) =>
    setForm((prev) => ({ ...prev, device: { ...prev.device, ...updates } }));

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (saving) return;
    if (!form.name.trim()) {
      setError('Account name is required.');
      return;
    }
    if (!form.device.device_id.trim()) {
      setError('ADB device serial is required (e.g. localhost:5556).');
      return;
    }
    setSaving(true);
    setError(null);
    try {
      await onSave({
        ...form,
        player_tag: form.player_tag.trim(),
        device: {
          adb_host: form.device.adb_host.trim() || '127.0.0.1',
          adb_port: form.device.adb_port || 5037,
          device_id: form.device.device_id.trim(),
        },
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="fixed inset-0 z-[100] flex items-center justify-center p-4 no-drag">
      <button
        type="button"
        aria-label="Close account editor"
        onClick={onCancel}
        className="absolute inset-0 bg-zinc-950/70 backdrop-blur-sm"
      />
      <form
        onSubmit={submit}
        role="dialog"
        aria-modal="true"
        aria-labelledby="account-editor-title"
        className="relative w-full max-w-2xl max-h-[90vh] overflow-y-auto bg-white dark:bg-zinc-900 rounded-[2rem] border border-zinc-200 dark:border-zinc-800 shadow-premium-lg dark:shadow-none p-8 space-y-6"
      >
        <div className="flex items-start justify-between gap-4">
          <div>
            <h3 id="account-editor-title" className="text-2xl font-bold tracking-tight text-zinc-950 dark:text-white">
              {account ? 'Edit account' : 'Add account'}
            </h3>
            <p className="text-sm text-zinc-500 font-medium mt-1">
              Each account runs on its own emulator instance and keeps its own config, stats and history.
            </p>
          </div>
          <button
            type="button"
            onClick={onCancel}
            className="w-10 h-10 rounded-xl flex items-center justify-center text-zinc-400 hover:text-zinc-900 dark:hover:text-white hover:bg-zinc-100 dark:hover:bg-zinc-800 transition-colors"
            aria-label="Close"
          >
            <span className="material-symbols-outlined">close</span>
          </button>
        </div>

        {error && (
          <div className="flex items-start gap-3 p-4 rounded-2xl bg-rose-500/10 border border-rose-500/30 text-sm font-semibold text-rose-600 dark:text-rose-400">
            <span className="material-symbols-outlined text-lg">error</span>
            <span>{error}</span>
          </div>
        )}

        <div className="grid grid-cols-1 sm:grid-cols-2 gap-5">
          <div className="sm:col-span-2">
            <label className={labelClass} htmlFor="acct-name">Village name</label>
            <input
              id="acct-name"
              className={fieldClass}
              value={form.name}
              onChange={(e) => patch({ name: e.target.value })}
              placeholder="Main village"
              maxLength={48}
              autoFocus
            />
          </div>
          <div>
            <label className={labelClass} htmlFor="acct-tag">Player tag</label>
            <input
              id="acct-tag"
              className={fieldClass}
              value={form.player_tag}
              onChange={(e) => patch({ player_tag: e.target.value.toUpperCase() })}
              placeholder="#2P0Y8LQGR"
              maxLength={15}
            />
          </div>
          <div>
            <label className={labelClass} htmlFor="acct-th">Town hall</label>
            <input
              id="acct-th"
              type="number"
              min={0}
              max={18}
              className={fieldClass}
              value={form.town_hall}
              onChange={(e) => patch({ town_hall: Number(e.target.value) || 0 })}
            />
          </div>
          <div>
            <label className={labelClass} htmlFor="acct-email">Login hint (never stored as a password)</label>
            <input
              id="acct-email"
              className={fieldClass}
              value={form.email}
              onChange={(e) => patch({ email: e.target.value })}
              placeholder="supercell address for this village"
              maxLength={200}
            />
          </div>
          <div>
            <label className={labelClass} htmlFor="acct-priority">Rotation priority</label>
            <input
              id="acct-priority"
              type="number"
              min={0}
              max={1000}
              className={fieldClass}
              value={form.priority}
              onChange={(e) => patch({ priority: Number(e.target.value) || 0 })}
            />
          </div>
        </div>

        <div className="p-5 rounded-2xl bg-zinc-50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 space-y-5">
          <div className="flex items-center gap-3">
            <span className="material-symbols-outlined text-zinc-400">devices</span>
            <div>
              <div className="text-[10px] font-black uppercase tracking-[0.2em] text-zinc-500">Emulator binding</div>
              <p className="text-xs text-zinc-500 font-medium">The instance already signed in to this village.</p>
            </div>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
            <div>
              <label className={labelClass} htmlFor="acct-host">ADB host</label>
              <input
                id="acct-host"
                className={fieldClass}
                value={form.device.adb_host}
                onChange={(e) => patchDevice({ adb_host: e.target.value })}
              />
            </div>
            <div>
              <label className={labelClass} htmlFor="acct-port">ADB server port</label>
              <input
                id="acct-port"
                type="number"
                min={1}
                max={65535}
                className={fieldClass}
                value={form.device.adb_port}
                onChange={(e) => patchDevice({ adb_port: Number(e.target.value) || 5037 })}
              />
            </div>
            <div>
              <label className={labelClass} htmlFor="acct-serial">Device serial</label>
              <input
                id="acct-serial"
                className={`${fieldClass} font-mono`}
                value={form.device.device_id}
                onChange={(e) => patchDevice({ device_id: e.target.value })}
                placeholder="localhost:5556"
              />
            </div>
          </div>
          <p className="text-[11px] text-zinc-500 font-medium">
            BlueStacks exposes each instance on the next port (5555, 5556, 5557…). Match the serial the
            instance reports in <span className="font-mono">adb devices</span>. Device changes apply on the
            next start.
          </p>
        </div>

        <div className="grid grid-cols-1 sm:grid-cols-3 gap-5">
          <div>
            <label className={labelClass} htmlFor="acct-cooldown">Rotation cooldown (min)</label>
            <input
              id="acct-cooldown"
              type="number"
              min={0}
              max={1440}
              className={fieldClass}
              value={form.cooldown_minutes}
              onChange={(e) => patch({ cooldown_minutes: Number(e.target.value) || 0 })}
            />
          </div>
          <div>
            <label className={labelClass} htmlFor="acct-daily">Daily attack cap (0 = ∞)</label>
            <input
              id="acct-daily"
              type="number"
              min={0}
              className={fieldClass}
              value={form.max_attacks_per_day}
              onChange={(e) => patch({ max_attacks_per_day: Number(e.target.value) || 0 })}
            />
          </div>
          <div>
            <label className={labelClass} htmlFor="acct-session">Session cap (0 = default)</label>
            <input
              id="acct-session"
              type="number"
              min={0}
              className={fieldClass}
              value={form.max_attacks_per_session}
              onChange={(e) => patch({ max_attacks_per_session: Number(e.target.value) || 0 })}
            />
          </div>
        </div>

        <div>
          <label className={labelClass} htmlFor="acct-notes">Notes</label>
          <textarea
            id="acct-notes"
            rows={3}
            className="w-full px-4 py-3 rounded-xl bg-zinc-50 dark:bg-zinc-950/60 border border-zinc-200 dark:border-zinc-800 text-sm font-semibold text-zinc-900 dark:text-zinc-100 placeholder:text-zinc-400 dark:placeholder:text-zinc-600 focus:outline-none focus:border-emerald-500/50 focus:ring-2 focus:ring-emerald-500/10 transition-all resize-none"
            value={form.notes}
            onChange={(e) => patch({ notes: e.target.value })}
            placeholder="Strategy, hero levels, anything the next session should know."
            maxLength={2000}
          />
        </div>

        <button
          type="button"
          role="switch"
          aria-checked={form.enabled}
          onClick={() => patch({ enabled: !form.enabled })}
          className="w-full flex items-center justify-between p-4 rounded-2xl bg-zinc-50 dark:bg-zinc-950/40 border border-zinc-100 dark:border-zinc-800 hover:bg-white dark:hover:bg-zinc-800/40 transition-colors text-left"
        >
          <div className="flex items-center gap-3">
            <span className="material-symbols-outlined text-zinc-400">power_settings_new</span>
            <div>
              <div className="text-sm font-bold text-zinc-950 dark:text-white">Include in rotation</div>
              <div className="text-xs text-zinc-500 font-medium">Disabled accounts can still be switched to manually.</div>
            </div>
          </div>
          <div className={`w-12 h-6 rounded-full p-1 transition-all ${form.enabled ? 'bg-emerald-500' : 'bg-zinc-300 dark:bg-zinc-700'}`}>
            <div className={`w-4 h-4 rounded-full bg-white shadow transition-all ${form.enabled ? 'translate-x-6' : 'translate-x-0'}`} />
          </div>
        </button>

        <div className="flex items-center justify-end gap-3 pt-2">
          <button
            type="button"
            onClick={onCancel}
            className="h-11 px-6 rounded-xl text-[11px] font-black uppercase tracking-[0.2em] text-zinc-500 hover:text-zinc-900 dark:hover:text-white transition-colors"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={saving}
            className="h-11 px-7 rounded-xl bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950 text-[11px] font-black uppercase tracking-[0.2em] hover:bg-zinc-800 dark:hover:bg-white transition-all active:scale-[0.98] disabled:opacity-50"
          >
            {saving ? 'Saving…' : account ? 'Save changes' : 'Add account'}
          </button>
        </div>
      </form>
    </div>
  );
};

const RotationPanel: React.FC<{
  rotation: Rotation;
  busy: boolean;
  onChange: (rotation: Rotation) => void;
}> = ({ rotation, busy, onChange }) => (
  <div className="bg-white dark:bg-zinc-900 rounded-[2.5rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none p-6">
    <div className="flex flex-wrap items-center justify-between gap-4">
      <div className="flex items-center gap-4">
        <button
          type="button"
          role="switch"
          aria-checked={rotation.enabled}
          onClick={() => onChange({ ...rotation, enabled: !rotation.enabled })}
          className={`w-12 h-6 rounded-full p-1 transition-all ${rotation.enabled ? 'bg-emerald-500' : 'bg-zinc-200 dark:bg-zinc-800'}`}
          aria-label="Enable automatic account rotation"
        >
          <div className={`w-4 h-4 rounded-full bg-white shadow transition-all ${rotation.enabled ? 'translate-x-6' : 'translate-x-0'}`} />
        </button>
        <div>
          <div className="text-sm font-bold text-zinc-950 dark:text-white flex items-center gap-2">
            Fleet rotation
            {busy && <span className="material-symbols-outlined text-sm animate-spin text-zinc-400">progress_activity</span>}
          </div>
          <p className="text-xs text-zinc-500 font-medium">
            Hand the device to the next eligible account when a session ends.
          </p>
        </div>
      </div>

      <div className="flex flex-wrap items-end gap-4">
        <div>
          <label className={labelClass} htmlFor="rot-order">Order</label>
          <select
            id="rot-order"
            disabled={!rotation.enabled}
            className="h-10 px-3 rounded-xl bg-zinc-50 dark:bg-zinc-950/60 border border-zinc-200 dark:border-zinc-800 text-xs font-bold text-zinc-900 dark:text-zinc-100 focus:outline-none transition-opacity disabled:opacity-40 disabled:cursor-not-allowed"
            value={rotation.order}
            onChange={(e) => onChange({ ...rotation, order: e.target.value as Rotation['order'] })}
          >
            <option value="round_robin">Round robin</option>
            <option value="priority">Priority</option>
          </select>
        </div>
        <div>
          <label className={labelClass} htmlFor="rot-attacks">Attacks / account</label>
          <input
            id="rot-attacks"
            type="number"
            min={1}
            max={500}
            disabled={!rotation.enabled}
            className="h-10 w-24 px-3 rounded-xl bg-zinc-50 dark:bg-zinc-950/60 border border-zinc-200 dark:border-zinc-800 text-xs font-bold text-zinc-900 dark:text-zinc-100 focus:outline-none transition-opacity disabled:opacity-40 disabled:cursor-not-allowed"
            value={rotation.attacks_per_account}
            onChange={(e) => onChange({ ...rotation, attacks_per_account: Math.max(1, Number(e.target.value) || 1) })}
          />
        </div>
        <button
          type="button"
          role="switch"
          aria-checked={rotation.auto_start}
          aria-label="Auto-start the bot after switching accounts"
          disabled={!rotation.enabled}
          onClick={() => onChange({ ...rotation, auto_start: !rotation.auto_start })}
          className={`h-10 px-3 rounded-xl flex items-center gap-2 text-[10px] font-black uppercase tracking-[0.15em] border transition-all disabled:opacity-40 disabled:cursor-not-allowed ${
            rotation.auto_start
              ? 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400 border-emerald-500/30'
              : 'bg-zinc-50 dark:bg-zinc-950/60 text-zinc-500 border-zinc-200 dark:border-zinc-800'
          }`}
        >
          <span className={`w-7 h-4 rounded-full p-0.5 transition-colors ${rotation.auto_start ? 'bg-emerald-500' : 'bg-zinc-300 dark:bg-zinc-700'}`}>
            <span className={`block w-3 h-3 rounded-full bg-white shadow transition-transform ${rotation.auto_start ? 'translate-x-3' : 'translate-x-0'}`} />
          </span>
          Auto-start
        </button>
      </div>
    </div>
    {rotation.enabled && (
      <p className="mt-4 pt-4 border-t border-zinc-100 dark:border-zinc-800 text-[11px] font-medium text-zinc-500">
        Sessions become finite at {rotation.attacks_per_account} attacks unless an account sets its own session cap.
        Cooldowns, daily caps and disabled accounts are skipped; if nobody is eligible the current account keeps running.
      </p>
    )}
  </div>
);

const AccountsView: React.FC<AccountsViewProps> = React.memo(({
  state,
  loading,
  error,
  onRefresh,
  onSwitch,
  onStop,
  onSave,
  onDelete,
  onResetUsage,
  onRotation,
}) => {
  const [editor, setEditor] = React.useState<{ account: AccountView | null } | null>(null);
  const [busy, setBusy] = React.useState<string | null>(null);
  const [actionError, setActionError] = React.useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = React.useState<string | null>(null);
  const [rotationDraft, setRotationDraft] = React.useState<Rotation>(state?.rotation ?? DEFAULT_ROTATION);

  const rotation = state?.rotation ?? DEFAULT_ROTATION;
  React.useEffect(() => {
    setRotationDraft(rotation);
  }, [rotation.enabled, rotation.order, rotation.auto_start, rotation.attacks_per_account]);

  const accounts = state?.accounts ?? [];
  const active = accounts.find((a) => a.active) ?? null;
  const totalToday = accounts.reduce((sum, a) => sum + a.today_attacks, 0);
  const totalGold = accounts.reduce((sum, a) => sum + (a.stats?.total_gold ?? 0), 0);

  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    setActionError(null);
    try {
      await fn();
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(null);
    }
  };

  const changeRotation = (next: Rotation) => {
    setRotationDraft(next);
    void run('rotation', () => onRotation(next));
  };

  const handleDelete = (id: string) => {
    if (confirmDelete !== id) {
      setConfirmDelete(id);
      return;
    }
    setConfirmDelete(null);
    void run(`delete:${id}`, () => onDelete(id));
  };

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-1 md:grid-cols-3 gap-6">
        {[
          { label: 'Fleet', value: `${accounts.length}`, detail: `${accounts.filter((a) => a.enabled).length} in rotation`, icon: 'group' },
          { label: 'Active account', value: active?.name ?? '—', detail: active ? active.device.device_id : 'no profile selected', icon: 'person' },
          { label: 'Today', value: `${totalToday}`, detail: `${compact(totalGold)} gold banked`, icon: 'bolt' },
        ].map((item) => (
          <div key={item.label} className="bg-white dark:bg-zinc-900 p-6 rounded-[2rem] border border-zinc-100/50 dark:border-zinc-800/50 shadow-premium dark:shadow-none flex items-center gap-5">
            <div className="w-12 h-12 rounded-2xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center border border-zinc-100 dark:border-zinc-700">
              <span className="material-symbols-outlined text-zinc-500 text-xl">{item.icon}</span>
            </div>
            <div className="min-w-0">
              <div className="text-[10px] font-black text-zinc-500 uppercase tracking-[0.2em]">{item.label}</div>
              <div className="text-2xl font-bold text-zinc-950 dark:text-white truncate">{item.value}</div>
              <div className="text-[10px] font-bold text-zinc-400 dark:text-zinc-600 truncate">{item.detail}</div>
            </div>
          </div>
        ))}
      </div>

      <RotationPanel rotation={rotationDraft} busy={busy === 'rotation'} onChange={changeRotation} />

      {(error || actionError) && (
        <div className="flex items-start gap-3 p-5 rounded-2xl bg-rose-500/10 border border-rose-500/30 text-sm font-semibold text-rose-600 dark:text-rose-400">
          <span className="material-symbols-outlined text-lg">error</span>
          <div className="flex-1">
            <div className="font-bold">Account action failed</div>
            <div className="font-medium opacity-90 mt-0.5">{actionError || error}</div>
          </div>
          <button type="button" onClick={onRefresh} className="text-[10px] font-black uppercase tracking-widest hover:underline">
            Retry
          </button>
        </div>
      )}

      <div className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h3 className="text-xl font-bold tracking-tight text-zinc-950 dark:text-white">Villages</h3>
          <p className="text-sm text-zinc-500 font-medium">One emulator instance per village. Switch hands over the bot safely.</p>
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onRefresh}
            className="w-11 h-11 rounded-2xl bg-white dark:bg-zinc-900 border border-zinc-100 dark:border-zinc-800 flex items-center justify-center text-zinc-500 hover:text-zinc-900 dark:hover:text-white transition-colors"
            aria-label="Refresh accounts"
          >
            <span className={`material-symbols-outlined text-xl ${loading ? 'animate-spin' : ''}`}>refresh</span>
          </button>
          <button
            type="button"
            onClick={() => setEditor({ account: null })}
            className="h-11 px-5 rounded-2xl bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950 text-[11px] font-black uppercase tracking-[0.2em] flex items-center gap-2 hover:bg-zinc-800 dark:hover:bg-white transition-all active:scale-[0.98]"
          >
            <span className="material-symbols-outlined text-lg">add</span>
            Add account
          </button>
        </div>
      </div>

      {loading && accounts.length === 0 ? (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
          {[0, 1].map((i) => (
            <div key={i} className="h-64 rounded-[2.5rem] bg-white dark:bg-zinc-900 border border-zinc-100 dark:border-zinc-800 animate-pulse" />
          ))}
        </div>
      ) : accounts.length === 0 ? (
        <div className="bg-white dark:bg-zinc-900 rounded-[2.5rem] border border-dashed border-zinc-200 dark:border-zinc-800 p-16 text-center">
          <div className="w-16 h-16 mx-auto rounded-3xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center mb-5">
            <span className="material-symbols-outlined text-3xl text-zinc-400">group_add</span>
          </div>
          <h4 className="text-lg font-bold text-zinc-950 dark:text-white">No accounts yet</h4>
          <p className="text-sm text-zinc-500 font-medium mt-1 max-w-md mx-auto">
            Add your first village and bind it to the emulator instance it is signed in to. You can add more at any time.
          </p>
          <button
            type="button"
            onClick={() => setEditor({ account: null })}
            className="mt-6 h-11 px-6 rounded-2xl bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950 text-[11px] font-black uppercase tracking-[0.2em]"
          >
            Add account
          </button>
        </div>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
          {accounts.map((account) => {
            const meta = STATUS_META[account.status] ?? STATUS_META.ready;
            const initials = account.name.trim().slice(0, 2).toUpperCase() || '??';
            const isBusy =
              busy === account.id ||
              busy === `delete:${account.id}` ||
              busy === `usage:${account.id}` ||
              busy === `stop:${account.id}`;
            return (
              <div
                key={account.id}
                className={`relative bg-white dark:bg-zinc-900 rounded-[2.5rem] border p-6 shadow-premium dark:shadow-none transition-all duration-300 ${
                  account.active
                    ? 'border-emerald-500/40 dark:border-emerald-500/30'
                    : 'border-zinc-100/50 dark:border-zinc-800/50'
                }`}
              >
                {account.active && (
                  <div className="absolute top-0 right-8 -translate-y-1/2 px-3 py-1 rounded-full bg-emerald-500 text-white text-[9px] font-black uppercase tracking-[0.2em]">
                    Active
                  </div>
                )}

                <div className="flex items-start gap-4">
                  <div className={`w-14 h-14 rounded-2xl flex items-center justify-center text-lg font-black text-white shadow-sm ${
                    account.active ? 'bg-emerald-500' : 'bg-zinc-900 dark:bg-zinc-700'
                  }`}>
                    {initials}
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2 flex-wrap">
                      <h4 className="text-lg font-bold tracking-tight text-zinc-950 dark:text-white truncate">{account.name}</h4>
                      <span className={`px-2.5 py-0.5 rounded-full border text-[9px] font-black uppercase tracking-[0.15em] flex items-center gap-1.5 ${meta.chip}`}>
                        <span className={`w-1.5 h-1.5 rounded-full ${meta.dot}`} />
                        {meta.label}
                      </span>
                    </div>
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 mt-1 text-[11px] font-bold text-zinc-500">
                      {account.player_tag && <span className="font-mono">{account.player_tag}</span>}
                      {!!account.town_hall && <span>TH{account.town_hall}</span>}
                      <span className="font-mono truncate">{account.device.device_id}</span>
                    </div>
                    <div className="text-[10px] font-bold text-zinc-400 dark:text-zinc-600 mt-0.5">
                      Last used {relativeTime(account.last_used_at)}
                    </div>
                  </div>
                </div>

                <div className="mt-4 min-h-[2rem]">
                  <p className={`text-xs font-medium line-clamp-2 ${account.notes ? 'text-zinc-500' : 'text-zinc-400 dark:text-zinc-600 italic'}`}>
                    {account.notes || 'No notes'}
                  </p>
                </div>

                <div className="flex flex-wrap items-center gap-x-3 gap-y-1 mt-2 text-[10px] font-black uppercase tracking-[0.12em] text-zinc-400 dark:text-zinc-600">
                  <span>Priority {account.priority}</span>
                  <span>Cooldown {account.cooldown_minutes}m</span>
                  <span>
                    {account.max_attacks_per_session > 0
                      ? `${account.max_attacks_per_session}/session`
                      : rotation.enabled
                        ? `Fleet ${rotation.attacks_per_account}/session`
                        : 'Config session'}
                  </span>
                </div>

                <div className="grid grid-cols-4 gap-3 mt-4">
                  {[
                    { label: 'Attacks', value: compact(account.stats?.attacks_completed ?? 0) },
                    { label: 'Gold', value: compact(account.stats?.total_gold ?? 0) },
                    { label: 'Elixir', value: compact(account.stats?.total_elixir ?? 0) },
                    { label: 'Today', value: `${account.today_attacks}${account.max_attacks_per_day ? `/${account.max_attacks_per_day}` : ''}` },
                  ].map((stat) => (
                    <div key={stat.label} className="p-3 rounded-xl bg-zinc-50 dark:bg-zinc-950/50 border border-zinc-100 dark:border-zinc-800/60">
                      <div className="text-[10px] font-black uppercase tracking-[0.15em] text-zinc-500">{stat.label}</div>
                      <div className="text-sm font-bold text-zinc-950 dark:text-white tabular-nums">{stat.value}</div>
                    </div>
                  ))}
                </div>

                <div className="flex flex-wrap items-center gap-2 mt-5">
                  {account.active ? (
                    account.running ? (
                      <button
                        type="button"
                        disabled={isBusy}
                        onClick={() => void run(`stop:${account.id}`, onStop)}
                        className="flex-1 min-w-[168px] h-11 rounded-xl bg-rose-50 dark:bg-rose-950/30 text-rose-600 dark:text-rose-400 border border-rose-100 dark:border-rose-900/30 text-[10px] font-black uppercase tracking-[0.2em] disabled:opacity-40 hover:bg-rose-100 dark:hover:bg-rose-950/50 transition-all active:scale-[0.98] flex items-center justify-center gap-2"
                      >
                        <span className="material-symbols-outlined text-lg">stop</span>
                        {busy === `stop:${account.id}` ? 'Stopping…' : 'Stop bot'}
                      </button>
                    ) : (
                      <button
                        type="button"
                        disabled={isBusy || !account.enabled}
                        onClick={() => void run(account.id, () => onSwitch(account.id, true))}
                        className="flex-1 min-w-[168px] h-11 rounded-xl bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950 text-[10px] font-black uppercase tracking-[0.2em] disabled:opacity-40 hover:bg-zinc-800 dark:hover:bg-white transition-all active:scale-[0.98]"
                      >
                        {busy === account.id ? 'Starting…' : 'Start bot'}
                      </button>
                    )
                  ) : (
                    <>
                      <button
                        type="button"
                        disabled={isBusy || !account.enabled}
                        onClick={() => void run(account.id, () => onSwitch(account.id, true))}
                        className="flex-1 min-w-[168px] h-11 rounded-xl bg-zinc-950 dark:bg-zinc-100 text-white dark:text-zinc-950 text-[10px] font-black uppercase tracking-[0.2em] disabled:opacity-40 hover:bg-zinc-800 dark:hover:bg-white transition-all active:scale-[0.98]"
                      >
                        {busy === account.id ? 'Switching…' : 'Switch & start'}
                      </button>
                      <button
                        type="button"
                        disabled={isBusy}
                        onClick={() => void run(account.id, () => onSwitch(account.id, false))}
                        className="h-11 px-4 rounded-xl bg-zinc-50 dark:bg-zinc-800 text-[10px] font-black uppercase tracking-[0.15em] text-zinc-600 dark:text-zinc-300 hover:bg-zinc-100 dark:hover:bg-zinc-700 transition-colors"
                      >
                        Switch
                      </button>
                    </>
                  )}
                  <button
                    type="button"
                    onClick={() => setEditor({ account })}
                    className="w-11 h-11 rounded-xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center text-zinc-500 hover:text-zinc-900 dark:hover:text-white transition-colors"
                    aria-label={`Edit ${account.name}`}
                    title="Edit"
                  >
                    <span className="material-symbols-outlined text-lg">edit</span>
                  </button>
                  <button
                    type="button"
                    disabled={isBusy || account.today_attacks === 0}
                    onClick={() => void run(`usage:${account.id}`, () => onResetUsage(account.id))}
                    className="w-11 h-11 rounded-xl bg-zinc-50 dark:bg-zinc-800 flex items-center justify-center text-zinc-500 hover:text-zinc-900 dark:hover:text-white transition-colors disabled:opacity-30 disabled:cursor-not-allowed"
                    aria-label={`Reset today's usage for ${account.name}`}
                    title={account.today_attacks === 0 ? 'No usage recorded today' : "Reset today's usage"}
                  >
                    <span className="material-symbols-outlined text-lg">restart_alt</span>
                  </button>
                  <button
                    type="button"
                    disabled={account.active || accounts.length === 1}
                    onClick={() => handleDelete(account.id)}
                    onBlur={() => setConfirmDelete(null)}
                    className={`h-11 px-3 rounded-xl flex items-center justify-center transition-colors disabled:opacity-30 ${
                      confirmDelete === account.id
                        ? 'bg-rose-500 text-white text-[9px] font-black uppercase tracking-widest'
                        : 'bg-zinc-50 dark:bg-zinc-800 text-zinc-500 hover:text-rose-500'
                    }`}
                    aria-label={confirmDelete === account.id ? `Confirm delete ${account.name}` : `Delete ${account.name}`}
                    title={account.active ? 'Switch away before deleting the active account' : 'Delete'}
                  >
                    {confirmDelete === account.id ? 'Confirm' : <span className="material-symbols-outlined text-lg">delete</span>}
                  </button>
                </div>
              </div>
            );
          })}
        </div>
      )}

      {editor && (
        <AccountForm
          key={editor.account?.id ?? 'new'}
          account={editor.account}
          onCancel={() => setEditor(null)}
          onSave={async (input) => {
            await onSave(editor.account?.id ?? null, input);
            setEditor(null);
          }}
        />
      )}
    </div>
  );
});

export default AccountsView;
