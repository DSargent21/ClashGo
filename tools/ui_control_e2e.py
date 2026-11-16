#!/usr/bin/env python3
"""Browse-mode UI control audit.

Drives every control rendered by the real React tree (Vite dev + the
wails-stub IPC shim) in headless Chromium, recording every App binding the
control fires and validating its name/arity/types against the generated
App.d.ts plus the Go validation ranges. Controls that legitimately do not call
Go (tabs, filters, clipboard, appearance settings) must instead change
observable UI/localStorage state.

One command: python3 tools/ui_control_e2e.py
"""

import json
import os
import re
import subprocess
import sys
import time
import urllib.request

from playwright.sync_api import sync_playwright

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
BASE = "http://localhost:5199/"
CHROME = os.path.expanduser(
    "~/Library/Caches/ms-playwright/chromium_headless_shell-1243/"
    "chrome-headless-shell-mac-arm64/chrome-headless-shell"
)

# ---------------------------------------------------------------------------
# IPC contract parsed from the generated bindings
# ---------------------------------------------------------------------------

DTS = os.path.join(ROOT, "web/wailsjs/go/main/App.d.ts")
SIG = {}
for line in open(DTS):
    m = re.match(r"export function (\w+)\(([^)]*)\)", line)
    if not m:
        continue
    args = []
    if m.group(2).strip():
        for a in m.group(2).split(","):
            t = a.split(":", 1)[1].strip()
            args.append(t)
    SIG[m.group(1)] = args


def check_payload(name, args):
    """Reject unknown bindings, wrong arity, wrong primitive types, bad ranges."""
    if name not in SIG:
        return f"unknown binding {name}"
    want = SIG[name]
    if len(args) != len(want):
        return f"{name}: {len(args)} args, generated binding takes {len(want)}"
    for i, (t, v) in enumerate(zip(want, args)):
        ok = {
            "boolean": isinstance(v, bool),
            "number": isinstance(v, (int, float)) and not isinstance(v, bool),
            "string": isinstance(v, str),
        }.get(t)
        if t.startswith("Array"):
            ok = isinstance(v, list)
        elif t.startswith("config.") or t.startswith("accounts.") or t.startswith("bot."):
            ok = isinstance(v, dict)
        if ok is False:
            return f"{name} arg{i + 1}: want {t}, got {type(v).__name__}={v!r}"

    def num(i):
        return args[i]

    if name == "StartBot":
        if not all(0 <= num(i) <= 10_000_000 for i in (0, 1, 2)):
            return f"StartBot floors out of range: {args[:3]}"
    if name in ("SaveLaunchSettings",):
        if not all(0 <= num(i) <= 10_000_000 for i in (0, 1, 2)):
            return f"{name} floors out of range: {args}"
    if name == "SaveConfig":
        if not all(0 <= num(i) <= 10_000_000 for i in (0, 1, 2)):
            return f"SaveConfig floors out of range: {args[:3]}"
        if not 10 <= num(6) <= 600:
            return f"SaveConfig stall out of range: {num(6)}"
    if name == "SetStorageConfig":
        rule = args[3]
        if rule not in ("ANY", "ALL", "ALL_3"):
            return f"SetStorageConfig rule {rule!r}"
        if not 50 <= num(4) <= 100:
            return f"SetStorageConfig full threshold {num(4)}"
        if not 0 <= num(5) < num(4):
            return f"SetStorageConfig resume {num(5)} vs full {num(4)}"
    if name == "SetScheduleConfig":
        for i in (1, 2):
            if not re.fullmatch(r"\d{2}:\d{2}", args[i]):
                return f"SetScheduleConfig hour {args[i]!r}"
        if not 10 <= num(3) <= 360:
            return f"SetScheduleConfig session {num(3)}"
        if not 5 <= num(5) <= 180:
            return f"SetScheduleConfig break {num(5)}"
        if not 1 <= num(8) <= 60 or not 1 <= num(9) <= 300:
            return f"SetScheduleConfig caps {args[8:10]}"
        if num(9) < num(8):
            return f"SetScheduleConfig daily cap below hourly: {args[8:10]}"
    if name == "SetStrategyArmySlot" and not 1 <= num(1) <= 4:
        return f"SetStrategyArmySlot slot {num(1)}"
    if name == "ChooseArmy":
        if not isinstance(args[0], str) or not args[0].endswith(".yaml"):
            return f"ChooseArmy file {args[0]!r}"
        if not 0 <= num(2) <= 4:
            return f"ChooseArmy slot {num(2)}"
    if name == "SetAutoUpgradeConfig":
        if args[2] not in (
            "strategic_rush", "max", "heroes_first", "cheapest_first",
            "most_expensive_first", "balanced",
        ):
            return f"SetAutoUpgradeConfig strategy {args[2]!r}"
        if args[12] not in ("cheapest_first", "troops_first", "spells_first", "siege_first"):
            return f"SetAutoUpgradeConfig lab {args[12]!r}"
        if args[16] not in ("gold", "elixir", "both"):
            return f"SetAutoUpgradeConfig wall {args[16]!r}"
        if not 0 <= num(1) <= 5:
            return f"SetAutoUpgradeConfig reserve {num(1)}"
    if name == "TestDeviceMenu" and args[0] not in ("open", "close", "filters"):
        return f"TestDeviceMenu action {args[0]!r}"
    if name == "SetAutoLoot" and not isinstance(args[0], bool):
        return "SetAutoLoot arg is not bool"
    return None


