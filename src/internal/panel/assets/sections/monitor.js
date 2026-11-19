// Раздел «Мониторинг» (ТЗ 25): показания самой машины — GET /api/server/stats.
// Опрос раз в 10 с, только пока раздел открыт и вкладка браузера видна.
let MON = null, MON_TIMER = null;

async function loadMonitor(){
  clearTimeout(MON_TIMER);
  if (TAB !== 'Monitor' || $('viewMain').hidden) return;
  const {ok, data} = await api('/api/server/stats');
  $('monErr').hidden = ok;
  if (!ok) $('monErr').textContent = errOf(data);
  else { MON = data; renderMonitor(); }
  MON_TIMER = setTimeout(() => { if (!document.hidden) loadMonitor(); else MON_TIMER = setTimeout(loadMonitor, 2000); }, 10000);
}

// monLevel — цвет по загрузке: до 70 % зелёный, от 70 % жёлтый, от 90 % красный.
function monLevel(p){ return p >= 90 ? 'err' : p >= 70 ? 'warn' : 'ok'; }
function monPct(used, total){ return total ? used / total * 100 : 0; }
function fmtPct(p){ return (p < 10 ? p.toFixed(1) : Math.round(p)) + ' %'; }
function fmtRate(v){ return fmtBytes(v) + (LANG === 'ru' ? '/с' : '/s'); }
function fmtDur(sec){
  sec = Math.max(0, sec | 0);
  const d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60);
  return d ? fill('mon_dhm', {d, h, m}) : fill('mon_hm', {h, m});
}

// monTile — плитка: значение, полоса, подпись уровня словом (не только цветом).
function monTile(id, pct, sub){
  const box = $(id);
  const lvl = monLevel(pct);
  box.classList.remove('ok', 'warn', 'err');
  box.classList.add(lvl);
  const v = box.querySelector('.v');
  v.textContent = fmtPct(pct);
  if (lvl !== 'ok') v.appendChild(el('span', 'lvl', t('mon_lvl_' + lvl)));
  const bar = box.querySelector('.bar i');
  if (bar) bar.style.width = Math.min(100, pct) + '%';
  box.querySelector('.s').textContent = sub;
}

function monMeter(box, name, used, total){
  const p = monPct(used, total);
  box.className = 'mon-meter ' + monLevel(p);
  box.innerHTML = '';
  const top = el('div', 't');
  top.appendChild(el('span', 'n', name));
  top.appendChild(el('span', 'p', total ? fmtPct(p) : '—'));
  box.appendChild(top);
  box.appendChild(el('div', 'h', total ? fmtBytes(used) + ' / ' + fmtBytes(total) : t('mon_none')));
  const bar = el('div', 'bar'), i = el('i');
  i.style.width = Math.min(100, p) + '%';
  bar.appendChild(i);
  box.appendChild(bar);
}

