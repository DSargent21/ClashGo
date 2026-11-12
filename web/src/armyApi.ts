// Typed wrapper for main.App.GetArmyPlans. Kept outside web/wailsjs
// because the generated tree predates the method and is regenerated as
// a whole; method names must match app_army.go exactly.
export interface ArmyPlanUnit {
  name: string;
  amount: string;
}

export interface ArmyPlanPhase {
  name: string;
  units: ArmyPlanUnit[];
}

export interface ArmyPlan {
  file: string;
  name: string;
  description: string;
  emblem: string;
  target_edge: string;
  army_slot: number;
  end_at_percent: number;
  active: boolean;
  phases: ArmyPlanPhase[];
}

type GoApp = Record<string, (...args: unknown[]) => Promise<unknown>>;

function goApp(): GoApp {
  const bridge = window as unknown as { go?: { main?: { App?: GoApp } } };
  const app = bridge.go?.main?.App;
  if (!app) {
    throw new Error('Wails bridge unavailable — run the desktop app to read army plans');
  }
  return app;
}

function call<T>(method: string, ...args: unknown[]): Promise<T> {
  const app = goApp();
  const fn = app[method];
  if (typeof fn !== 'function') {
    return Promise.reject(new Error(`Method ${method} is not exposed by this build`));
  }
  return fn(...args) as Promise<T>;
}

export const getArmyPlans = () => call<ArmyPlan[]>('GetArmyPlans');