# ---------------------------------------------------------------------------
# Browser-side helpers
# ---------------------------------------------------------------------------

SCAN_JS = r"""
() => {
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none';
  };
  const labelOf = (el) => {
    if (el.getAttribute('aria-label')) return el.getAttribute('aria-label');
    if (el.id) {
      const l = document.querySelector(`label[for="${CSS.escape(el.id)}"]`);
      if (l && l.innerText.trim()) return l.innerText.trim();
    }
    const field = el.closest('.f-field, .set-row, .field-row');
    if (field) {
      const l = field.querySelector('label, .set-label b, .label');
      if (l && l.innerText.trim()) return l.innerText.trim();
    }
    if (el.placeholder) return 'placeholder:' + el.placeholder;
    return (el.innerText || el.value || '').trim().replace(/\s+/g, ' ').slice(0, 90);
  };
  const out = [];
  const counts = {};
  for (const el of document.querySelectorAll('button, input, select, textarea')) {
    if (!visible(el)) continue;
    const base = [el.tagName, el.type || '', el.getAttribute('aria-label') || '', labelOf(el)].join('|');
    const ordinal = counts[base] = (counts[base] || 0) + 1;
    out.push({
      key: base,
      ordinal,
      tag: el.tagName.toLowerCase(),
      type: el.type || '',
      label: labelOf(el),
      text: (el.innerText || '').trim().replace(/\s+/g, ' ').slice(0, 90),
      id: el.id || '',
      cls: (el.className || '').toString().slice(0, 80),
      disabled: !!el.disabled,
      checked: el.type === 'checkbox' ? !!el.checked : null,
      value: el.value === undefined ? null : el.value,
    });
  }
  return out;
}
"""

SIGNATURE_JS = r"""
() => {
  const ls = {};
  try { for (let i = 0; i < localStorage.length; i++) { const k = localStorage.key(i); ls[k] = localStorage.getItem(k); } } catch (e) {}
  const ctrls = [...document.querySelectorAll('button, input, select, textarea')].map((e) => [
    e.value || '', !!e.checked, e.getAttribute('aria-pressed'), e.getAttribute('aria-checked'), !!e.disabled,
  ]);
  return JSON.stringify({ ls, url: location.href, len: document.body.innerText.length, ctrls });
}
"""