// monChart — график за час: одна ось, линия (у сети две: принято сплошной,
// отдано пунктиром), наведение — вертикаль и значение в этой точке.
function monChart(card, series, opts){
  const plot = card.querySelector('.mon-plot');
  plot.innerHTML = '';
  const n = Math.max(...series.map(s => s.data.length));
  if (n < 2){ plot.appendChild(el('div', 'empty', t('mon_collecting'))); return; }
  const max = opts.max || Math.max(1, ...series.flatMap(s => s.data)) * 1.15;
  const W = 600, H = 160;
  const x = i => i / (n - 1) * W, y = v => H - Math.min(v, max) / max * H;
  const svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H);
  svg.setAttribute('preserveAspectRatio', 'none');
  svg.setAttribute('role', 'img');
  svg.setAttribute('aria-label', card.querySelector('h1').textContent);
  [0, .5, 1].forEach(f => svgEl('line', {x1: 0, x2: W, y1: f * H, y2: f * H, class: 'grid'}, svg));
  series.forEach((s, k) => {
    const off = n - s.data.length;
    const pts = s.data.map((v, i) => x(i + off).toFixed(1) + ',' + y(v).toFixed(1));
    if (series.length === 1) svgEl('path', {d: 'M' + x(off) + ',' + H + ' L' + pts.join(' L') + ' L' + W + ',' + H + ' Z', class: 'ar'}, svg);
    svgEl('path', {d: 'M' + pts.join(' L'), class: 'ln ' + (s.cls || 'one')}, svg);
  });
  plot.appendChild(svg);
  plot.appendChild(el('span', 'ax top', opts.fmt(max)));
  plot.appendChild(el('span', 'ax mid', opts.fmt(max / 2)));
  plot.appendChild(el('span', 'ax l', fill('mon_ago_min', {m: Math.round((n - 1) * (MON.step || 10) / 60)})));
  plot.appendChild(el('span', 'ax r', t('mon_now')));
  const cross = el('div', 'cross'), tip = el('div', 'tip');
  cross.hidden = tip.hidden = true;
  plot.append(cross, tip);
  const show = e => {
    const r = plot.getBoundingClientRect();
    const i = Math.max(0, Math.min(n - 1, Math.round((e.clientX - r.left) / r.width * (n - 1))));
    const px = i / (n - 1) * r.width;
    cross.hidden = tip.hidden = false;
    cross.style.left = px + 'px';
    tip.innerHTML = '';
    const ago = Math.round((n - 1 - i) * (MON.step || 10));
    tip.appendChild(el('div', '', ago ? fill('mon_ago', {t: ago < 60 ? fill('mon_s', {s: ago}) : fmtDur(ago)}) : t('mon_now')));
    series.forEach(s => {
      const v = s.data[i - (n - s.data.length)];
      const row = el('div', '', (s.name ? s.name + ': ' : ''));
      row.appendChild(el('b', '', v === undefined ? '—' : opts.fmt(v)));
      tip.appendChild(row);
    });
    tip.style.left = Math.min(Math.max(0, px - tip.offsetWidth / 2), r.width - tip.offsetWidth) + 'px';
  };
  plot.onpointermove = show;
  plot.onpointerdown = show;
  plot.onpointerleave = () => { cross.hidden = tip.hidden = true; };
}

function monTable(table, list, cpu){
  table.innerHTML = '';
  const head = document.createElement('tr');
  [t('mon_proc'), 'PID', cpu ? t('mon_cpu_col') : t('mon_mem_col')].forEach(h => head.appendChild(el('th', '', h)));
  table.appendChild(head);
  if (!list.length){ const tr = document.createElement('tr'); cell(tr, cpu ? t('mon_collecting') : '—'); table.appendChild(tr); return; }
  list.forEach(p => {
    const tr = document.createElement('tr');
    cell(tr, p.name);
    cell(tr, String(p.pid), 'num');
    cell(tr, cpu ? fmtPct(p.cpu) : fmtBytes(p.rss), 'num');
    table.appendChild(tr);
  });
}

