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
	
	    static createFrom(source: any = {}) {
	        return new DebugConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.capture_debug = source["capture_debug"];
	        this.save_screenshots = source["save_screenshots"];
	        this.template_debug = source["template_debug"];
	        this.state_debug = source["state_debug"];
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