DRIVE_JS = r"""
({ key, ordinal, action, value }) => {
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none';
  };
  const labelOf = (el) => {
    if (el.getAttribute('aria-label')) return el.getAttribute('aria-label');
    if (el.id) {
      const l = document.querySelector(`label[for="${CSS.escape(el.id)}"]`);
      if (l && l.innerText.trim()) return l.innerText.trim();
    }
    const field = el.closest('.f-field, .set-row, .field-row');
    if (field) {
      const l = field.querySelector('label, .set-label b, .label');
      if (l && l.innerText.trim()) return l.innerText.trim();
    }
    if (el.placeholder) return 'placeholder:' + el.placeholder;
    return (el.innerText || el.value || '').trim().replace(/\s+/g, ' ').slice(0, 90);
  };
  const counts = {};
  let target = null;
  for (const el of document.querySelectorAll('button, input, select, textarea')) {
    if (!visible(el)) continue;
    const base = [el.tagName, el.type || '', el.getAttribute('aria-label') || '', labelOf(el)].join('|');
    const ord = counts[base] = (counts[base] || 0) + 1;
    if (base === key && ord === ordinal) { target = el; break; }
  }
  if (!target) return { found: false };
  const before = window.__sig();
  window.__calls = [];
  window.__control = key;
  const disabled = !!target.disabled;
  if (disabled) target.disabled = false;
  if (action === 'value') {
    const proto = Object.getPrototypeOf(target);
    const setter = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
    if (setter) setter.call(target, String(value)); else target.value = String(value);
    target.dispatchEvent(new Event('input', { bubbles: true }));
    target.dispatchEvent(new Event('change', { bubbles: true }));
  } else if (action === 'select') {
    const opts = [...target.options];
    const chosen = opts.find((o) => o.value !== target.value) || opts[0];
    target.value = chosen.value;
    target.dispatchEvent(new Event('change', { bubbles: true }));
  } else {
    target.click();
  }
  return { found: true, before, disabled };
}
"""


# ---------------------------------------------------------------------------
# Servers / browser
# ---------------------------------------------------------------------------

def http_ok(url, timeout=1.0):
    # vite binds ::1 only; urllib may try 127.0.0.1 first for "localhost", and
    # an inherited proxy can answer instead of the dev server.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    for candidate in (url, url.replace("localhost", "[::1]")):
        try:
            with opener.open(candidate, timeout=timeout) as r:
                if r.status == 200:
                    return True
        except Exception:
            pass
    return False


def port_open():
    import socket
    for host in ("::1", "127.0.0.1"):
        try:
            with socket.create_connection((host, 5199), timeout=0.5):
                return True
        except OSError:
            pass
    return False


def ensure_vite():
    # The Python HTTP client gets a filtered 404 for this origin in some
    # environments; a TCP probe is enough, Chromium fetches the page itself.
    if port_open():
        return None
    log = open("/tmp/clashgo-ui-e2e-vite.log", "wb")
    proc = subprocess.Popen(
        ["npx", "vite", "--port", "5199", "--strictPort"],
        cwd=os.path.join(ROOT, "web"), stdout=log, stderr=subprocess.STDOUT,
    )
    for _ in range(120):
        if port_open():
            return proc
        time.sleep(0.25)
    proc.terminate()
    raise SystemExit("vite did not become ready on :5199")


# ---------------------------------------------------------------------------
# Surfaces
# ---------------------------------------------------------------------------

