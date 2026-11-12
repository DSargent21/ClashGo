import React from 'react';
import type {
  AccountInput,
  AccountView,
  AccountsState,
  AccountStatus,
  Rotation,
} from '../types';
import { compactNumber, relativeTime } from '../utils';
import Icon from './ui/Icon';
import Drawer from './ui/Drawer';
import { Switch, SwitchRow } from './ui/Switch';
import StatCard from './ui/StatCard';

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

const STATUS_META: Record<AccountStatus, { label: string; tone: string }> = {
  running: { label: 'Running', tone: 'running' },
  ready: { label: 'Ready', tone: '' },
  cooldown: { label: 'Cooldown', tone: 'warn' },
  daily_limit: { label: 'Daily limit', tone: 'warn' },
  disabled: { label: 'Disabled', tone: 'error' },
};

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

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
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
    <form onSubmit={submit}>
      {error ? (
        <div className="error-banner" style={{ marginBottom: 14 }}>
          <Icon name="alert" size={14} />
          <span>{error}</span>
        </div>
      ) : null}

      <div className="form-grid">
        <div className="f-field full">
          <label className="label" htmlFor="acct-name">Village name</label>
          <input
            id="acct-name"
            className="field"
            value={form.name}
            onChange={(event) => patch({ name: event.target.value })}
            placeholder="Main village"
            maxLength={48}
            autoFocus
          />
        </div>
        <div className="f-field">
          <label className="label" htmlFor="acct-tag">Player tag</label>
          <input
            id="acct-tag"
            className="field mono"
            value={form.player_tag}
            onChange={(event) => patch({ player_tag: event.target.value.toUpperCase() })}
            placeholder="#2P0Y8LQGR"
            maxLength={15}
          />
        </div>
        <div className="f-field">
          <label className="label" htmlFor="acct-th">Town hall</label>
          <input
            id="acct-th"
            type="number"
            min={0}
            max={18}
            className="field"
            value={form.town_hall}
            onChange={(event) => patch({ town_hall: Number(event.target.value) || 0 })}
          />
        </div>
        <div className="f-field full">
          <label className="label" htmlFor="acct-email">Login hint (never a password)</label>
          <input
            id="acct-email"
            className="field"
            value={form.email}
            onChange={(event) => patch({ email: event.target.value })}
            placeholder="supercell address for this village"
            maxLength={200}
          />
        </div>
        <div className="f-field">
          <label className="label" htmlFor="acct-priority">Rotation priority</label>
          <input
            id="acct-priority"
            type="number"
            min={0}
            max={1000}
            className="field"
            value={form.priority}
            onChange={(event) => patch({ priority: Number(event.target.value) || 0 })}
          />
        </div>
        <div className="f-field">
          <label className="label" htmlFor="acct-cooldown">Cooldown (min)</label>
          <input
            id="acct-cooldown"
            type="number"
            min={0}
            max={1440}
            className="field"
            value={form.cooldown_minutes}
            onChange={(event) => patch({ cooldown_minutes: Number(event.target.value) || 0 })}
          />
        </div>
      </div>

      <div className="form-section">
        <div className="form-section-title">
          <Icon name="device" size={13} />
          Emulator binding
        </div>
        <div className="form-grid">
          <div className="f-field">
            <label className="label" htmlFor="acct-host">ADB host</label>
            <input
              id="acct-host"
              className="field mono"
              value={form.device.adb_host}
              onChange={(event) => patchDevice({ adb_host: event.target.value })}
            />
          </div>
          <div className="f-field">
            <label className="label" htmlFor="acct-port">ADB server port</label>
            <input
              id="acct-port"
              type="number"
              min={1}
              max={65535}
              className="field mono"
              value={form.device.adb_port}
              onChange={(event) => patchDevice({ adb_port: Number(event.target.value) || 5037 })}
            />
          </div>
          <div className="f-field full">
            <label className="label" htmlFor="acct-serial">Device serial</label>
            <input
              id="acct-serial"
              className="field mono"
              value={form.device.device_id}
              onChange={(event) => patchDevice({ device_id: event.target.value })}
              placeholder="localhost:5556"
            />
          </div>
        </div>
        <p className="hint">
          BlueStacks exposes each instance on the next port (5555, 5556, 5557…). Match the serial
          reported by <span className="mono">adb devices</span>. Device changes apply on next start.
        </p>
      </div>

      <div className="form-grid" style={{ marginTop: 14 }}>
        <div className="f-field">
          <label className="label" htmlFor="acct-daily">Daily attack cap (0 = ∞)</label>
          <input
            id="acct-daily"
            type="number"
            min={0}
            className="field"
            value={form.max_attacks_per_day}
            onChange={(event) => patch({ max_attacks_per_day: Number(event.target.value) || 0 })}
          />
        </div>
        <div className="f-field">
          <label className="label" htmlFor="acct-session">Session cap (0 = default)</label>
          <input
            id="acct-session"
            type="number"
            min={0}
            className="field"
            value={form.max_attacks_per_session}
            onChange={(event) => patch({ max_attacks_per_session: Number(event.target.value) || 0 })}
          />
        </div>
      </div>

      <div className="f-field full" style={{ marginTop: 14 }}>
        <label className="label" htmlFor="acct-notes">Notes</label>
        <textarea
          id="acct-notes"
          rows={3}
          className="field"
          value={form.notes}
          onChange={(event) => patch({ notes: event.target.value })}
          placeholder="Strategy, hero levels, anything the next session should know."
          maxLength={2000}
        />
      </div>

      <div className="form-section">
        <SwitchRow
          checked={form.enabled}
          onChange={(next) => patch({ enabled: next })}
          label="Include in rotation"
          description="Disabled accounts can still be switched to manually."
        />
      </div>

      <div className="form-actions">
        <button type="button" className="btn" onClick={onCancel}>Cancel</button>
        <button type="submit" className="btn primary" disabled={saving}>
          {saving ? 'Saving…' : account ? 'Save changes' : 'Add account'}
        </button>
      </div>
    </form>
  );
};

