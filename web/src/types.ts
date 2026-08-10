export interface BotStats {
  attacks_completed: number;
  search_skips: number;
  total_gold: number;
  total_elixir: number;
  total_de: number;
  stars_0: number;
  stars_1: number;
  stars_2: number;
  stars_3: number;
  uptime: number; // nanoseconds
  // Device-independent CPU metrics. cpu_time_sec is absolute CPU seconds
  // since process start (comparable across machines). cpu_cores is CPU usage
  // as a fraction of one core over the last sample window (1.0 = one full
  // core busy). To show a 0-100% number on a specific host, multiply
  // cpu_cores by that host's logical core count.
  cpu_time_sec: number;
  cpu_cores: number;
  adb_health: {
    avg_capture_ms: number;
    consecutive_fails: number;
    last_error?: string;
  };
}

export interface AttackReport {
  timestamp: string;
  strategy: string;
  target_edge: string;
  deploy_success: boolean;
  undeployed_slots: number;
  deploy_error?: string;
  parsed_results: boolean;
  stars: number;
  gold_stolen: number;
  elixir_stolen: number;
  dark_elixir_stolen: number;
  bonus_gold: number;
  bonus_elixir: number;
  bonus_de: number;
  total_attacks_session: number;
}

export interface BotConfig {
  search: {
    min_loot_gold: number;
    min_loot_elixir: number;
    min_loot_de: number;
    enabled: boolean;
  };
  upgrade: {
    upgrade_walls: boolean;
  };
  attack: {
    strategy_file: string;
    stall_timer_seconds: number;
  };
}

export type TabType = 'dashboard' | 'analytics' | 'config' | 'settings';

// UpdateStatus mirrors internal/updater.Status (Go side).
// Casing follows Wails JSON convention (snake_case). Keep field names
// in sync with the Go struct — renaming here without updating
// updater.Status will break the banner silently.
export interface UpdateStatus {
  current_version: string;
  latest_version: string;
  available: boolean;
  // 'idle' | 'checking' | 'available' | 'downloading' | 'ready' | 'installing' | 'error' | 'up_to_date'
  state: string;
  progress: number; // 0..1
  notes: string;
  release_url: string;
  asset_name: string;
  download_path: string;
  expected_size: number;
  downloaded_size: number;
  error: string;
  last_checked_unix: number;
  skip_version: string;
  min_supported: string;
}

export const DEFAULT_UPDATE_STATUS: UpdateStatus = {
  current_version: '',
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
};