def app_page(browser, wizard=False):
    ctx = browser.new_context(viewport={"width": 1400, "height": 1000},
                              permissions=["clipboard-read", "clipboard-write"])
    page = ctx.new_page()
    page.add_init_script(
        r"""
        try {
          if (!sessionStorage.getItem('__e2e')) {
            sessionStorage.setItem('__e2e', '1');
          }
        } catch (e) {}
        window.__calls = [];
        const wrap = () => {
          const app = window.go && window.go.main && window.go.main.App;
          if (!app) return false;
          for (const k of Object.keys(app)) {
            if (typeof app[k] !== 'function' || app[k].__wrapped) continue;
            const orig = app[k];
            app[k] = function (...args) {
              window.__calls.push({ name: k, args: args });
              return orig.apply(this, args);
            };
            app[k].__wrapped = true;
          }
          window.__sig = () => {
            const ls = {};
            try { for (let i = 0; i < localStorage.length; i++) { const key = localStorage.key(i); ls[key] = localStorage.getItem(key); } } catch (e) {}
            const ctrls = [...document.querySelectorAll('button, input, select, textarea')].map((e) => [
              e.value || '', !!e.checked, e.getAttribute('aria-pressed'), e.getAttribute('aria-checked'), !!e.disabled,
            ]);
            return JSON.stringify({ ls, url: location.href, body: document.body.innerText, ctrls });
          };
          return true;
        };
        const t = setInterval(() => { if (wrap()) clearInterval(t); }, 5);
        """
    )
    page.on("pageerror", lambda e: ERRORS.append(f"pageerror: {e}"))
    page.on("console", lambda m: ERRORS.append(f"console.{m.type}: {m.text}")
            if m.type == "error" and "favicon" not in m.text else None)
    # Earlier surface crawls may leave "confirm before stopping" on; accept the
    # native confirm so the Stop control is exercised like a real user click.
    page.on("dialog", lambda d: d.accept())
    page.on("download", lambda d: DOWNLOADS.append(d))
    return ctx, page


ERRORS = []
DOWNLOADS = []


def load_app(page):
    page.goto(BASE, wait_until="domcontentloaded")
    page.evaluate("localStorage.setItem('launchSetupDone','1')")
    page.reload(wait_until="domcontentloaded")
    page.wait_for_timeout(700)


def load_wizard(page):
    page.goto(BASE, wait_until="domcontentloaded")
    page.evaluate("localStorage.removeItem('launchSetupDone')")
    page.reload(wait_until="domcontentloaded")
    page.wait_for_timeout(700)


def click_js(page, selector):
    page.evaluate(
        "(sel) => { const el = document.querySelector(sel); if (!el) throw new Error('missing ' + sel); el.click(); }",
        selector,
    )
    page.wait_for_timeout(300)


def click_text(page, text):
    page.evaluate(
        "(t) => { const b = [...document.querySelectorAll('button')].find(e => e.innerText.trim() === t); if (!b) throw new Error('no button ' + t); b.click(); }",
        text,
    )
    page.wait_for_timeout(300)


def nav_app(page, tab):
    click_js(page, f'button[aria-label="{tab}"]')
    page.wait_for_timeout(350)


def setup_surface(page, surface):
    if surface.startswith("wizard"):
        load_wizard(page)
        if surface in ("wizard-2", "wizard-3"):
            click_js(page, ".launch-plan")
            click_js(page, ".launch-cta")
        if surface == "wizard-3":
            click_js(page, ".launch-cta")
    else:
        load_app(page)
    if surface == "dashboard":
        nav_app(page, "Dashboard")
    elif surface == "army":
        nav_app(page, "Army")
    elif surface == "analytics":
        nav_app(page, "Analytics")
    elif surface == "accounts":
        nav_app(page, "Accounts")
    elif surface == "accounts-editor":
        nav_app(page, "Accounts")
        click_text(page, "Add account")
    elif surface.startswith("config-"):
        nav_app(page, "Config")
        tab = surface.split("-", 1)[1]
        click_text(page, tab)
    elif surface == "settings":
        nav_app(page, "Settings")


# Controls whose handler lives outside the stub/browser (external network,
# native window, device, real raid) or which need an unavailable environment.
HARDWARE_ONLY = {
    "Check now", "Download", "Install & restart", "Skip this version",
    "Open release page", "Install update", "Restart now",
}

# Selection groups: clicking the currently-selected member is a legitimate
# no-op, so drive a sibling first and then the target to force a transition.
GROUP_CLASSES = ("nav-btn", "launch-plan", "launch-choice", "launch-nav-item",
                 "option-card", "seg-btn", "slot-chip")

