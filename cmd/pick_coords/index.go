package main

// indexHTML is the whole picker UI. It is a plain string (no build step, no
// template literals) so the tool stays a single self-contained binary — the
// point of the tool is that using it is faster than changing the bot.
const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>ClashGO deploy-point picker</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body { margin: 0; background: #0e1116; color: #e8ecf1;
         font: 13px/1.45 -apple-system, system-ui, "Segoe UI", sans-serif; }
  header { display: flex; gap: 14px; align-items: center; flex-wrap: wrap;
           padding: 10px 14px; background: #151a22; border-bottom: 1px solid #263040;
           position: sticky; top: 0; z-index: 5; }
  header b { color: #6cc7ff; font-size: 14px; }
  .grow { flex: 1 1 auto; }
  button { background: #223047; color: #dce6f2; border: 1px solid #33455f;
           border-radius: 6px; padding: 6px 11px; font: inherit; cursor: pointer; }
  button:hover { background: #2b3c58; }
  button.primary { background: #1d6f3f; border-color: #2c8d54; }
  button.primary:disabled { background: #223047; border-color: #33455f;
                            color: #6b7887; cursor: default; }
  button.armed { background: #7a5a12; border-color: #b98a1d; color: #ffe9b0; }
  .tab { padding: 6px 12px; border-radius: 999px; }
  .tab.on { background: #2b3c58; border-color: #6cc7ff; color: #cfe9ff; }
  .tab .dot { display: inline-block; width: 7px; height: 7px; border-radius: 50%;
              background: #3c4a5e; margin-right: 6px; vertical-align: middle; }
  .tab .dot.set { background: #46d17a; }
  main { padding: 12px 14px 40px; display: grid; gap: 14px;
         grid-template-columns: minmax(0, 1fr) 320px; align-items: start; }
  @media (max-width: 900px) { main { grid-template-columns: 1fr; } }
  .stage { position: relative; background: #000; border: 1px solid #263040;
           border-radius: 8px; overflow: hidden; }
  .stage img { display: block; width: 100%; height: auto; user-select: none; }
  .stage svg { position: absolute; inset: 0; width: 100%; height: 100%; }
  .stage.picking { cursor: crosshair; }
  .panel { background: #151a22; border: 1px solid #263040; border-radius: 8px; padding: 10px; }
  .panel h3 { margin: 0 0 8px; font-size: 12px; letter-spacing: .06em;
              text-transform: uppercase; color: #8ea2ba; }
  .row { display: flex; align-items: center; gap: 8px; padding: 7px 8px;
         border-radius: 6px; border: 1px solid transparent; cursor: pointer; }
  .row:hover { background: #1b2230; }
  .row.on { background: #241f10; border-color: #b98a1d; }
  .row .name { flex: 1 1 auto; }
  .row .val { color: #9fb0c6; font-variant-numeric: tabular-nums; font-size: 12px; }
  .swatch { width: 10px; height: 10px; border-radius: 2px; }
  .hint { color: #8ea2ba; font-size: 12px; }
  .hint code { color: #cfe0f2; background: #1b2230; padding: 1px 4px; border-radius: 3px; }
  .readout { position: absolute; right: 8px; bottom: 8px; background: rgba(10,14,20,.85);
             border: 1px solid #33455f; border-radius: 6px; padding: 3px 7px;
             font-variant-numeric: tabular-nums; font-size: 12px; }
  .warn { font-size: 12px; color: #ffb4b4; }
  .tab .badge { display: inline-block; margin-left: 6px; padding: 0 5px; border-radius: 8px;
    background: #7a1f1f; color: #ffd7d7; font-size: 11px; }
  .warn ul { margin: 0; padding-left: 16px; }
  .warn .ok { color: #9fe8b0; }
  .toast { position: fixed; left: 50%; transform: translateX(-50%); bottom: 18px;
           background: #16351f; border: 1px solid #2c8d54; color: #d6ffe4;
           padding: 9px 14px; border-radius: 8px; max-width: 90vw; display: none; }
  .toast.err { background: #3a1a1c; border-color: #9c3038; color: #ffdcdc; }
  kbd { background: #1b2230; border: 1px solid #33455f; border-bottom-width: 2px;
        border-radius: 4px; padding: 0 4px; font-size: 11px; }
</style>
</head>
<body>
<header>
  <b>Deploy-point picker</b>
  <span class="hint" id="cfg"></span>
  <span class="grow"></span>
  <span class="hint" id="dims"></span>
  <button id="refresh" title="Grab a fresh frame from the emulator">Refresh frame</button>
  <label class="hint" style="display:flex;gap:5px;align-items:center">
    <input type="checkbox" id="auto" checked> live view
  </label>
  <button id="ring" title="Fill a safe outer-ring plan for all four sides">Fill standard ring</button>
  <button id="clearside" title="Remove everything pinned for the side you are on">Clear this side</button>
  <button id="reload">Reload saved</button>
  <button id="save" class="primary" disabled>Save to config</button>
</header>

<main>
  <section>
    <div class="warn" id="verdict" style="margin-bottom:8px"></div>
    <div style="display:flex;gap:8px;flex-wrap:wrap;margin-bottom:10px" id="tabs"></div>
    <div class="stage picking" id="stage">
      <img id="shot" alt="emulator frame">
      <svg id="overlay"></svg>
      <div class="readout" id="readout">-</div>
    </div>
    <p class="hint" id="howto" style="margin:10px 0 0"></p>
  </section>

  <aside class="panel">
    <h3>First time? One click</h3>
    <p class="hint" style="margin:0 0 10px">
      Press <b>Fill standard ring</b> above, then <b>Save to config</b>. That
      gives all four sides a safe plan (outer ring, never in the base). Refine
      any side later by clicking the frame.
    </p>
    <h3>What to place</h3>
    <div id="rows"></div>
    <h3 style="margin-top:12px">Checks <span class="hint" id="warncount"></span></h3>
    <div class="warn" id="warns"></div>
    <h3 style="margin-top:12px">Rules of thumb</h3>
    <p class="hint" style="margin:0">
      Stay in the outer ring: the base never reaches the screen edge, so ground
      within ~130&nbsp;px of a border is deployable on every base. The shaded
      band shows it. If a tap is refused the game says
      <em>“You cannot deploy troops on the Red area!”</em> — that point is
      inside the base's no-deploy pocket; move it outward and re-save.
    </p>
    <p class="hint" style="margin:8px 0 0">
      Save writes the <code>width</code>/<code>height</code> reference geometry,
      not the live pixels: the numbers on screen are the taps you get.
    </p>
  </aside>
</main>

<div class="toast" id="toast"></div>

<script>
const SIDES = ['TopLeft', 'TopRight', 'BottomLeft', 'BottomRight'];
const KINDS = [
  { key: 'troop_line',  label: 'Troop line',     need: 2, color: '#6cc7ff', deploy: true,
    hint: 'p1 → p2. Every troop tap (Balloons, EDrags, …) lands on this line.' },
  { key: 'spell_a',     label: 'Spell line A',   need: 2, color: '#e26cf7',
    hint: 'p1 → p2 for the first spell line (rage).' },
  { key: 'spell_b',     label: 'Spell line B',   need: 2, color: '#f7a45b',
    hint: 'p1 → p2 for the second spell (ice).' },
  { key: 'hero',        label: 'Hero point',     need: 1, color: '#5be27a', deploy: true,
    hint: 'Where heroes drop; the bot snaps it onto the troop line.' },
  { key: 'spell_point', label: 'Spell point',    need: 1, color: '#ffe066',
    hint: 'Single-point spell target.' }
];

let state = null;           // last /state payload
let draft = null;           // live-pixel edits (what Save sends)
let side = 'TopLeft';
let active = null;          // {key, step}
let dirty = false;
let liveW = 0, liveH = 0;

const $ = (id) => document.getElementById(id);
const toast = (msg, err) => {
  const t = $('toast');
  t.textContent = msg; t.className = 'toast' + (err ? ' err' : ''); t.style.display = 'block';
  clearTimeout(toast._t);
  toast._t = setTimeout(() => { t.style.display = 'none'; }, err ? 9000 : 3500);
};

function clone(o) { return JSON.parse(JSON.stringify(o)); }

function blankItems() {
  return { troop_line: {}, spell_a: {}, spell_b: {}, hero: {}, spell_point: {} };
}

async function loadState() {
  const r = await fetch('/state', { cache: 'no-store' });
  state = await r.json();
  draft = clone(state.items || blankItems());
  dirty = false;
  if (state.live && state.live.w) { liveW = state.live.w; liveH = state.live.h; }
  render();
  if (state.note) toast(state.note, true);
}

function refreshFrame() {
  const img = $('shot');
  img.src = '/frame.png?t=' + Date.now();
}

$('shot').addEventListener('load', () => {
  const img = $('shot');
  liveW = img.naturalWidth; liveH = img.naturalHeight;
  if (state) { state.live = { w: liveW, h: liveH }; }
  render();
});
$('shot').addEventListener('error', () => toast('frame capture failed — is CoC on screen?', true));

// ---------------------------------------------------------------- saving

async function save() {
  if (!liveW) { toast('refresh the frame first', true); return; }
  const r = await fetch('/save', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(draft)
  });
  if (!r.ok) { toast('save failed: ' + (await r.text()), true); return; }
  const res = await r.json();
  dirty = false;
  render();
  toast('saved ' + res.sides.length + ' side(s) to ' + res.written +
        ' — as ' + res.stored_pixels + '. The bot reloads config within ~2 s.');
}

// ---------------------------------------------------------------- render

function sideHasData(s) {
  return Object.keys(draft.troop_line).includes(s) ||
         Object.keys(draft.spell_a).includes(s) ||
         Object.keys(draft.spell_b).includes(s) ||
         Object.keys(draft.hero).includes(s) ||
         Object.keys(draft.spell_point).includes(s);
}

function render() {
  $('cfg').textContent = state ? state.config_path : '';
  $('dims').textContent = liveW ? (liveW + 'x' + liveH + ' live / ' +
      state.ref.w + 'x' + state.ref.h + ' stored') : 'no frame yet';
  $('save').disabled = !dirty;

  const tabs = $('tabs');
  tabs.innerHTML = '';
  SIDES.forEach((s) => {
    const b = document.createElement('button');
    b.className = 'tab' + (s === side ? ' on' : '');
    const n = summarizeSide(s).bad;
    b.innerHTML = '<span class="dot' + (sideHasData(s) ? ' set' : '') + '"></span>' + s +
      (n ? '<span class="badge" title="' + n + ' point(s) this side need a look">' + n + '</span>' : '');
    b.onclick = () => { side = s; active = null; render(); };
    b.onmouseenter = () => { previewSide = s; };
    tabs.appendChild(b);
  });
  const badSides = SIDES.filter((s) => summarizeSide(s).bad > 0);
  $('verdict').innerHTML = badSides.length
    ? 'Attention: ' + badSides.join(', ') + ' — see Checks below.'
    : '<span class="ok">All four sides look good: every point is on the field and close enough to an edge.</span>';

  const rows = $('rows');
  rows.innerHTML = '';
  KINDS.forEach((k, i) => {
    const row = document.createElement('div');
    row.className = 'row' + (active && active.key === k.key ? ' on' : '');
    const val = describe(k.key, side);
    row.innerHTML = '<span class="swatch" style="background:' + k.color + '"></span>' +
      '<span class="name">' + (i + 1) + '. ' + k.label + '</span>' +
      '<span class="val">' + val + '</span>';
    row.onclick = () => arm(k);
    rows.appendChild(row);
  });

  const warnRows = summarizeSide(side).rows;
  $('warns').innerHTML = warnRows.length
    ? '<ul><li>' + warnRows.join('</li><li>') + '</li></ul>'
    : '<span class="ok">No problems on ' + side + ': every point is inside the deploy band and close enough to an edge.</span>';
  $('warncount').textContent = warnRows.length ? '(' + warnRows.length + ')' : '(all clear)';

  const k = KINDS.find((x) => active && x.key === active.key);
  $('howto').innerHTML = k
    ? ('<b style="color:' + k.color + '">' + k.label + '</b> — click ' +
       (k.need === 2 ? (active.step === 0 ? 'the FIRST end' : 'the SECOND end') : 'the point') +
       ' on the frame for <b>' + side + '</b>. <kbd>Esc</kbd> cancels. ' +
       k.hint)
    : ('Click a tool on the right (or press <kbd>1</kbd>–<kbd>5</kbd>), then click the frame ' +
       'to place it for <b>' + side + '</b>.');

  draw();
}

function describe(key, s) {
  if (key === 'hero' || key === 'spell_point') {
    const m = draft[key][s];
    return m ? (m.x + ',' + m.y) : '—';
  }
  const e = draft[key][s];
  return e ? (e.p1.x + ',' + e.p1.y + ' → ' + e.p2.x + ',' + e.p2.y) : '—';
}

function arm(k) {
  if (active && active.key === k.key) { active = null; }      // toggle off
  else { active = { key: k.key, step: 0 }; }
  render();
}

// ---------------------------------------------------------------- SVG

const NS = 'http://www.w3.org/2000/svg';
function el(tag, attrs) {
  const n = document.createElementNS(NS, tag);
  for (const a in attrs) n.setAttribute(a, attrs[a]);
  return n;
}

// linspace of the bot's actual taps along a line, so the dots on screen are the
// taps that will be fired (lineFromPoints uses 15).
function linePoints(p1, p2, n) {
  const out = [];
  for (let i = 0; i < n; i++) {
    const t = n > 1 ? i / (n - 1) : 0.5;
    out.push({ x: Math.round(p1.x + t * (p2.x - p1.x)), y: Math.round(p1.y + t * (p2.y - p1.y)) });
  }
  return out;
}

function draw() {
  const svg = $('overlay');
  svg.innerHTML = '';
  if (!liveW) return;
  svg.setAttribute('viewBox', '0 0 ' + liveW + ' ' + liveH);

  // Outer always-deployable ring + rule-of-thirds-ish guides.
  svg.appendChild(el('rect', { x: 130, y: 130, width: Math.max(0, liveW - 260),
    height: Math.max(0, liveH - 300), fill: 'rgba(255,80,80,0.05)',
    stroke: 'rgba(255,120,120,0.35)', 'stroke-width': 2, 'stroke-dasharray': '8 6' }));
  svg.appendChild(el('rect', { x: 0, y: 0, width: liveW, height: liveH,
    fill: 'none', stroke: 'rgba(108,199,255,0.25)', 'stroke-width': 2 }));

  KINDS.forEach((k) => {
    const color = k.color;
    if (k.key === 'hero' || k.key === 'spell_point') {
      const p = draft[k.key][side];
      if (!p) return;
      if (pointIssues(p, k.deploy).length) {
        svg.appendChild(el('circle', { cx: p.x, cy: p.y, r: 20, fill: 'none',
          stroke: '#ff4d4d', 'stroke-width': 3, 'stroke-dasharray': '4 3' }));
      }
      svg.appendChild(el('circle', { cx: p.x, cy: p.y, r: 12, fill: color,
        'fill-opacity': 0.25, stroke: color, 'stroke-width': 3 }));
      svg.appendChild(el('text', { x: p.x + 18, y: p.y + 5, fill: color,
        'font-size': 15, 'font-weight': 600 }));
      svg.lastChild.textContent = k.label;
      handle(k.key, side, 'p', p, color);
      return;
    }
    const e = draft[k.key][side];
    if (!e) return;
    svg.appendChild(el('line', { x1: e.p1.x, y1: e.p1.y, x2: e.p2.x, y2: e.p2.y,
      stroke: color, 'stroke-width': 3, 'stroke-dasharray': '6 4' }));
    linePoints(e.p1, e.p2, 15).forEach((p) => {
      svg.appendChild(el('circle', { cx: p.x, cy: p.y, r: 5, fill: color }));
    });
    handle(k.key, side, 'p1', e.p1, color);
    handle(k.key, side, 'p2', e.p2, color);
    [e.p1, e.p2].forEach((p) => {
      if (!pointIssues(p, k.deploy).length) return;
      svg.appendChild(el('circle', { cx: p.x, cy: p.y, r: 22, fill: 'none',
        stroke: '#ff4d4d', 'stroke-width': 3, 'stroke-dasharray': '4 3' }));
    });
  });
}

function handle(kind, s, which, p, color) {
  const c = el('circle', { cx: p.x, cy: p.y, r: 13, fill: 'rgba(0,0,0,0.35)',
    stroke: color, 'stroke-width': 4, style: 'cursor:grab' });
  c.addEventListener('mousedown', (ev) => startDrag(ev, kind, s, which));
  $('overlay').appendChild(c);
}

// ---------------------------------------------------------------- picking

let previewSide = null;

function startDrag(ev, kind, s, which) {
  ev.preventDefault();
  const move = (e) => {
    const p = toImage(e);
    if (!p) return;
    if (kind === 'hero' || kind === 'spell_point') draft[kind][s] = p;
    else draft[kind][s][which] = p;
    dirty = true;
    draw();
    $('save').disabled = false;
  };
  const up = () => { window.removeEventListener('mousemove', move);
                     window.removeEventListener('mouseup', up); };
  window.addEventListener('mousemove', move);
  window.addEventListener('mouseup', up);
}

function toImage(e) {
  const img = $('shot');
  const r = img.getBoundingClientRect();
  if (!r.width) return null;
  const x = Math.round((e.clientX - r.left) * liveW / r.width);
  const y = Math.round((e.clientY - r.top) * liveH / r.height);
  return { x: Math.max(0, Math.min(liveW, x)), y: Math.max(0, Math.min(liveH, y)) };
}

$('stage').addEventListener('mousemove', (e) => {
  const p = toImage(e);
  if (p) $('readout').textContent = p.x + ', ' + p.y;
});

$('stage').addEventListener('click', (e) => {
  if (!active || !liveW) return;
  const p = toImage(e);
  if (!p) return;
  const k = KINDS.find((x) => x.key === active.key);
  if (k.need === 1) {
    draft[k.key][side] = p;
    active = null;
  } else {
    if (active.step === 0) {
      draft[k.key][side] = { p1: p, p2: p };
      active.step = 1;
    } else {
      draft[k.key][side].p2 = p;
      active = null;
    }
  }
  dirty = true;
  render();
});

document.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') { active = null; render(); return; }
  if (e.key >= '1' && e.key <= '5') { arm(KINDS[parseInt(e.key, 10) - 1]); return; }
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
    e.preventDefault();
    if (dirty) save();
  }
});

// ---------------------------------------------------------------- sanity check

// The bot's own deploy band at 720p: yTopMin (110) to 0.85 * height (612). Above
// that is the top resource HUD and below it is the troop bar; a tap there is
// thrown away (or hits UI). "Inland" matters for a different reason: the base's
// no-deploy pocket is in the middle of the field, and that is the ground the game
// refuses with "You cannot deploy troops on the Red area!" — the outer ring is
// the safe part. These are warnings, not rules: the tool will happily save a
// point you insist on.
// Matches the bot's ClampToDeployBand(): yTopMin (110) plus a small inset so a
// point is clear of the HUD's last row. Anything the tool calls safe here, the
// bot leaves exactly where you put it.
const BAND_TOP = 130;
const BAND_BOTTOM_RATIO = 0.85;
const INLAND_LIMIT = 170;

function edgeDistance(p) {
  return Math.min(p.x, p.y, liveW - p.x, liveH - p.y);
}

function pointIssues(p, deploy) {
  const out = [];
  if (p.y < BAND_TOP) out.push('in the top HUD');
  if (p.y > BAND_BOTTOM_RATIO * liveH - 12) out.push('on the troop bar');
  // The red "no deploy" area only restricts things you place ON the field.
  // Spells are thrown at the base - targeting inland is the whole point of a
  // spell line, so an inland spell is not a problem and must not be flagged.
  if (deploy && edgeDistance(p) > INLAND_LIMIT) {
    out.push('inland (' + edgeDistance(p) + 'px from the nearest edge - the base\'s no-deploy pocket lives here)');
  }
  return out;
}

function collectIssues(whichSide) {
  const rows = [];
  if (!liveW) return rows;
  const add = (label, p, deploy) => {
    const i = pointIssues(p, deploy);
    if (i.length) rows.push(label + ' at ' + p.x + ',' + p.y + ' is ' + i.join(' and '));
  };
  KINDS.forEach((k) => {
    const v = draft[k.key][whichSide];
    if (!v) return;
    if (k.need === 2) {
      add(k.label + ' p1', v.p1, k.deploy);
      add(k.label + ' p2', v.p2, k.deploy);
      const len = Math.round(Math.hypot(v.p2.x - v.p1.x, v.p2.y - v.p1.y));
      if (len < 120) rows.push(k.label + ' is only ' + len + 'px long - too short to spread an army');
    } else {
      add(k.label, v, k.deploy);
    }
  });
  return rows;
}

// summarizeSide returns this side's verdict plus a plain-language reason, so
// the tab badge and the Checks box can never disagree about a side.
function summarizeSide(whichSide) {
  const rows = collectIssues(whichSide);
  return { bad: rows.length, rows: rows };
}

// ---------------------------------------------------------------- one-click defaults

// standardRing fills a plan that is deployable against ANY base: every point
// sits in the outer ring (110 px from the screen edge, far outside the base's
// footprint), so nothing here can land in the no-deploy pocket the game refuses
// with "You cannot deploy troops on the Red area!". It is a starting point to
// drag into shape, not a guess about this base.
function standardRing() {
  if (!liveW) { toast('refresh the frame first', true); return; }
  const W = liveW, H = liveH, m = 110, inward = 60;
  SIDES.forEach((s) => {
    const left = s.indexOf('Left') >= 0;
    const top = s.indexOf('Top') >= 0;
    const x = left ? m : W - m;
    const dx = left ? inward : -inward;
    const y0 = Math.round((top ? 0.18 : 0.38) * H);
    const y1 = Math.round((top ? 0.62 : 0.82) * H);
    const mid = Math.round((y0 + y1) / 2);
    draft.troop_line[s] = { p1: { x: x, y: y0 }, p2: { x: x, y: y1 } };
    draft.spell_a[s] = { p1: { x: x + dx, y: y0 }, p2: { x: x + dx, y: y1 } };
    draft.spell_b[s] = { p1: { x: x + 2 * dx, y: y0 }, p2: { x: x + 2 * dx, y: y1 } };
    draft.hero[s] = { x: x, y: mid };
    draft.spell_point[s] = { x: x + dx, y: mid };
  });
  dirty = true;
  render();
  toast('standard outer ring filled for all four sides - drag any handle to taste, then Save');
}

function clearSide() {
  KINDS.forEach((k) => { delete draft[k.key][side]; });
  dirty = true;
  render();
  toast('cleared ' + side);
}

$('ring').onclick = standardRing;
$('clearside').onclick = clearSide;

// ---------------------------------------------------------------- live view

let timer = null;
function tick() {
  if ($('auto').checked && !active && !dirty) refreshFrame();
}
$('auto').addEventListener('change', () => { if ($('auto').checked) refreshFrame(); });
$('refresh').onclick = refreshFrame;
$('reload').onclick = () => loadState();
$('save').onclick = save;

refreshFrame();
loadState().then(() => { timer = setInterval(tick, 1500); });
</script>
</body>
</html>
`