const RotationPanel: React.FC<{
  rotation: Rotation;
  busy: boolean;
  onChange: (rotation: Rotation) => void;
}> = ({ rotation, busy, onChange }) => (
  <section className="card">
    <div className="card-head">
      <div>
        <h3>Fleet rotation</h3>
        <p className="card-sub">Hand the device to the next eligible account after a session</p>
      </div>
      <div className="card-tools">
        {busy ? <Icon name="refresh" size={13} className="spin" /> : null}
        <Switch
          checked={rotation.enabled}
          onChange={(next) => onChange({ ...rotation, enabled: next })}
          ariaLabel="Enable automatic account rotation"
        />
      </div>
    </div>
    <div className="card-body">
      <div className="rotation-controls">
        <div className="ctl">
          <label className="label" htmlFor="rot-order">Order</label>
          <select
            id="rot-order"
            className="field select"
            disabled={!rotation.enabled}
            value={rotation.order}
            onChange={(event) => onChange({ ...rotation, order: event.target.value as Rotation['order'] })}
          >
            <option value="round_robin">Round robin</option>
            <option value="priority">Priority</option>
          </select>
        </div>
        <div className="ctl">
          <label className="label" htmlFor="rot-attacks">Attacks / account</label>
          <input
            id="rot-attacks"
            type="number"
            min={1}
            max={500}
            className="field mono"
            disabled={!rotation.enabled}
            value={rotation.attacks_per_account}
            onChange={(event) => onChange({
              ...rotation,
              attacks_per_account: Math.max(1, Number(event.target.value) || 1),
            })}
          />
        </div>
        <button
          type="button"
          role="switch"
          aria-checked={rotation.auto_start}
          aria-label="Auto-start the bot after switching accounts"
          disabled={!rotation.enabled}
          onClick={() => onChange({ ...rotation, auto_start: !rotation.auto_start })}
          className={`btn ${rotation.auto_start ? 'primary' : ''}`.trim()}
        >
          <Icon name={rotation.auto_start ? 'check' : 'power'} size={13} />
          Auto-start
        </button>
      </div>
      {rotation.enabled ? (
        <p className="rotation-note">
          Sessions become finite at {rotation.attacks_per_account} attacks unless an account sets
          its own cap. Cooldowns, daily caps, and disabled accounts are skipped; if nobody is
          eligible, the current account keeps running.
        </p>
      ) : null}
    </div>
  </section>
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
  const active = accounts.find((account) => account.active) ?? null;
  const totalToday = accounts.reduce((sum, account) => sum + account.today_attacks, 0);
  const totalGold = accounts.reduce((sum, account) => sum + (account.stats?.total_gold ?? 0), 0);

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
    <div>
      <div className="page-head">
        <div>
          <h1>Accounts</h1>
          <p>One emulator instance per village. Switching hands over the bot safely.</p>
        </div>
        <div className="page-side">
          <button type="button" className="btn sm" onClick={onRefresh} aria-label="Refresh accounts">
            <Icon name="refresh" size={13} className={loading ? 'spin' : undefined} />
            Refresh
          </button>
          <button type="button" className="btn primary sm" onClick={() => setEditor({ account: null })}>
            <Icon name="plus" size={13} />
            Add account
          </button>
        </div>
      </div>

      <div className="fleet-stats">
        <StatCard
          icon="users"
          label="Fleet"
          value={accounts.length}
          detail={`${accounts.filter((account) => account.enabled).length} in rotation`}
        />
        <StatCard
          icon="shield"
          label="Active account"
          value={active?.name ?? '—'}
          detail={active ? active.device.device_id : 'no profile selected'}
          tone="gold"
        />
        <StatCard
          icon="bolt"
          label="Attacks today"
          value={totalToday}
          detail="across all villages"
          tone="blue"
        />
        <StatCard
          icon="coin"
          label="Gold banked"
          value={compactNumber(totalGold)}
          detail="fleet total"
          tone="elixir"
        />
      </div>

      <div className="section-stack" style={{ marginBottom: 16 }}>
        <RotationPanel rotation={rotationDraft} busy={busy === 'rotation'} onChange={changeRotation} />
      </div>

      {(error || actionError) ? (
        <div className="error-banner" style={{ marginBottom: 16 }}>
          <Icon name="alert" size={14} />
          <div style={{ flex: 1 }}>
            <b>Account action failed</b>
            <div style={{ marginTop: 2 }}>{actionError || error}</div>
          </div>
          <button type="button" className="btn sm" onClick={onRefresh}>Retry</button>
        </div>
      ) : null}

      {loading && accounts.length === 0 ? (
        <div className="account-grid">
          {[0, 1].map((index) => (
            <div key={index} className="skeleton" style={{ height: 258 }} />
          ))}
        </div>
      ) : accounts.length === 0 ? (
        <div className="card empty">
          <span className="empty-ico"><Icon name="users" size={20} /></span>
          <b>No accounts yet</b>
          <p>Add your first village and bind it to the emulator instance it is signed in to.</p>
          <button type="button" className="btn primary" onClick={() => setEditor({ account: null })}>
            <Icon name="plus" size={13} />
            Add account
          </button>
        </div>
      ) : (
        <div className="account-grid">
          {accounts.map((account) => {
            const meta = STATUS_META[account.status] ?? STATUS_META.ready;
            const initials = account.name.trim().slice(0, 2).toUpperCase() || '??';
            const isBusy =
              busy === account.id ||
              busy === `delete:${account.id}` ||
              busy === `usage:${account.id}` ||
              busy === `stop:${account.id}`;

            return (
              <article key={account.id} className={`card acct-card ${account.active ? 'active' : ''}`.trim()}>
                <div className="acct-head">
                  <div className="acct-avatar">{initials}</div>
                  <div className="acct-main">
                    <div className="acct-name">
                      <h3>{account.name}</h3>
                      <span className={`status-pill ${meta.tone}`.trim()}>
                        <i />{meta.label}
                      </span>
                      {account.active ? <span className="tag gold">Active</span> : null}
                    </div>
                    <div className="acct-meta">
                      {account.player_tag ? <span className="mono">{account.player_tag}</span> : null}
                      {account.town_hall ? <span>TH{account.town_hall}</span> : null}
                      <span className="mono">{account.device.device_id}</span>
                    </div>
                    <div className="acct-meta">Last used {relativeTime(account.last_used_at)}</div>
                  </div>
                </div>

                <p className={`acct-notes ${account.notes ? '' : 'empty-note'}`.trim()}>
                  {account.notes || 'No notes'}
                </p>

                <div className="acct-tags">
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

                <div className="mini-grid">
                  {[
                    { label: 'Attacks', value: compactNumber(account.stats?.attacks_completed ?? 0) },
                    { label: 'Gold', value: compactNumber(account.stats?.total_gold ?? 0) },
                    { label: 'Elixir', value: compactNumber(account.stats?.total_elixir ?? 0) },
                    {
                      label: 'Today',
                      value: `${account.today_attacks}${account.max_attacks_per_day ? `/${account.max_attacks_per_day}` : ''}`,
                    },
                  ].map((stat) => (
                    <div key={stat.label} className="mini">
                      <span>{stat.label}</span>
                      <b>{stat.value}</b>
                    </div>
                  ))}
                </div>

                <div className="acct-actions">
                  {account.active ? (
                    account.running ? (
                      <button
                        type="button"
                        disabled={isBusy}
                        onClick={() => void run(`stop:${account.id}`, onStop)}
                        className="btn danger"
                      >
                        <Icon name="stop" size={13} />
                        {busy === `stop:${account.id}` ? 'Stopping…' : 'Stop bot'}
                      </button>
                    ) : (
                      <button
                        type="button"
                        disabled={isBusy || !account.enabled}
                        onClick={() => void run(account.id, () => onSwitch(account.id, true))}
                        className="btn primary"
                      >
                        <Icon name="play" size={13} />
                        {busy === account.id ? 'Starting…' : 'Start bot'}
                      </button>
                    )
                  ) : (
                    <>
                      <button
                        type="button"
                        disabled={isBusy || !account.enabled}
                        onClick={() => void run(account.id, () => onSwitch(account.id, true))}
                        className="btn primary"
                      >
                        <Icon name="play" size={13} />
                        {busy === account.id ? 'Switching…' : 'Switch & start'}
                      </button>
                      <button
                        type="button"
                        disabled={isBusy}
                        onClick={() => void run(account.id, () => onSwitch(account.id, false))}
                        className="btn"
                      >
                        Switch
                      </button>
                    </>
                  )}
                  <button
                    type="button"
                    onClick={() => setEditor({ account })}
                    className="icon-btn"
                    aria-label={`Edit ${account.name}`}
                    title="Edit"
                  >
                    <Icon name="edit" size={14} />
                  </button>
                  <button
                    type="button"
                    disabled={isBusy || account.today_attacks === 0}
                    onClick={() => void run(`usage:${account.id}`, () => onResetUsage(account.id))}
                    className="icon-btn"
                    aria-label={`Reset today's usage for ${account.name}`}
                    title={account.today_attacks === 0 ? 'No usage recorded today' : "Reset today's usage"}
                  >
                    <Icon name="refresh" size={14} />
                  </button>
                  <button
                    type="button"
                    disabled={account.active || accounts.length === 1}
                    onClick={() => handleDelete(account.id)}
                    onBlur={() => setConfirmDelete(null)}
                    className={`btn sm ${confirmDelete === account.id ? 'danger' : ''}`.trim()}
                    aria-label={confirmDelete === account.id ? `Confirm delete ${account.name}` : `Delete ${account.name}`}
                    title={account.active ? 'Switch away before deleting the active account' : 'Delete'}
                  >
                    {confirmDelete === account.id ? 'Confirm' : <Icon name="trash" size={14} />}
                  </button>
                </div>
              </article>
            );
          })}
        </div>
      )}

      <Drawer
        open={editor !== null}
        onClose={() => setEditor(null)}
        kicker={editor?.account ? 'Edit village' : 'New village'}
        title={editor?.account ? editor.account.name : 'Add account'}
      >
        {editor ? (
          <AccountForm
            key={editor.account?.id ?? 'new'}
            account={editor.account}
            onCancel={() => setEditor(null)}
            onSave={async (input) => {
              await onSave(editor.account?.id ?? null, input);
              setEditor(null);
            }}
          />
        ) : null}
      </Drawer>
    </div>
  );
});

AccountsView.displayName = 'AccountsView';

export default AccountsView;
