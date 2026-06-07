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
  };
}

export type TabType = 'dashboard' | 'feed' | 'analytics' | 'config' | 'settings';
