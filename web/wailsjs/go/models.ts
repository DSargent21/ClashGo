export namespace adb {
	
	export class Health {
	    // Go type: time
	    last_capture: any;
	    avg_capture_ms: number;
	    consecutive_fails: number;
	    captures_total: number;
	    errors_total: number;
	    last_error: string;
	
	    static createFrom(source: any = {}) {
	        return new Health(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.last_capture = this.convertValues(source["last_capture"], null);
	        this.avg_capture_ms = source["avg_capture_ms"];
	        this.consecutive_fails = source["consecutive_fails"];
	        this.captures_total = source["captures_total"];
	        this.errors_total = source["errors_total"];
	        this.last_error = source["last_error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace bot {
	
	export class AttackReport {
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
	
	    static createFrom(source: any = {}) {
	        return new AttackReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.timestamp = source["timestamp"];
	        this.strategy = source["strategy"];
	        this.target_edge = source["target_edge"];
	        this.deploy_success = source["deploy_success"];
	        this.undeployed_slots = source["undeployed_slots"];
	        this.deploy_error = source["deploy_error"];
	        this.parsed_results = source["parsed_results"];
	        this.stars = source["stars"];
	        this.gold_stolen = source["gold_stolen"];
	        this.elixir_stolen = source["elixir_stolen"];
	        this.dark_elixir_stolen = source["dark_elixir_stolen"];
	        this.bonus_gold = source["bonus_gold"];
	        this.bonus_elixir = source["bonus_elixir"];
	        this.bonus_de = source["bonus_de"];
	        this.total_attacks_session = source["total_attacks_session"];
	    }
	}
	export class BotStats {
	    attacks_completed: number;
	    search_skips: number;
	    total_gold: number;
	    total_elixir: number;
	    total_de: number;
	    stars_0: number;
	    stars_1: number;
	    stars_2: number;
	    stars_3: number;
	    uptime: number;
	    adb_health: adb.Health;
	    cpu_time_sec: number;
	    cpu_cores: number;
	
	    static createFrom(source: any = {}) {
	        return new BotStats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.attacks_completed = source["attacks_completed"];
	        this.search_skips = source["search_skips"];
	        this.total_gold = source["total_gold"];
	        this.total_elixir = source["total_elixir"];
	        this.total_de = source["total_de"];
	        this.stars_0 = source["stars_0"];
	        this.stars_1 = source["stars_1"];
	        this.stars_2 = source["stars_2"];
	        this.stars_3 = source["stars_3"];
	        this.uptime = source["uptime"];
	        this.adb_health = this.convertValues(source["adb_health"], adb.Health);
	        this.cpu_time_sec = source["cpu_time_sec"];
	        this.cpu_cores = source["cpu_cores"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace config {
	
	export class Duration {
	    Duration: number;
	
	    static createFrom(source: any = {}) {
	        return new Duration(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Duration = source["Duration"];
	    }
	}
	export class AttackConfig {
	    enabled: boolean;
	    strategy_file: string;
	    attack_when_full: boolean;
	    max_attack_per_session: number;
	    drop_delay: Duration;
	    spell_delay: Duration;
	    end_battle_delay: Duration;
	    use_queen: boolean;
	    use_warden: boolean;
	    use_clan_castle: boolean;
	    queen_charge_at_pct: number;
	    warden_use_at_pct: number;
	    reserve_de_percent: number;
	    stall_timer_seconds: number;
	
	    static createFrom(source: any = {}) {
	        return new AttackConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.strategy_file = source["strategy_file"];
	        this.attack_when_full = source["attack_when_full"];
	        this.max_attack_per_session = source["max_attack_per_session"];
	        this.drop_delay = this.convertValues(source["drop_delay"], Duration);
	        this.spell_delay = this.convertValues(source["spell_delay"], Duration);
	        this.end_battle_delay = this.convertValues(source["end_battle_delay"], Duration);
	        this.use_queen = source["use_queen"];
	        this.use_warden = source["use_warden"];
	        this.use_clan_castle = source["use_clan_castle"];
	        this.queen_charge_at_pct = source["queen_charge_at_pct"];
	        this.warden_use_at_pct = source["warden_use_at_pct"];
	        this.reserve_de_percent = source["reserve_de_percent"];
	        this.stall_timer_seconds = source["stall_timer_seconds"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DebugConfig {
	    capture_debug: boolean;
	    save_screenshots: boolean;
	    template_debug: boolean;
	    state_debug: boolean;
	    use_shell_pipe: boolean;
	    shell_pipe_sync_flush: boolean;
	    jitter_taps: boolean;
	    jitter_delays: boolean;
	    max_jitter_pixels: number;
	    jitter_fraction: number;
	
	    static createFrom(source: any = {}) {
	        return new DebugConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.capture_debug = source["capture_debug"];
	        this.save_screenshots = source["save_screenshots"];
	        this.template_debug = source["template_debug"];
	        this.state_debug = source["state_debug"];
	        this.use_shell_pipe = source["use_shell_pipe"];
	        this.shell_pipe_sync_flush = source["shell_pipe_sync_flush"];
	        this.jitter_taps = source["jitter_taps"];
	        this.jitter_delays = source["jitter_delays"];
	        this.max_jitter_pixels = source["max_jitter_pixels"];
	        this.jitter_fraction = source["jitter_fraction"];
	    }
	}
	export class UpgradeConfig {
	    upgrade_walls: boolean;
	
	    static createFrom(source: any = {}) {
	        return new UpgradeConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.upgrade_walls = source["upgrade_walls"];
	    }
	}
	export class SearchConfig {
	    enabled: boolean;
	    min_trophies: number;
	    max_trophies: number;
	    min_town_hall: number;
	    max_town_hall: number;
	    skip_big_base: boolean;
	    skip_max_th: boolean;
	    attack_if_de_gt: number;
	    attack_if_trophies_gt: number;
	    min_loot_gold: number;
	    min_loot_elixir: number;
	    min_loot_de: number;
	
	    static createFrom(source: any = {}) {
	        return new SearchConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.min_trophies = source["min_trophies"];
	        this.max_trophies = source["max_trophies"];
	        this.min_town_hall = source["min_town_hall"];
	        this.max_town_hall = source["max_town_hall"];
	        this.skip_big_base = source["skip_big_base"];
	        this.skip_max_th = source["skip_max_th"];
	        this.attack_if_de_gt = source["attack_if_de_gt"];
	        this.attack_if_trophies_gt = source["attack_if_trophies_gt"];
	        this.min_loot_gold = source["min_loot_gold"];
	        this.min_loot_elixir = source["min_loot_elixir"];
	        this.min_loot_de = source["min_loot_de"];
	    }
	}
	export class TrainingConfig {
	    enabled: boolean;
	    full_army_before_attack: boolean;
	    train_dead_troops: boolean;
	    min_barracks_level: number;
	    sleep_after_train: Duration;
	
	    static createFrom(source: any = {}) {
	        return new TrainingConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.full_army_before_attack = source["full_army_before_attack"];
	        this.train_dead_troops = source["train_dead_troops"];
	        this.min_barracks_level = source["min_barracks_level"];
	        this.sleep_after_train = this.convertValues(source["sleep_after_train"], Duration);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DeviceConfig {
	    adb_host: string;
	    adb_port: number;
	    device_id: string;
	    package_name: string;
	    zoom_out_key: string;
	    zoom_in_key: string;
	    width: number;
	    height: number;
	    dpi: number;
	    restart_on_startup: boolean;
	    disable_chest_dismissal: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DeviceConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.adb_host = source["adb_host"];
	        this.adb_port = source["adb_port"];
	        this.device_id = source["device_id"];
	        this.package_name = source["package_name"];
	        this.zoom_out_key = source["zoom_out_key"];
	        this.zoom_in_key = source["zoom_in_key"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.dpi = source["dpi"];
	        this.restart_on_startup = source["restart_on_startup"];
	        this.disable_chest_dismissal = source["disable_chest_dismissal"];
	    }
	}
	export class BotConfig {
	    device: DeviceConfig;
	    training: TrainingConfig;
	    attack: AttackConfig;
	    search: SearchConfig;
	    upgrade: UpgradeConfig;
	    debug: DebugConfig;
	
	    static createFrom(source: any = {}) {
	        return new BotConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.device = this.convertValues(source["device"], DeviceConfig);
	        this.training = this.convertValues(source["training"], TrainingConfig);
	        this.attack = this.convertValues(source["attack"], AttackConfig);
	        this.search = this.convertValues(source["search"], SearchConfig);
	        this.upgrade = this.convertValues(source["upgrade"], UpgradeConfig);
	        this.debug = this.convertValues(source["debug"], DebugConfig);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	
	
	

}

export namespace main {
	
	export class BotStatus {
	    running: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new BotStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.message = source["message"];
	    }
	}

}

export namespace updater {
	
	export class Status {
	    current_version: string;
	    latest_version: string;
	    available: boolean;
	    state: string;
	    progress: number;
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
	
	    static createFrom(source: any = {}) {
	        return new Status(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.current_version = source["current_version"];
	        this.latest_version = source["latest_version"];
	        this.available = source["available"];
	        this.state = source["state"];
	        this.progress = source["progress"];
	        this.notes = source["notes"];
	        this.release_url = source["release_url"];
	        this.asset_name = source["asset_name"];
	        this.download_path = source["download_path"];
	        this.expected_size = source["expected_size"];
	        this.downloaded_size = source["downloaded_size"];
	        this.error = source["error"];
	        this.last_checked_unix = source["last_checked_unix"];
	        this.skip_version = source["skip_version"];
	        this.min_supported = source["min_supported"];
	    }
	}

}