PRECONDITION_JS = r"""
({ key, ordinal, groups }) => {
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none';
  };
  const labelOf = (el) => {
    if (el.getAttribute('aria-label')) return el.getAttribute('aria-label');
    if (el.id) {
      const l = document.querySelector(`label[for="${CSS.escape(el.id)}"]`);
      if (l && l.innerText.trim()) return l.innerText.trim();
    }
    const field = el.closest('.f-field, .set-row, .field-row');
    if (field) {
      const l = field.querySelector('label, .set-label b, .label');
      if (l && l.innerText.trim()) return l.innerText.trim();
    }
    if (el.placeholder) return 'placeholder:' + el.placeholder;
    return (el.innerText || el.value || '').trim().replace(/\s+/g, ' ').slice(0, 90);
  };
  const counts = {};
  let target = null;
  for (const el of document.querySelectorAll('button, input, select, textarea')) {
    if (!visible(el)) continue;
    const base = [el.tagName, el.type || '', el.getAttribute('aria-label') || '', labelOf(el)].join('|');
    const ord = counts[base] = (counts[base] || 0) + 1;
    if (base === key && ord === ordinal) { target = el; break; }
  }
  if (!target) return false;
  for (const cls of groups) {
    if (!target.classList.contains(cls)) continue;
    const mate = [...document.querySelectorAll('.' + cls)].find((el) =>
      el !== target && !el.disabled && visible(el) && el.closest('body'));
    if (mate) { mate.click(); return true; }
  }
  return false;
}
"""

EXPECT = {
    ("dashboard", "Start bot"): ["StartBot"],
    ("dashboard", "Stop bot"): ["StopBot"],
    ("army", "Slot 1"): ["SetStrategyArmySlot"],
    ("army", "Slot 2"): ["SetStrategyArmySlot"],
    ("army", "Slot 3"): ["SetStrategyArmySlot"],
    ("army", "Slot 4"): ["SetStrategyArmySlot"],
    ("army", "Use this army"): ["SaveConfig"],
    ("accounts", "Refresh accounts"): ["GetAccounts"],
    ("config-Attack & loot", "Attack strategy"): ["SaveConfig"],
    ("config-Attack & loot", "Save settings"): ["SaveConfig", "SetStorageConfig", "SetScheduleConfig", "SetAutoUpgradeConfig"],
    ("config-Attack & loot", "Auto loot tuning"): ["SetAutoLoot"],
    ("config-Attack & loot", "Apply suggestion"): ["ApplyLootAutotune"],
    ("config-Attack & loot", "Reset history"): ["ResetLootAutotune"],
    ("config-Attack & loot", "Auto end battle at 50 percent"): ["SetStrategyAutoEnd"],
    ("config-Attack & loot", "Slot 1"): ["SetStrategyArmySlot"],
    ("config-Attack & loot", "Slot 2"): ["SetStrategyArmySlot"],
    ("config-Attack & loot", "Slot 3"): ["SetStrategyArmySlot"],
    ("config-Attack & loot", "Slot 4"): ["SetStrategyArmySlot"],
    ("settings", "Open builder menu"): ["TestDeviceMenu"],
    ("settings", "Open filters"): ["TestDeviceMenu"],
    ("settings", "Close menu"): ["TestDeviceMenu"],

    ("accounts", "Enable automatic account rotation"): ["SetRotation"],
    ("accounts", "Switch"): ["SwitchAccount"],
    ("accounts", "Switch & start"): ["SwitchAccount"],
    ("wizard-3", "Start with Auto EDrag Rush"): ["ChooseArmy", "SaveUpgradeSetup", "SaveLaunchSettings"],
}


# ---------------------------------------------------------------------------
# Driving
# ---------------------------------------------------------------------------

