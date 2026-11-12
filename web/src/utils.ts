/** Compact, human-readable number for dense UI (1.2M, 48K, 912). */
export const compactNumber = (value: number): string => {
  if (!Number.isFinite(value) || value === 0) return '0';
  const abs = Math.abs(value);
  if (abs >= 1e9) return `${(value / 1e9).toFixed(1).replace(/\.0$/, '')}B`;
  if (abs >= 1e6) return `${(value / 1e6).toFixed(1).replace(/\.0$/, '')}M`;
  if (abs >= 1e3) return `${Math.round(value / 1e3)}K`;
  return String(Math.round(value));
};

/** Exact grouped number for values that must remain auditable. */
export const exactNumber = (value: number): string =>
  Number.isFinite(value) ? Math.round(value).toLocaleString('en-US') : '0';

export const formatClock = (date: Date = new Date(), use24Hour = true): string =>
  date.toLocaleTimeString(use24Hour ? 'en-GB' : 'en-US', {
    hour12: !use24Hour,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });

/** Relative time from an ISO timestamp or RFC3339 string. */
export const relativeTime = (iso: string, fallback = 'never'): string => {
  if (!iso) return fallback;
  const then = new Date(iso).getTime();
  if (Number.isNaN(then) || then <= 0) return fallback;
  const seconds = Math.max(1, Math.floor((Date.now() - then) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
};

/** Trim a strategy path (`assets/strategies/valk_spam.yaml`) to a display label. */
const STRATEGY_ACRONYMS: Record<string, string> = { edrag: 'EDrag', de: 'DE', th: 'TH', adb: 'ADB' };

export const strategyLabel = (path: string): string => {
  if (!path) return 'Not configured';
  const file = path.split('/').pop() ?? path;
  const label = file.replace(/\.(yaml|csv)$/i, '').replace(/_/g, ' ');
  return label
    .split(' ')
    .map((word) => STRATEGY_ACRONYMS[word.toLowerCase()] ?? word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ');
};

export const formatUptime = (ns: number) => {
  if (!ns) return '0h 0m';
  const seconds = Math.floor(ns / 1e9);
  const hrs = Math.floor(seconds / 3600);
  const mins = Math.floor((seconds % 3600) / 60);
  return `${hrs}h ${mins}m`;
};

export const cleanLogMessage = (msg: string) => {
  return msg.replace(/\x1b\[[0-9;]*m/g, '').trim();
};

// Severity model shared by the console viewer + its filter chips.
export type LogSeverity = 'debug' | 'info' | 'success' | 'warn' | 'error';

export interface ParsedLogLine {
  // Wall-clock time from the zerolog ConsoleWriter prefix (HH:MM:SS),
  // or '' when the line carried no timestamp.
  timestamp: string;
  level: LogSeverity;
  message: string;
}

const LEVEL_MAP: Record<string, LogSeverity> = {
  DBG: 'debug', DEBUG: 'debug',
  INF: 'info', INFO: 'info',
  WRN: 'warn', WARN: 'warn',
  ERR: 'error', ERROR: 'error',
  FTL: 'error', FATAL: 'error',
  PNC: 'error', PANIC: 'error',
};

// Words that make a plain INFO line read as a positive event (rendered
// in the success color). Kept deliberately small so the color coding
// stays meaningful.
const SUCCESS_HINTS = /\b(success|complete|verified|launched)\b/i;

/**
 * Parses one raw log line from the Go-side ring buffer into a
 * structured { timestamp, level, message } triple.
 *
 * The buffer holds zerolog ConsoleWriter output (internal/logger),
 * which looks like:
 *
 *   15:04:05 | INF | Bot started adb_port=5555
 *
 * with ANSI color codes around the level token. Older/other lines may
 * be JSON ({"level":"error","message":"..."}) or bare text; those are
 * handled with fallbacks so nothing renders as raw escape codes.
 */
export const parseLogLine = (raw: string): ParsedLogLine => {
  const cleaned = cleanLogMessage(raw);
  if (!cleaned) return { timestamp: '', level: 'info', message: '' };

  // Canonical zerolog console form: `HH:MM:SS | LEVEL | message`.
  const consoleMatch = cleaned.match(/^(\d{2}:\d{2}:\d{2})\s*\|\s*([A-Z]+)\s*\|\s*([\s\S]*)$/);
  if (consoleMatch) {
    const level = LEVEL_MAP[consoleMatch[2].toUpperCase()] ?? 'info';
    const message = consoleMatch[3] || cleaned;
    return {
      timestamp: consoleMatch[1],
      level: level === 'info' && SUCCESS_HINTS.test(message) ? 'success' : level,
      message,
    };
  }

  // JSON lines (file-format parity for lines that ever land in the buffer).
  if (cleaned.startsWith('{')) {
    try {
      const parsed = JSON.parse(cleaned);
      const level = LEVEL_MAP[String(parsed.level || '').toUpperCase()] ?? 'info';
      const message = typeof parsed.message === 'string' ? parsed.message : cleaned;
      return {
        timestamp: parsed.time ? String(parsed.time).slice(11, 19) : '',
        level: level === 'info' && SUCCESS_HINTS.test(message) ? 'success' : level,
        message,
      };
    } catch {
      // fall through to bare-text handling
    }
  }

  // Bare text: sniff an optional [ERROR] / [INFO] / SUCCESS prefix.
  const prefixMatch = cleaned.match(/^\[?(ERROR|WARN|DEBUG|INFO|SUCCESS|FATAL|PANIC)\]?\s*[:|-]?\s*/i);
  if (prefixMatch) {
    const key = prefixMatch[1].toUpperCase();
    const level: LogSeverity =
      key === 'ERROR' || key === 'FATAL' || key === 'PANIC' ? 'error'
        : key === 'WARN' ? 'warn'
          : key === 'DEBUG' ? 'debug'
            : key === 'SUCCESS' ? 'success'
              : 'info';
    return { timestamp: '', level, message: cleaned.slice(prefixMatch[0].length) || cleaned };
  }

  return { timestamp: '', level: 'info', message: cleaned };
};

/**
 * Memoised parseLogLine. The console buffer is polled every 2s and almost
 * every line is unchanged between polls, so each distinct line is parsed once.
 * The cache is small and bounded: the Go buffer holds 100 lines.
 */
const PARSED_CACHE_LIMIT = 512;
const parsedCache = new Map<string, ParsedLogLine>();

export const parseLogLineCached = (raw: string): ParsedLogLine => {
  const hit = parsedCache.get(raw);
  if (hit) return hit;
  const parsed = parseLogLine(raw);
  if (parsedCache.size >= PARSED_CACHE_LIMIT) parsedCache.clear();
  parsedCache.set(raw, parsed);
  return parsed;
};