const MON_SVC = {panel: 'mon_svc_panel', meshd: 'mon_svc_meshd', udprelay: 'mon_svc_udprelay', node: 'mon_svc_node'};
function renderMonitor(){
  const s = MON;
  if (!s) return;
  const cpu = s.cpu || {}, mem = s.mem || {}, net = s.net || {};
  monTile('monTileCpu', cpu.percent || 0, s.ready ? fill('mon_cpu_sub', {n: cpu.count || '—', l: (cpu.load || [0])[0].toFixed(2)}) : t('mon_collecting'));
  monTile('monTileMem', monPct(mem.used, mem.total), fmtBytes(mem.used) + ' / ' + fmtBytes(mem.total));
  const disks = s.disks || [];
  const fullest = disks.slice().sort((a, b) => monPct(b.used, b.total) - monPct(a.used, a.total))[0];
  if (fullest) monTile('monTileDisk', monPct(fullest.used, fullest.total), fullest.mount + ' · ' + fmtBytes(fullest.used) + ' / ' + fmtBytes(fullest.total));
  else monTile('monTileDisk', 0, t('mon_none'));
  const nv = $('monTileNet').querySelector('.v');
  nv.innerHTML = '';
  nv.append('↓ ' + fmtRate(net.rxRate || 0), document.createElement('br'), '↑ ' + fmtRate(net.txRate || 0));
  $('monTileNet').querySelector('.s').textContent = fill('mon_net_sub', {r: fmtBytes(net.rxTotal), t: fmtBytes(net.txTotal)});

  const h = s.history || {};
  monChart($('monChartCpu'), [{data: h.cpu || []}], {max: 100, fmt: v => Math.round(v) + ' %'});
  monChart($('monChartMem'), [{data: h.mem || []}], {max: 100, fmt: v => Math.round(v) + ' %'});
  monChart($('monChartNet'), [{data: h.rx || [], cls: 'rx', name: t('mon_rx')}, {data: h.tx || [], cls: 'tx', name: t('mon_tx')}], {fmt: fmtRate});

  $('monCores').textContent = cpu.count || '—';
  $('monLoad').textContent = (cpu.load || []).map(v => v.toFixed(2)).join(' / ') || '—';
  const bars = $('monCoreBars');
  bars.innerHTML = '';
  (cpu.cores || []).forEach((p, i) => {
    const c = el('div', 'c ' + monLevel(p)), f = el('i');
    f.style.height = Math.max(2, p) + '%';
    c.title = fill('mon_core', {n: i + 1}) + ': ' + fmtPct(p);
    c.appendChild(f);
    bars.appendChild(c);
  });
  monMeter($('monRam'), t('mon_ram'), mem.used, mem.total);
  monMeter($('monSwap'), t('mon_swap'), mem.swapUsed, mem.swapTotal);
  const dbox = $('monDisks');
  dbox.innerHTML = '';
  if (!disks.length) dbox.appendChild(el('div', 'empty', t('mon_none')));
  disks.forEach(d => { const m = el('div'); monMeter(m, d.mount + ' · ' + d.fs, d.used, d.total); dbox.appendChild(m); });

  $('monRxNow').textContent = fmtRate(net.rxRate || 0);
  $('monTxNow').textContent = fmtRate(net.txRate || 0);
  $('monRxTotal').textContent = fmtBytes(net.rxTotal);
  $('monTxTotal').textContent = fmtBytes(net.txTotal);
  $('monHost').textContent = s.host || '—';
  $('monOS').textContent = s.os || '—';
  $('monKernel').textContent = s.kernel || '—';
  $('monUptime').textContent = fmtDur(s.uptimeSeconds);

  const sv = $('monServices');
  sv.innerHTML = '';
  (s.services || []).forEach(x => {
    const row = el('div', 'mon-svc');
    row.appendChild(el('span', 'n', t(MON_SVC[x.id] || x.id)));
    row.appendChild(el('span', 'badge ' + (x.active ? 'ok' : x.installed ? 'warn' : 'dim'),
      x.active ? t('svc_active') : x.installed ? t('svc_stopped') : t('svc_missing')));
    const ver = x.id === 'meshd' ? (x.version ? 'meshd ' + meshdLabel(x) : '') : x.version || '';
    const since = x.active && x.since ? fill('mon_since', {t: fmtDur(s.time - x.since)}) : '';
    row.appendChild(el('span', 'h', [x.unit, ver, since].filter(Boolean).join(' · ')));
    sv.appendChild(row);
  });
  monTable($('monTopCpu'), s.topCpu || [], true);
  monTable($('monTopMem'), s.topMem || [], false);
}

/* ---------- события каркаса ---------- */
onPanel('show:Monitor', () => loadMonitor());
onPanel('lang', () => renderMonitor());
document.addEventListener('visibilitychange', () => { if (!document.hidden && TAB === 'Monitor') loadMonitor(); });
addEventListener('resize', () => { if (TAB === 'Monitor') renderMonitor(); });