ALL_SURFACES = [
    "wizard-1", "wizard-2", "wizard-3",
    "dashboard", "army", "analytics", "accounts",
    "config-Attack & loot", "config-Builders", "config-Lab & pets",
    "config-Schedule", "config-Storage & walls",
    "settings", "accounts-editor",
]
RAW_ARGS = sys.argv[1:]
SCEN_ONLY = "--scenarios" in RAW_ARGS
SURFACES = [] if SCEN_ONLY else [s for s in ALL_SURFACES if not RAW_ARGS or s in RAW_ARGS]
RUN_SCENARIOS = SCEN_ONLY or not RAW_ARGS

results = []
skipped_hw = []


def drive_one(page, surface, ctrl):
    key, ordinal = ctrl["key"], ctrl["ordinal"]
    label = ctrl["label"] or ctrl["text"] or ctrl["key"]
    if label in HARDWARE_ONLY:
        skipped_hw.append((surface, label))
        return None

    if ctrl["tag"] == "textarea":
        action = "value"
        val = "e2e note"
    elif ctrl["tag"] == "input" and ctrl["type"] in ("number", "range", "text", "time"):
        if ctrl["type"] == "text":
            val = "e2e-check"
        elif ctrl["type"] == "time":
            val = "12:00"
        else:
            try:
                cur = int(float(ctrl["value"] or 0))
            except ValueError:
                cur = 0
            val = max(1, min(9_000_000, cur + 1000))
        action = "value"
    elif ctrl["tag"] == "input" and ctrl["type"] in ("checkbox",):
        action = "click"
    elif ctrl["tag"] == "select":
        action = "select"
    else:
        action = "click"

    page.evaluate(PRECONDITION_JS, {"key": key, "ordinal": ordinal, "groups": list(GROUP_CLASSES)})
    page.wait_for_timeout(160)
    downloads_before = len(DOWNLOADS)
    page.evaluate("window.__calls = []")
    r = page.evaluate(DRIVE_JS, {"key": key, "ordinal": ordinal, "action": action,
                                 "value": val if action == "value" else ""})
    if not r.get("found"):
        return {"status": "missing", "label": label, "calls": [], "effect": None,
                "disabled": ctrl["disabled"], "action": action}
    page.wait_for_timeout(220)
    calls = page.evaluate("window.__calls || []")
    after = page.evaluate("window.__sig()")
    effect = after != r["before"] or len(DOWNLOADS) > downloads_before
    return {"status": "driven", "label": label, "calls": calls, "effect": effect,
            "disabled": r.get("disabled", False), "action": action}


