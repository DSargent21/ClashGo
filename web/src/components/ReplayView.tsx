import React, { useEffect, useMemo, useState } from 'react';

const API_BASE = 'http://127.0.0.1:8080';

interface ReplayItem {
  timestamp: string;
  type: 'frame' | 'event';
  subject?: string;
  data?: Record<string, unknown>;
  frame?: string;
  frame_state?: string;
}

const ReplayView: React.FC = () => {
  const [items, setItems] = useState<ReplayItem[]>([]);
  const [selected, setSelected] = useState<ReplayItem | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const loadReplay = async () => {
    setLoading(true);
    setError('');
    try {
      const to = Date.now();
      const from = to - 30_000;
      const res = await fetch(`${API_BASE}/replay?from=${from}&to=${to}`);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const json = await res.json();
      const next = Array.isArray(json.items) ? json.items : [];
      setItems(next);
      setSelected(next.find((item: ReplayItem) => item.type === 'frame') ?? next[0] ?? null);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Replay fetch failed');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void loadReplay();
    const id = setInterval(loadReplay, 2000);
    return () => clearInterval(id);
  }, []);

  const selectedFrame = useMemo(() => {
    if (!selected?.frame) return '';
    return `data:image/jpeg;base64,${selected.frame}`;
  }, [selected]);

  return (
    <div className="grid grid-cols-1 lg:grid-cols-[1fr_360px] gap-6">
      <section className="rounded-3xl border border-zinc-100/60 dark:border-zinc-800/80 bg-white dark:bg-zinc-900 shadow-premium dark:shadow-none p-6">
        <div className="flex items-center justify-between gap-4 mb-6">
          <div>
            <h2 className="text-2xl font-bold">Time Travel Replay</h2>
            <p className="text-sm text-zinc-500 dark:text-zinc-400">Last 30 seconds of frames, state events, loot decisions, and diagnostics.</p>
          </div>
          <button onClick={() => void loadReplay()} className="px-4 py-2 rounded-xl bg-zinc-950 dark:bg-zinc-800 text-white text-sm font-bold">
            Refresh
          </button>
        </div>

        {error && <div className="mb-4 rounded-2xl bg-rose-50 dark:bg-rose-950/30 text-rose-600 dark:text-rose-300 p-4 text-sm">{error}</div>}
        {loading && <div className="text-sm text-zinc-500">Loading replay…</div>}

        {selectedFrame ? (
          <div className="rounded-3xl overflow-hidden bg-zinc-950 border border-zinc-800">
            <img src={selectedFrame} alt="replay frame" className="w-full object-contain max-h-[620px]" />
          </div>
        ) : (
          <div className="rounded-3xl border border-dashed border-zinc-300 dark:border-zinc-700 p-12 text-center text-zinc-500">
            No replay frames yet. Start the bot and wait for captures.
          </div>
        )}
      </section>

      <aside className="rounded-3xl border border-zinc-100/60 dark:border-zinc-800/80 bg-white dark:bg-zinc-900 shadow-premium dark:shadow-none p-4 max-h-[760px] overflow-hidden flex flex-col">
        <h3 className="font-bold mb-3">Event Timeline</h3>
        <div className="space-y-2 overflow-y-auto pr-1">
          {items.map((item, idx) => (
            <button
              key={`${item.timestamp}-${item.type}-${idx}`}
              onClick={() => setSelected(item)}
              className={`w-full text-left rounded-2xl p-3 border transition ${
                selected === item
                  ? 'bg-zinc-950 text-white border-zinc-950 dark:bg-zinc-800 dark:border-zinc-700'
                  : 'bg-zinc-50 dark:bg-zinc-950/40 border-zinc-100 dark:border-zinc-800 hover:bg-zinc-100 dark:hover:bg-zinc-800'
              }`}
            >
              <div className="flex items-center justify-between gap-2">
                <span className="text-xs font-black uppercase tracking-widest">{item.type}</span>
                <span className="text-[10px] text-zinc-500">{new Date(item.timestamp).toLocaleTimeString()}</span>
              </div>
              <div className="mt-1 text-sm font-semibold truncate">
                {item.type === 'frame' ? `Frame · ${item.frame_state ?? 'unknown'}` : item.subject ?? 'event'}
              </div>
              {item.data && (
                <pre className="mt-2 text-[10px] leading-tight whitespace-pre-wrap opacity-70 max-h-20 overflow-hidden">
                  {JSON.stringify(item.data, null, 0).slice(0, 260)}
                </pre>
              )}
            </button>
          ))}
        </div>
      </aside>
    </div>
  );
};

export default ReplayView;