def main():
    proc = ensure_vite()
    failures = []
    scenarios = []
    try:
        with sync_playwright() as p:
            browser = p.chromium.launch(executable_path=CHROME, headless=True)
            ctx, page = app_page(browser)
            ctx2, wpage = app_page(browser, wizard=True)

            for surface in SURFACES:
                pg = wpage if surface.startswith("wizard") else page
                setup_surface(pg, surface)
                controls = pg.evaluate(SCAN_JS)
                if surface != "dashboard":
                    controls = [c for c in controls
                                if "nav-btn" not in c["cls"]
                                and c["label"] not in (
                                    "Start bot", "Stop bot", "Collapse sidebar", "Auto",
                                    "ALL", "DEBUG", "INFO", "SUCCESS", "WARN", "ERROR",
                                    "Copy log line", "Export logs", "Filter logs by text")]
                if not controls:
                    failures.append(f"{surface}: enumerated 0 controls")
                    continue
                for ctrl in controls:
                    setup_surface(pg, surface)
                    fresh = pg.evaluate(SCAN_JS)
                    match = [c for c in fresh if c["key"] == ctrl["key"] and c["ordinal"] == ctrl["ordinal"]]
                    if not match:
                        failures.append(f"{surface}: control disappeared after reset: {ctrl['key']} #{ctrl['ordinal']}")
                        continue
                    out = drive_one(pg, surface, match[0])
                    if out is None:
                        continue
                    label = out["label"]
                    calls = out["calls"]
                    effect = out["effect"]
                    disabled = out["disabled"]
                    action = out["action"]
                    if out["status"] == "missing":
                        failures.append(f"{surface}: {label}: target not found when driving")
                        continue
                    names = [c["name"] for c in calls]
                    for c in calls:
                        err = check_payload(c["name"], c["args"])
                        if err:
                            failures.append(f"{surface}: {label}: payload {err}")
                    expect = EXPECT.get((surface, label))
                    if expect:
                        for name in expect:
                            if name not in names:
                                failures.append(f"{surface}: {label}: expected {name}, fired {names}")
                    elif not names and not effect:
                        failures.append(f"{surface}: {label}: inert (no IPC, no state change)")
                    results.append({
                        "surface": surface, "label": label, "tag": ctrl["tag"],
                        "type": ctrl["type"], "calls": names, "effect": effect,
                        "disabled": disabled, "action": action,
                    })

            if RUN_SCENARIOS:
                setup_surface(page, "config-Attack & loot")
                page.evaluate("window.__calls = []")
                page.locator("input[type=number]").first.fill("123000")
                click_text(page, "Save settings")
                page.wait_for_timeout(700)
                calls = page.evaluate("window.__calls")
                save = next((c for c in calls if c["name"] == "SaveConfig"), None)
                if not save:
                    failures.append("scenario: Save settings did not call SaveConfig")
                elif save["args"][0] != 123000:
                    failures.append(f"scenario: SaveConfig gold = {save['args'][0]}, want 123000")
                scenarios.append(("config-save", len(calls)))

                setup_surface(page, "dashboard")
                page.evaluate("window.__calls = []")
                click_js(page, 'button[aria-label="Start bot"]')
                page.wait_for_timeout(500)
                calls = page.evaluate("window.__calls")
                if "StartBot" not in [c["name"] for c in calls]:
                    failures.append("scenario: Start bot did not call StartBot")
                scenarios.append(("start", len(calls)))

                page.evaluate("window.__calls = []")
                click_js(page, 'button[aria-label="Stop bot"]')
                page.wait_for_timeout(500)
                calls = page.evaluate("window.__calls")
                if "StopBot" not in [c["name"] for c in calls]:
                    failures.append("scenario: Stop bot did not call StopBot")
                scenarios.append(("stop", len(calls)))

                setup_surface(page, "accounts-editor")
                page.evaluate("window.__calls = []")
                page.locator("form input").first.fill("E2E account")
                click_js(page, 'button[type="submit"]')
                page.wait_for_timeout(500)
                calls = page.evaluate("window.__calls")
                names = [c["name"] for c in calls]
                if "CreateAccount" not in names:
                    failures.append(f"scenario: account editor Save fired {names}, want CreateAccount")
                scenarios.append(("account-create", len(calls)))

                setup_surface(page, "accounts")
                page.evaluate("window.__calls = []")
                page.evaluate("() => { const b = [...document.querySelectorAll('button')].find(e => (e.getAttribute('aria-label')||'').startsWith('Delete Alt')); if (!b) throw new Error('no delete'); b.click(); }")
                page.wait_for_timeout(300)
                page.evaluate("() => { const b = [...document.querySelectorAll('button')].find(e => (e.getAttribute('aria-label')||'').startsWith('Confirm delete Alt')); if (!b) throw new Error('no confirm delete'); b.click(); }")
                page.wait_for_timeout(500)
                calls = page.evaluate("window.__calls")
                if "DeleteAccount" not in [c["name"] for c in calls]:
                    failures.append(f"scenario: confirm delete fired {[c['name'] for c in calls]}, want DeleteAccount")
                scenarios.append(("account-delete", len(calls)))

                setup_surface(page, "config-Lab & pets")
                page.evaluate("window.__calls = []")
                click_js(page, 'button[aria-label="Move Phoenix down"]')
                page.evaluate("() => { const b = [...document.querySelectorAll('[role=switch]')].find(e => (e.getAttribute('aria-label')||'').includes('pet upgrades')); if (b && b.getAttribute('aria-checked') !== 'true') b.click(); }")
                click_text(page, "Save settings")
                page.wait_for_timeout(700)
                calls = page.evaluate("window.__calls")
                auto = next((c for c in calls if c["name"] == "SetAutoUpgradeConfig"), None)
                if not auto:
                    failures.append("scenario: pet reorder+save did not call SetAutoUpgradeConfig")
                else:
                    pri = auto["args"][15]
                    want = ["Diggy", "Unicorn", "Frosty", "Spirit Fox", "Phoenix"]
                    if not isinstance(pri, list) or pri[:5] != want:
                        failures.append(f"scenario: pet priorities saved as {pri[:5]}, want {want}")
                    if auto["args"][14] is not True:
                        failures.append("scenario: auto_pets not saved as true")
                scenarios.append(("pet-reorder-save", len(calls)))

                setup_surface(page, "config-Builders")
                page.evaluate("window.__calls = []")
                page.evaluate("() => { const b = [...document.querySelectorAll('button[role=switch],button')].find(e => (e.getAttribute('aria-label')||'').includes('automatic builders')); if (!b) throw new Error('no auto builders'); b.click(); }")
                click_text(page, "Save settings")
                page.wait_for_timeout(700)
                calls = page.evaluate("window.__calls")
                auto = next((c for c in calls if c["name"] == "SetAutoUpgradeConfig"), None)
                if not auto:
                    failures.append("scenario: builders toggle+save did not call SetAutoUpgradeConfig")
                elif auto["args"][0] is not True:
                    failures.append(f"scenario: auto_builders saved as {auto['args'][0]!r}, want true")
                scenarios.append(("builder-toggle-save", len(calls)))

                setup_surface(page, "settings")
                for label, binding, key, selector in (
                    ("Reset all statistics", "ResetStats", "reset-stats", ".reset-btn:not(.secondary)"),
                ):
                    page.evaluate("window.__calls = []")
                    for _ in range(2):
                        page.evaluate("(s) => { const b = document.querySelector(s); if (!b) throw new Error('no ' + s); b.click(); }", selector)
                        page.wait_for_timeout(300)
                    calls = page.evaluate("window.__calls")
                    if binding not in [c["name"] for c in calls]:
                        failures.append(f"scenario: {label} fired {[c['name'] for c in calls]}, want {binding}")
                    scenarios.append((key, len(calls)))

            browser.close()
    finally:
        if proc:
            proc.terminate()

    total = len(results)
    by_surface = {}
    for r in results:
        by_surface[r["surface"]] = by_surface.get(r["surface"], 0) + 1
    print("UI CONTROL E2E")
    print(f"controls driven: {total}")
    for s in SURFACES:
        print(f"  {s}: {by_surface.get(s, 0)}")
    with_calls = sum(1 for r in results if r["calls"])
    print(f"controls that fired IPC: {with_calls}")
    print(f"local/effect-only controls: {total - with_calls}")
    print(f"scenarios: {scenarios}")
    hw_notes = [
        ("app", "real Wails WKWebView window (not reachable from a browser page)"),
        ("device", "live ADB taps / device menu test"),
        ("updates", "GitHub update download and relaunch (Check now outer loop skipped)"),
        ("raid", "real in-game raid (needs a live account and device)"),
    ]
    print(f"hardware-only skipped: {len(skipped_hw) + len(hw_notes)}")
    for s, label in sorted(set(skipped_hw)):
        print(f"  skip {s}: {label}")
    for s, label in hw_notes:
        print(f"  skip {s}: {label}")
    print(f"page errors: {len(ERRORS)}")
    for e in ERRORS[:10]:
        print("  " + e)
    if failures:
        print(f"FAILURES: {len(failures)}")
        for f in failures:
            print("  - " + f)
        sys.exit(1)
    print("PASS")


if __name__ == "__main__":
    main()
