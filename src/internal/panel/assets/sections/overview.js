// Раздел «Обзор»: плитки сети, схема, клиенты, трафик, состояние сервера.
let TRAFFIC = null;
let PERIOD = localStorage.getItem('panelPeriod') || 'month';
async function loadTraffic(){
  const {ok, data} = await api('/api/traffic?period=' + encodeURIComponent(PERIOD));
  if (!ok) return;
  TRAFFIC = data;
  renderTraffic();
}
/* ---------- главная: список подключений (только показ) ---------- */
function renderHomeClients(){
  const table = $('homeClients');
  table.innerHTML = '';
  $('homeClientsEmpty').hidden = CLIENTS.length > 0;
  table.hidden = CLIENTS.length === 0;
  if (!CLIENTS.length) return;

  const head = document.createElement('tr');
  [t('col_device'), t('col_state'), t('col_traffic'), t('col_online')].forEach(name => {
    const th = document.createElement('th');
    th.textContent = name;
    head.appendChild(th);
  });
  table.appendChild(head);

  CLIENTS.forEach(c => {
    const row = document.createElement('tr');

    const nameCell = document.createElement('td');
    const nm = document.createElement('div');
    nm.style.fontWeight = '600';
    nm.textContent = c.name;
    const sub = document.createElement('div');
    sub.className = 'sub';
    sub.textContent = t('device_' + c.deviceType) + ' · ' + t('created_on') + ' ' + fmtDate(c.createdAt);
    nameCell.appendChild(nm); nameCell.appendChild(sub);
    row.appendChild(nameCell);

    const stateCell = document.createElement('td');
    stateCell.appendChild(stateBadge(c.state));
    row.appendChild(stateCell);

    // Неразрывные пробелы: в узкой колонке (ТЗ 27) перенос — только между
    // «принято» и «отдано», а не посреди числа.
    const nb = v => v.replace(/ /g, ' ');
    cell(row, nb('↓ ' + fmtBytes(c.rxBytes)) + ' ' + nb('↑ ' + fmtBytes(c.txBytes)), 'num');
    cell(row, onlineText(c));
    table.appendChild(row);
  });
}

/* ---------- главная: трафик за период ---------- */
$('periods').addEventListener('click', e => {
  const btn = e.target.closest('button');
  if (!btn) return;
  PERIOD = btn.dataset.period;
  localStorage.setItem('panelPeriod', PERIOD);
  document.querySelectorAll('#periods button').forEach(b => b.classList.toggle('on', b === btn));
  loadTraffic();
});

function renderTraffic(){
  document.querySelectorAll('#periods button').forEach(b => {
    b.classList.toggle('on', b.dataset.period === PERIOD);
  });
  const table = $('trafficByClient');
  table.innerHTML = '';
  if (!TRAFFIC){ return; }

  $('trafficRx').textContent = fmtBytes(TRAFFIC.rxBytes);
  $('trafficTx').textContent = fmtBytes(TRAFFIC.txBytes);
  $('trafficAll').textContent = fmtBytes((TRAFFIC.rxBytes || 0) + (TRAFFIC.txBytes || 0));

  const list = TRAFFIC.clients || [];
  if (!list.length) return;
  const head = document.createElement('tr');
  [t('col_device'), t('traffic_down_total'), t('traffic_up_total')].forEach(name => {
    const th = document.createElement('th');
    th.textContent = name;
    head.appendChild(th);
  });
  table.appendChild(head);
  list.forEach(c => {
    const row = document.createElement('tr');
    const nameCell = document.createElement('td');
    const nm = document.createElement('div');
    nm.textContent = c.name;
    const sub = document.createElement('div');
    sub.className = 'sub';
    sub.textContent = t('device_' + c.deviceType);
    nameCell.appendChild(nm); nameCell.appendChild(sub);
    row.appendChild(nameCell);
    cell(row, fmtBytes(c.rxBytes), 'num');
    cell(row, fmtBytes(c.txBytes), 'num');
    table.appendChild(row);
  });
}

/* ---------- где стоит схема ---------- */
// Схема одна и стоит в «Обзоре» (ТЗ 23, 27).
function placeMap(){
  const slot = $('ovMapSlot');
  const card = $('mapCard');
  if (card.parentNode !== slot){ slot.appendChild(card); MAP_SIG = ''; }
}
function mapVisible(){ return $('meshMap').getClientRects().length > 0; }

// renderOverviewMesh — плитки и схема (только когда сеть устройств есть).
function renderOverviewMesh(m){
  const devices = meshDevices();

  // Плитки.
  const online = devices.filter(d => d.online);
  animateNum($('tileDevices'), online.length, v => Math.round(v));
  $('tileDevicesSub').textContent = t('tile_online') + ' · ' + t('tile_of') + ' ' + devices.length;
  // Счётчики — по всей сети (ТЗ 24): серверы сети, задержка и трафик со
  // всех серверов.
  const servers = m.servers || [];
  animateNum($('tileServers'), servers.filter(s => s.online).length, v => Math.round(v));
  $('tileServersSub').textContent = t('srv_tile_reach') + ' · ' + t('tile_of') + ' ' + servers.length;
  const rtts = [];
  devices.forEach(d => devConns(d).forEach(c => { if (c.online && (c.rttAvgMs || c.rttMs)) rtts.push(c.rttAvgMs || c.rttMs); }));
  const avg = rtts.length ? rtts.reduce((a, b) => a + b, 0) / rtts.length : 0;
  if (avg) animateNum($('tileLatency'), avg, fmtMs); else $('tileLatency').textContent = '—';
  $('tileLatencySub').textContent = avg ? t('tile_avg') : t('tile_no_data');
  const traffic = devices.reduce((a, d) => a + devConns(d).reduce((x, c) => x + (c.bytesUp || 0), 0), 0);
  animateNum($('tileTraffic'), traffic, fmtBytes);
  $('tileTrafficSub').textContent = t('tile_traffic_sub');

  // Невидимую схему не считаем: у неё нет размера, и она тратила бы кадры.
  if (mapVisible()) renderMap(servers, devices);
}

/* ---------- схема сети ---------- */
let MAP_SIG = '', MAP_REFS = {}, MAP_SIM = null;
// Масштаб и сдвиг карты (как у обычной карты): k — во сколько раз
// приближено, x/y — сдвиг. Переживает перестройку схемы.
const MAP_VIEW = {k: 1, x: 0, y: 0, user: false}; // user — человек сам двигал карту
// Где стояли узлы (в долях ширины и высоты схемы): перестройка — новое
// устройство, смена языка, другой размер окна — не раскидывает их заново.
const MAP_POS = {};

// edgeMs — задержка для подписи на схеме: «12мс», «135мс».
function edgeMs(v){
  if (!v) return '';
  return (v < 1 ? '<1' : String(Math.round(v))) + (LANG === 'ru' ? 'мс' : 'ms');
}

/* Схема живая, как граф в Obsidian: узлы — шарики, которые расталкиваются,
   линии — пружины, которые держат их на расстоянии, где помещаются цифры
   задержки. Каждый кадр считаются силы, узлы чуть сдвигаются, и за пару
   секунд всё успокаивается (alpha — «температура», она остывает до нуля).
   Узел можно схватить и потащить — остальные подвинутся следом. */

// mapEdge — линия схемы между узлами a и b. Она из двух половинок, чтобы
// посередине оставить окно под цифры задержки.
function mapEdge(sim, a, b, kind, labelOn){
  const cls = kind === 'srv' ? 'edge srv' : kind === 'p2p' ? 'edge p2p' : kind === 'relay' ? 'edge relay' : 'edge';
  if (kind === 'relay') kind = 'p2p'; // раскладка — как у прямого соединения: слабая пружина
  const g = svgEl('g', {class: cls}, kind === 'p2p' ? sim.gDirect : sim.gEdges);
  const e = {a, b, kind, g, labelOn, text: '', w: 0,
    bend: 0,
    rest: 1.5 * (kind === 'srv' ? (sim.narrow ? 150 : 230) : kind === 'p2p' ? (sim.narrow ? 110 : 150) : (sim.narrow ? 90 : 125)),
    k: kind === 'p2p' ? 0.04 : 0.2,
    p1: svgEl('path', {}, g), p2: svgEl('path', {}, g),
    tx: svgEl('text', {class: 'edge-label'}, sim.gLabels)};
  sim.edges.push(e);
  return e;
}

function removeEdge(sim, e){
  e.g.remove();
  e.tx.remove();
  sim.edges = sim.edges.filter(x => x !== e);
}

// setEdgeText — цифры на линии; цвет — как у самой линии.
function setEdgeText(e, text, color){
  if (!e.labelOn) text = '';
  e.tx.style.fill = color || '';
  if (text === e.text) return;
  e.text = text;
  e.tx.textContent = text;
  e.w = 0;
  if (text){
    try { e.w = e.tx.getComputedTextLength(); } catch (err) {}
    if (!e.w) e.w = text.length * 5.8;
  }
  if (MAP_SIM && !MAP_SIM.raf) simDraw(MAP_SIM);
}

// layoutEdge рисует линию квадратичной кривой Безье и прерывает её ровно на
// ширину цифр с небольшим зазором: цифры стоят «на пути» линии, но её не
// касаются. Если середина занята кружком или названием узла — цифры
// сдвигаются вдоль линии; места нет совсем — линия целиком.
function layoutEdge(e, obst){
  e.bend = pickBend(e, obst);
  const a = e.a, b = e.b, bend = e.bend;
  const dx = b.x - a.x, dy = b.y - a.y;
  const c = {x: (a.x + b.x) / 2 - dy * bend, y: (a.y + b.y) / 2 + dx * bend};
  const at = t => ({x: (1-t)*(1-t)*a.x + 2*t*(1-t)*c.x + t*t*b.x, y: (1-t)*(1-t)*a.y + 2*t*(1-t)*c.y + t*t*b.y});
  const tan = t => ({x: 2*(1-t)*(c.x - a.x) + 2*t*(b.x - c.x), y: 2*(1-t)*(c.y - a.y) + 2*t*(b.y - c.y)});
  const f = v => v.toFixed(1);
  // Кусок кривой от t0 до t1 — тоже квадратичная кривая (алгоритм де Кастельжо).
  const seg = (t0, t1) => {
    const p = at(t0), q = at(t1), v = tan(t0), k = (t1 - t0) / 2;
    return 'M' + f(p.x) + ' ' + f(p.y) + 'Q' + f(p.x + v.x * k) + ' ' + f(p.y + v.y * k) + ' ' + f(q.x) + ' ' + f(q.y);
  };
  const whole = () => {
    e.tx.setAttribute('visibility', 'hidden');
    e.p1.setAttribute('d', seg(0, 1));
    e.p2.setAttribute('d', '');
  };
  if (!e.text) return whole();
  const hw = e.w / 2 + 3, hh = 5 + 3;
  // Площадь, на которую цифры в точке t наехали бы на узлы.
  const overlap = t => {
    const m = at(t);
    let sum = 0;
    for (const r of obst){
      const ox = Math.min(m.x + hw, r.x2) - Math.max(m.x - hw, r.x1);
      const oy = Math.min(m.y + hh, r.y2) - Math.max(m.y - hh, r.y1);
      if (ox > 0 && oy > 0) sum += ox * oy;
    }
    return sum;
  };
  let tm = -1;
  for (const t of [0.5, 0.44, 0.56, 0.38, 0.62, 0.32, 0.68, 0.26, 0.74]){
    if (overlap(t) === 0){ tm = t; break; }
  }
  if (tm < 0) return whole();
  const m = at(tm), v = tan(tm), speed = Math.hypot(v.x, v.y) || 1;
  // Следующие цифры на эти уже не лягут.
  obst.push({x1: m.x - hw, x2: m.x + hw, y1: m.y - hh, y2: m.y + hh});
  e.tx.removeAttribute('visibility');
  e.tx.setAttribute('x', f(m.x));
  e.tx.setAttribute('y', f(m.y));
  // Прямая через центр прямоугольника цифр выходит из него на расстоянии
  // min(½ширины/|cos|, ½высоты/|sin|) — столько линии и убираем.
  const cos = Math.abs(v.x) / speed, sin = Math.abs(v.y) / speed;
  const half = Math.min(cos > 1e-3 ? hw / cos : Infinity, sin > 1e-3 ? hh / sin : Infinity);
  const dt = half / speed;
  e.p1.setAttribute('d', tm - dt > 0.02 ? seg(0, tm - dt) : '');
  e.p2.setAttribute('d', tm + dt < 0.98 ? seg(tm + dt, 1) : '');
}

// simObstacles — кружки и названия узлов: сюда цифрам задержки нельзя.
function simObstacles(sim){
  const out = [];
  sim.nodes.forEach(n => {
    out.push({x1: n.x - n.r - 2, x2: n.x + n.r + 2, y1: n.y - n.r - 2, y2: n.y + n.r + 2, n, disc: true});
    if (n.tw) out.push(n.up
      ? {x1: n.x - n.tw / 2 - 2, x2: n.x + n.tw / 2 + 2, y1: n.y - n.r - n.th - 2, y2: n.y - n.r - 2, n}
      : {x1: n.x - n.tw / 2 - 2, x2: n.x + n.tw / 2 + 2, y1: n.y + n.r + 2, y2: n.y + n.r + n.th, n});
  });
  return out;
}

// pickBend — изгиб линии: прямая, если путь свободен; иначе наименьший
// изгиб (влево или вправо), при котором линия не задевает чужие кружки и
// ничьи названия, включая название под своим же узлом. При равенстве —
// прежний изгиб, чтобы линия не дёргалась с боку на бок.
function pickBend(e, obst){
  const a = e.a, b = e.b, dx = b.x - a.x, dy = b.y - a.y;
  const hits = bend => {
    const cx = (a.x + b.x) / 2 - dy * bend, cy = (a.y + b.y) / 2 + dx * bend;
    let h = 0;
    for (let t = 0.05; t < 0.96; t += 0.045){
      const x = (1-t)*(1-t)*a.x + 2*t*(1-t)*cx + t*t*b.x, y = (1-t)*(1-t)*a.y + 2*t*(1-t)*cy + t*t*b.y;
      for (const r of obst){
        if (r.disc && (r.n === a || r.n === b)) continue;
        if (x > r.x1 - 3 && x < r.x2 + 3 && y > r.y1 - 3 && y < r.y2 + 3) h++;
      }
    }
    return h;
  };
  const side = e.bend < 0 ? -1 : 1;
  let best = e.bend, bestH = hits(e.bend);
  if (bestH === 0 && Math.abs(e.bend) < 0.005) return e.bend;
  for (const m of [0, 0.07, 0.14, 0.22, 0.31, 0.42]){
    for (const b2 of m ? [side * m, -side * m] : [0]){
      const h = hits(b2);
      // Лучше — меньше задеваний; при равных — меньше изгиб.
      if (h < bestH || (h === bestH && Math.abs(b2) < Math.abs(best) - 0.01)){ best = b2; bestH = h; }
    }
    if (bestH === 0 && Math.abs(best) <= m) break;
  }
  return best;
}

// Пересекаются ли отрезки ab и cd.
function segCross(a, b, c, d){
  const o = (p, q, r) => (q.x - p.x) * (r.y - p.y) - (q.y - p.y) * (r.x - p.x);
  const d1 = o(a, b, c), d2 = o(a, b, d), d3 = o(c, d, a), d4 = o(c, d, b);
  return d1 * d2 < 0 && d3 * d4 < 0;
}

// simTick — один шаг: силы → скорости → положения.
function simTick(sim){
  const N = sim.nodes, E = sim.edges, al = sim.alpha;
  const sx = 1.6; // названия шире кружков: по горизонтали узлам нужно больше места
  // 1. Узлы отталкиваются друг от друга; вплотную — жёстко расталкиваются.
  for (let i = 0; i < N.length; i++){
    for (let j = i + 1; j < N.length; j++){
      const a = N[i], b = N[j];
      let dx = (b.x - a.x) / sx, dy = b.y - a.y, d = Math.hypot(dx, dy);
      if (d < 0.01){ dx = Math.cos(i * 7 + j); dy = Math.sin(i * 7 + j); d = 1; }
      const req = Math.max((a.tw + b.tw) / 2 / sx + 8, a.r + b.r + 34);
      let f = sim.charge * al / d;
      if (d < req) f += (req - d) * 0.25;
      const ux = dx / d * sx * f, uy = dy / d * f;
      a.vx -= ux; a.vy -= uy;
      b.vx += ux; b.vy += uy;
    }
  }
  // 2. Линии — пружины: держат длину, на которой цифрам хватает места.
  E.forEach(e => {
    const dx = e.b.x - e.a.x, dy = e.b.y - e.a.y, d = Math.hypot(dx, dy) || 1;
    const rest = Math.max(e.rest, e.a.r + e.b.r + (e.w ? e.w + 30 : 20));
    const f = (d - rest) / d * e.k * al;
    e.a.vx += dx * f; e.a.vy += dy * f;
    e.b.vx -= dx * f; e.b.vy -= dy * f;
  });
  // 3. Узел не лежит на чужой линии — ни кружком, ни названием под ним:
  // линия его отталкивает.
  E.forEach(e => {
    const ax = e.a.x, ay = e.a.y, dx = e.b.x - ax, dy = e.b.y - ay, l2 = dx * dx + dy * dy || 1;
    N.forEach(n => {
      if (n === e.a || n === e.b) return;
      const ly = n.up ? n.y - n.r - n.th / 2 : n.y + n.r + n.th / 2, half = n.tw / 2 - 6;
      const pts = [[n.x, n.y, n.r + 18]];
      if (n.tw) pts.push([n.x, ly, n.th / 2 + 8], [n.x - half, ly, n.th / 2 + 6], [n.x + half, ly, n.th / 2 + 6]);
      for (const [x, y, thr] of pts){
        const t = ((x - ax) * dx + (y - ay) * dy) / l2;
        if (t <= 0.05 || t >= 0.95) continue;
        let nx = x - (ax + dx * t), ny = y - (ay + dy * t), d = Math.hypot(nx, ny);
        if (d >= thr) continue;
        if (d < 0.01){ nx = -dy; ny = dx; d = Math.hypot(nx, ny) || 1; }
        const f = (thr - d) / d * 0.2 * Math.max(al, 0.05);
        n.vx += nx * f; n.vy += ny * f;
        e.a.vx -= nx * f / 2; e.a.vy -= ny * f / 2;
        e.b.vx -= nx * f / 2; e.b.vy -= ny * f / 2;
        break;
      }
    });
  });
  // 4. Линии из одного узла расходятся веером: если две идут почти в одну
  // сторону, их дальние концы разъезжаются по кругу.
  const minA = 0.55; // ≈ 30°
  N.forEach(c => {
    const nb = [];
    E.forEach(e => { if (e.a === c) nb.push(e.b); else if (e.b === c) nb.push(e.a); });
    for (let i = 0; i < nb.length; i++){
      for (let j = i + 1; j < nb.length; j++){
        const u = nb[i], v = nb[j];
        const ux = u.x - c.x, uy = u.y - c.y, vx = v.x - c.x, vy = v.y - c.y;
        const lu = Math.hypot(ux, uy) || 1, lv = Math.hypot(vx, vy) || 1;
        const ang = Math.acos(Math.max(-1, Math.min(1, (ux * vx + uy * vy) / lu / lv)));
        if (ang >= minA) continue;
        // Поворот: u — в сторону от v, v — в сторону от u.
        const side = ux * vy - uy * vx >= 0 ? 1 : -1;
        const f = (minA - ang) * 0.35 * Math.max(al, 0.05);
        u.vx += uy / lu * side * f * lu * 0.1; u.vy += -ux / lu * side * f * lu * 0.1;
        v.vx -= vy / lv * side * f * lv * 0.1; v.vy -= -vx / lv * side * f * lv * 0.1;
      }
    }
  });
  // 5. Линии не пересекаются: ближний к чужой линии конец выталкивается на
  // другую её сторону.
  for (let i = 0; i < E.length; i++){
    for (let j = i + 1; j < E.length; j++){
      const e = E[i], g = E[j];
      if (e.a === g.a || e.a === g.b || e.b === g.a || e.b === g.b) continue;
      if (!segCross(e.a, e.b, g.a, g.b)) continue;
      [[e, g], [g, e]].forEach(([p, q]) => {
        const dx = q.b.x - q.a.x, dy = q.b.y - q.a.y, l = Math.hypot(dx, dy) || 1;
        const nx = -dy / l, ny = dx / l;
        const s1 = (p.a.x - q.a.x) * nx + (p.a.y - q.a.y) * ny;
        const s2 = (p.b.x - q.a.x) * nx + (p.b.y - q.a.y) * ny;
        const [n, s, other] = Math.abs(s1) < Math.abs(s2) ? [p.a, s1, s2] : [p.b, s2, s1];
        const w = p.kind === 'p2p' || q.kind === 'p2p' ? 0.04 : 0.1;
        const f = (Math.abs(s) + 12) * w * Math.max(al, 0.05) * Math.sign(other || 1);
        n.vx += nx * f; n.vy += ny * f;
      });
    }
  }
  // 6. Главный сервер тянется к центру, остальные — чуть-чуть.
  N.forEach(n => {
    const g = n.main ? 0.12 : 0.012;
    n.vx += (sim.W / 2 - n.x) * g * al;
    n.vy += (sim.H / 2 - n.y) * g * al;
  });
  // 7. Сдвиг с трением и границы схемы.
  N.forEach(n => {
    if (n.pinned){ n.vx = n.vy = 0; return; }
    n.vx *= 0.6; n.vy *= 0.6;
    n.x += n.vx; n.y += n.vy;
    // Мир карты шире окна: узлы не упираются в края, а карта сама
    // отдаляется, чтобы всё было видно (см. fitView).
    const ex = sim.W * 0.3, ey = sim.H * 0.3;
    const mx = Math.max(n.r, n.tw / 2) + 4;
    n.x = Math.max(mx - ex, Math.min(sim.W + ex - mx, n.x));
    n.y = Math.max(n.r + 4 - ey, Math.min(sim.H + ey - n.r - n.th - 2, n.y));
  });
  sim.alpha += (sim.target - sim.alpha) * 0.02;
}

// simDraw — перенести положения на экран.
function simDraw(sim){
  sim.nodes.forEach(n => {
    n.outer.setAttribute('transform', 'translate(' + n.x.toFixed(1) + ' ' + n.y.toFixed(1) + ')');
    MAP_POS[n.key] = {fx: n.x / sim.W, fy: n.y / sim.H};
  });
  labelSides(sim);
  sim.obst = simObstacles(sim);
  sim.edges.forEach(e => layoutEdge(e, sim.obst));
  if (!MAP_VIEW.user && !sim.dragging) fitView(sim);
}

// labelSides — название узла под кружком, но если вниз от узла уходит
// больше линий, чем вверх, — над ним: иначе линии шли бы прямо через текст.
// Сторона меняется, только когда на другой строго свободнее.
function labelSides(sim){
  sim.nodes.forEach(n => {
    if (!n.tx) return;
    let down = 0, up = 0;
    sim.edges.forEach(e => {
      const o = e.a === n ? e.b : e.b === n ? e.a : null;
      if (!o) return;
      const vx = o.x - n.x, vy = o.y - n.y;
      if (Math.abs(vx) < Math.abs(vy) * 1.3){ if (vy > 0) down++; else up++; }
    });
    const want = n.up ? !(up > down) : down > up;
    if (want === n.up) return;
    n.up = want;
    if (want){
      n.tx.setAttribute('y', -(n.r + (n.st ? 19 : 8)));
      if (n.st) n.st.setAttribute('y', -(n.r + 8));
    } else {
      n.tx.setAttribute('y', n.r + 13);
      if (n.st) n.st.setAttribute('y', n.r + 24);
    }
  });
}

// fitView — масштаб и сдвиг, при которых видна вся сеть с названиями (но не
// крупнее, чем 1:1). Работает, пока человек сам не двигал карту.
function fitView(sim){
  if (!sim.nodes.length) return;
  let x1 = Infinity, y1 = Infinity, x2 = -Infinity, y2 = -Infinity;
  sim.nodes.forEach(n => {
    const hw = Math.max(n.r, n.tw / 2);
    x1 = Math.min(x1, n.x - hw); x2 = Math.max(x2, n.x + hw);
    y1 = Math.min(y1, n.y - n.r - (n.up ? n.th : 0)); y2 = Math.max(y2, n.y + n.r + (n.up ? 0 : n.th));
  });
  // Справа — место под кнопки масштаба, чтобы они не закрывали узлы.
  const pad = 24, right = 56;
  const k = Math.min(1, (sim.W - pad - right) / Math.max(1, x2 - x1), (sim.H - 2 * pad) / Math.max(1, y2 - y1));
  MAP_VIEW.k = k;
  MAP_VIEW.x = (sim.W - right + pad) / 2 - (x1 + x2) / 2 * k;
  MAP_VIEW.y = sim.H / 2 - (y1 + y2) / 2 * k;
  applyView();
}

function simRun(sim){
  if (sim.raf) return;
  const step = () => {
    sim.raf = 0;
    if (MAP_SIM !== sim || !sim.svg.isConnected) return;
    simTick(sim);
    simDraw(sim);
    if (sim.alpha > 0.005 || sim.target > 0) sim.raf = requestAnimationFrame(step);
  };
  sim.raf = requestAnimationFrame(step);
}

// kickSim — «подогреть» схему: что-то поменялось, пусть узлы подвинутся.
function kickSim(alpha){
  const sim = MAP_SIM;
  if (!sim) return;
  sim.alpha = Math.max(sim.alpha, alpha);
  if (REDUCED_MOTION){
    for (let i = 0; i < 200 && sim.alpha > 0.005; i++) simTick(sim);
    simDraw(sim);
    return;
  }
  simRun(sim);
}
const REDUCED_MOTION = window.matchMedia && matchMedia('(prefers-reduced-motion: reduce)').matches;

// mapWorld — точка экрана схемы → точка карты (с учётом масштаба и сдвига).
function mapWorld(p){ return {x: (p.x - MAP_VIEW.x) / MAP_VIEW.k, y: (p.y - MAP_VIEW.y) / MAP_VIEW.k}; }

function applyView(){
  if (MAP_SIM) MAP_SIM.gView.setAttribute('transform',
    'translate(' + MAP_VIEW.x.toFixed(1) + ' ' + MAP_VIEW.y.toFixed(1) + ') scale(' + MAP_VIEW.k.toFixed(4) + ')');
}

// zoomAt — приблизить в k2 раз так, чтобы точка p экрана осталась на месте.
function zoomAt(p, k2){
  MAP_VIEW.user = true;
  k2 = Math.max(0.2, Math.min(4, k2));
  MAP_VIEW.x = p.x - (p.x - MAP_VIEW.x) * k2 / MAP_VIEW.k;
  MAP_VIEW.y = p.y - (p.y - MAP_VIEW.y) * k2 / MAP_VIEW.k;
  MAP_VIEW.k = k2;
  applyView();
}

// Карта: колесо — масштаб под курсором, фон тащится мышью или пальцем, два
// пальца — щипок. Узлы тащатся отдельно (dragNode).
(function mapNav(){
  const svg = $('meshMap');
  const pts = new Map();
  let start = null;
  const center = () => ({x: svg.viewBox.baseVal.width / 2, y: svg.viewBox.baseVal.height / 2});
  const snapshot = () => {
    const list = [...pts.values()];
    start = {view: {...MAP_VIEW}, list: list.map(p => ({...p}))};
    if (list.length >= 2){
      start.d = Math.hypot(list[0].x - list[1].x, list[0].y - list[1].y) || 1;
      start.mid = {x: (list[0].x + list[1].x) / 2, y: (list[0].y + list[1].y) / 2};
    }
  };
  svg.addEventListener('pointerdown', e => {
    if (e.target.closest('.node') || (e.pointerType === 'mouse' && e.button !== 0)) return;
    try { svg.setPointerCapture(e.pointerId); } catch (err) {}
    pts.set(e.pointerId, svgPoint(svg, e.clientX, e.clientY));
    MAP_VIEW.user = true;
    svg.classList.add('panning');
    hidePop();
    snapshot();
  });
  svg.addEventListener('pointermove', e => {
    if (!pts.has(e.pointerId) || !start) return;
    pts.set(e.pointerId, svgPoint(svg, e.clientX, e.clientY));
    const list = [...pts.values()], v = start.view;
    if (list.length >= 2 && start.d){
      // Щипок: масштаб по расстоянию между пальцами, середина — на месте.
      const d = Math.hypot(list[0].x - list[1].x, list[0].y - list[1].y);
      const mid = {x: (list[0].x + list[1].x) / 2, y: (list[0].y + list[1].y) / 2};
      const k = Math.max(0.2, Math.min(4, v.k * d / start.d));
      const wx = (start.mid.x - v.x) / v.k, wy = (start.mid.y - v.y) / v.k;
      MAP_VIEW.k = k; MAP_VIEW.x = mid.x - wx * k; MAP_VIEW.y = mid.y - wy * k;
    } else if (list.length === 1 && start.list.length === 1){
      MAP_VIEW.x = v.x + list[0].x - start.list[0].x;
      MAP_VIEW.y = v.y + list[0].y - start.list[0].y;
    }
    applyView();
  });
  const up = e => {
    if (!pts.delete(e.pointerId)) return;
    if (!pts.size){ start = null; svg.classList.remove('panning'); } else snapshot();
  };
  svg.addEventListener('pointerup', up);
  svg.addEventListener('pointercancel', up);
  svg.addEventListener('wheel', e => {
    e.preventDefault();
    zoomAt(svgPoint(svg, e.clientX, e.clientY), MAP_VIEW.k * Math.exp(-e.deltaY * 0.0015));
  }, {passive: false});
  $('mapZoomIn').addEventListener('click', () => zoomAt(center(), MAP_VIEW.k * 1.3));
  $('mapZoomOut').addEventListener('click', () => zoomAt(center(), MAP_VIEW.k / 1.3));
  $('mapZoomFit').addEventListener('click', () => {
    MAP_VIEW.user = false;
    if (MAP_SIM) fitView(MAP_SIM);
  });
})();

function svgPoint(svg, x, y){
  const pt = svg.createSVGPoint();
  pt.x = x; pt.y = y;
  // Обычный объект: у SVGPoint x и y — геттеры, и копия {...p} была бы пустой.
  const q = pt.matrixTransform(svg.getScreenCTM().inverse());
  return {x: q.x, y: q.y};
}

// dragNode — узел можно тащить мышью или пальцем; отпустил — схема
// успокаивается сама. Короткое касание без движения — обычный щелчок.
function dragNode(sim, n){
  n.outer.addEventListener('pointerdown', ev => {
    if (ev.button !== 0) return;
    const st = {id: ev.pointerId, x0: ev.clientX, y0: ev.clientY, moved: false};
    try { n.outer.setPointerCapture(ev.pointerId); } catch (err) {}
    const move = e2 => {
      if (e2.pointerId !== st.id) return;
      if (!st.moved){
        if (Math.hypot(e2.clientX - st.x0, e2.clientY - st.y0) < 4) return;
        st.moved = true;
        sim.dragging = true;
        n.pinned = true;
        n.outer.classList.add('dragging');
        hidePop();
        sim.target = 0.25;
        kickSim(0.3);
      }
      const p = mapWorld(svgPoint(sim.svg, e2.clientX, e2.clientY));
      n.x = p.x; n.y = p.y;
    };
    const up = e2 => {
      if (e2.pointerId !== st.id) return;
      n.outer.removeEventListener('pointermove', move);
      n.outer.removeEventListener('pointerup', up);
      n.outer.removeEventListener('pointercancel', up);
      if (!st.moved) return;
      // Щелчок, который браузер пришлёт следом, — конец перетаскивания.
      n.justDragged = true;
      setTimeout(() => { n.justDragged = false; }, 0);
      sim.dragging = false;
      n.pinned = false;
      n.outer.classList.remove('dragging');
      sim.target = 0;
      kickSim(0.2);
    };
    n.outer.addEventListener('pointermove', move);
    n.outer.addEventListener('pointerup', up);
    n.outer.addEventListener('pointercancel', up);
  });
}

function qClass(online, q){ return online ? 'on q-' + (q || 'unknown') : 'off'; }
// mapServersOf — к каким серверам на схеме тянуть линии от устройства: с
// кем на связи; ни с кем — к тому, через который его знаем.
function mapServersOf(d){
  const on = devConns(d).filter(c => c.online).map(c => c.server);
  return on.length ? [...new Set(on)] : [d.server];
}

function renderMap(servers, devices){
  const svg = $('meshMap');
  const W = Math.round(svg.clientWidth) || 720, H = Math.round(svg.clientHeight) || 420;
  // Раскладка меняется только от состава сети; качество связи и «в сети»
  // обновляются на месте — иначе схема мигала бы при каждом опросе.
  const links = (MESH && MESH.serverLinks) || [];
  const relays = ((MESH && MESH.relays) || []).filter(r => r.net === MESH_NET);
  const sig = [W, H, LANG, servers.map(s => s.id + '/' + s.name).join(','),
    devices.map(d => d.id + '/' + mapServersOf(d).join('+') + '/' + d.display + '/' + d.platform).join(','),
    links.map(l => l.a + '-' + l.b).join(','), relays.map(r => r.from + '>' + r.to).join(',')].join('|');
  if (sig !== MAP_SIG){
    const first = !MAP_SIG;
    MAP_SIG = sig;
    buildMap(svg, W, H, servers, devices, first);
  }
  servers.forEach(s => {
    const r = MAP_REFS['s:' + s.id];
    if (!r) return;
    r.node.classList.toggle('offline', !s.online);
    r.node.classList.toggle('stale', !!s.stale);
    r.halo.style.display = s.online ? '' : 'none';
    r.data = s;
  });
  // Линии между серверами: цвет — по задержке связи сервер—сервер.
  links.forEach(l => {
    const e = MAP_REFS['l:' + l.a + '|' + l.b];
    if (!e) return;
    const q = l.rttMs ? qOfMs(l.rttMs) : (l.online ? 'excellent' : 'unknown');
    e.g.setAttribute('class', 'edge srv ' + qClass(l.online, q));
    setEdgeText(e, l.online ? edgeMs(l.rttMs) : '', qColor(l.online, q));
  });
  devices.forEach(d => {
    const r = MAP_REFS['d:' + d.id];
    if (!r) return;
    (r.edges || []).forEach(({e, srv}) => {
      const c = devConns(d).find(x => x.server === srv) || {};
      e.g.setAttribute('class', 'edge ' + qClass(c.online, c.quality));
      setEdgeText(e, c.online ? edgeMs(c.rttMs) : '', qColor(c.online, c.quality));
    });
    r.node.classList.toggle('offline', !d.online);
    r.halo.style.display = d.online ? '' : 'none';
    r.disc.setAttribute('stroke', qColor(d.online, d.quality));
    r.data = d;
  });
  drawDirect(devices);
}

// drawDirect — прямые соединения между устройствами (по отчётам самих
// устройств): синяя пунктирная дуга мимо сервера. Тоже пружина, только
// слабая: устройства с прямым соединением чуть тянутся друг к другу.
function drawDirect(devices){
  const sim = MAP_SIM;
  if (!sim) return;
  const byIP = {}, want = new Map();
  devices.forEach(d => { byIP[d.ip] = d; });
  devices.forEach(d => {
    (d.direct || []).forEach(l => {
      const o = byIP[l.ip];
      const a = sim.byKey['d:' + d.id], b = o && sim.byKey['d:' + o.id];
      if (!a || !b || !d.online || !o.online) return;
      // Каждая пара — один раз: обычно о ней сообщают оба устройства.
      const key = [d.id, o.id].sort().join('|');
      if (!want.has(key)) want.set(key, {a, b, d, o, l});
    });
  });
  let changed = false;
  sim.direct.forEach((e, key) => {
    if (want.has(key)) return;
    removeEdge(sim, e);
    sim.direct.delete(key);
    changed = true;
  });
  want.forEach((w, key) => {
    let e = sim.direct.get(key);
    if (!e){
      e = mapEdge(sim, w.a, w.b, 'p2p', !sim.many);
      e.title = svgEl('title', {}, e.g);
      sim.direct.set(key, e);
      changed = true;
    }
    e.title.textContent = w.d.display + ' ⇄ ' + w.o.display + ' · ' + t('map_direct') +
      (w.l.rttMs ? ' · ' + fmtMs(w.l.rttMs) : '');
    setEdgeText(e, edgeMs(w.l.rttMs), 'var(--accent)');
  });
  if (changed) kickSim(0.35);
  else if (!sim.raf) simDraw(sim);
}

function qColor(online, q){
  if (!online) return 'var(--dim)';
  return {excellent:'var(--ok)', good:'var(--ok)', fair:'var(--warn)', poor:'var(--err)'}[q] || 'var(--accent)';
}

function buildMap(svg, W, H, servers, devices, animate){
  if (MAP_SIM && MAP_SIM.raf) cancelAnimationFrame(MAP_SIM.raf);
  svg.innerHTML = '';
  svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H);
  MAP_REFS = {};
  const cx = W / 2, cy = H / 2, S = Math.min(W, H);
  const defs = svgEl('defs', {}, svg);
  const view = svgEl('g', {}, svg);
  const grad = svgEl('linearGradient', {id:'gNode', x1:0, y1:0, x2:0, y2:1}, defs);
  svgEl('stop', {offset:'0', 'stop-color':'var(--accent-muted)'}, grad);
  svgEl('stop', {offset:'1', 'stop-color':'var(--accent-muted-dark)'}, grad);
  for (let i = 1; i <= 3; i++) svgEl('circle', {cx, cy, r: S * 0.155 * i, fill:'none', class:'map-grid'}, view);
  const narrow = W < 520;
  const many = devices.length > (narrow ? 12 : 28);
  const sim = MAP_SIM = {svg, W, H, narrow, many, nodes: [], edges: [], byKey: {}, direct: new Map(),
    alpha: 1, target: 0, raf: 0, charge: 1.5 * (narrow ? 160 : 260), gView: view,
    gEdges: svgEl('g', {}, view), gDirect: svgEl('g', {}, view), gLabels: svgEl('g', {}, view)};
  const gNodes = svgEl('g', {}, view);
  applyView();

  const main = servers.find(s => s.main);
  const secs = servers.filter(s => !s.main);
  // Начальные места — где узел стоял раньше; новому — возле его сервера.
  const start = (key, x, y) => {
    const p = MAP_POS[key];
    return p ? {x: p.fx * W, y: p.fy * H} : {x, y};
  };
  const rx = W * 0.33, ry = H * 0.3;
  let k = 0, fresh = 0;
  const node = (key, p0, r, path, label, sub, data, isServer, isMain) => {
    if (!MAP_POS[key]) fresh++;
    const p = start(key, p0.x, p0.y);
    // Положение — атрибутом внешней группы, анимация появления — у внутренней:
    // CSS-свойство transform у SVG перекрывает атрибут, и анимация на той же
    // группе увезла бы узел в угол схемы.
    const outer = svgEl('g', {class: 'node'}, gNodes);
    const g = svgEl('g', animate ? {class: 'node-enter'} : {}, outer);
    if (animate) g.style.animationDelay = (k++ * 0.025) + 's';
    const halo = svgEl('circle', {r, fill: isServer ? 'var(--accent)' : 'var(--ok)', class: 'halo'}, g);
    const disc = svgEl('circle', {r, fill: 'url(#gNode)', stroke: 'var(--accent)', class: 'disc'}, g);
    svgEl('path', {d: path, class: 'glyph', transform: 'scale(' + (r / 10.5).toFixed(2) + ')'}, g);
    let th = 2, tx = null, st = null;
    if (!many || isServer){
      tx = svgEl('text', {y: r + 13}, g);
      tx.textContent = label.length > 18 ? label.slice(0, 17) + '…' : label;
      th = 17;
      if (sub && !narrow){
        st = svgEl('text', {y: r + 24, class: 'sub'}, g);
        st.textContent = sub;
        th = 28;
      }
    }
    let tw = 0;
    outer.querySelectorAll('text').forEach(x => { try { tw = Math.max(tw, x.getComputedTextLength()); } catch (err) {} });
    const n = {key, x: p.x, y: p.y, vx: 0, vy: 0, r, tw, th, main: !!isMain, outer, tx, st, up: false};
    sim.nodes.push(n);
    sim.byKey[key] = n;
    dragNode(sim, n);
    const ref = {node: outer, halo, disc, data, n};
    MAP_REFS[key] = ref;
    outer.addEventListener('mouseenter', () => { if (!sim.dragging) showPop(disc, isServer ? serverPop(ref.data) : devicePop(ref.data)); });
    outer.addEventListener('mouseleave', () => { if (!hintPinned) hidePop(); });
    outer.addEventListener('click', e => {
      e.stopPropagation();
      if (n.justDragged) return;
      if (isServer){ showPop(disc, serverPop(ref.data), true); return; }
      hidePop();
      focusDevice(ref.data.id);
    });
    return ref;
  };

  if (main){
    node('s:' + main.id, {x: cx, y: cy}, 23, GLYPH.main, main.name, main.meshHost ? main.meshHost + '.mesh' : t('srv_main'), main, true, true);
  }
  secs.forEach((s, i) => {
    const a = i * 2 * Math.PI / secs.length;
    node('s:' + s.id, {x: cx + rx * Math.cos(a), y: cy + ry * Math.sin(a)}, 17, GLYPH.server, s.name,
      s.stale ? ageText(s.age || 0) : (s.meshHost ? s.meshHost + '.mesh' : (s.host || '')), s, true, false);
  });
  // Линии между серверами — из MESH.serverLinks (ТЗ 24); старая панель без
  // них — как раньше, от этого сервера к каждому.
  const links = (MESH && MESH.serverLinks) || [];
  if (links.length){
    links.forEach(l => {
      const a = sim.byKey['s:' + l.a], b = sim.byKey['s:' + l.b];
      if (a && b) MAP_REFS['l:' + l.a + '|' + l.b] = mapEdge(sim, a, b, 'srv', true);
    });
  } else if (main){
    secs.forEach(s => { MAP_REFS['l:' + main.id + '|' + s.id] = mapEdge(sim, sim.byKey['s:' + main.id], sim.byKey['s:' + s.id], 'srv', true); });
  }
  devices.forEach((d, j) => {
    const ids = mapServersOf(d).filter(id => MAP_REFS['s:' + id]);
    if (!ids.length && main) ids.push(main.id);
    if (!ids.length) return;
    const sp = MAP_REFS['s:' + ids[0]].n, a = j * 2.4 + 0.7; // золотой угол — соседи не друг на друге
    const rr = narrow ? 80 : 115;
    const r = node('d:' + d.id, {x: sp.x + rr * Math.cos(a), y: sp.y + rr * 0.8 * Math.sin(a)}, narrow ? 10.5 : 12.5,
      glyphOf(d.platform), d.display, d.host + '.mesh', d, false, false);
    // Линия к каждому серверу, с которым устройство на связи (ТЗ 24).
    r.edges = ids.map(id => ({e: mapEdge(sim, r.n, MAP_REFS['s:' + id].n, 'dev', !many), srv: id}));
    r.edge = r.edges[0].e;
  });
  // Соединения, пересланные между серверами (meshd 7): пунктир.
  ((MESH && MESH.relays) || []).filter(x => x.net === MESH_NET).forEach(x => {
    const a = sim.byKey['d:' + x.from];
    const b = sim.byKey[x.to.startsWith('srv:') ? 's:' + x.to.slice(4) : 'd:' + x.to];
    if (!a || !b) return;
    const e = mapEdge(sim, a, b, 'relay', false);
    svgEl('title', {}, e.g).textContent = fill('relay_title', {a: serverName(x.a), b: serverName(x.b)});
  });
  if (!servers.length){
    const tx = svgEl('text', {x: cx, y: cy, 'text-anchor': 'middle', fill: 'var(--dim)'}, view);
    tx.textContent = '—';
  }
  // Новые узлы сначала «досчитываются» без показа, чтобы схема не
  // разлеталась на глазах; дальше она успокаивается уже анимацией.
  sim.alpha = fresh ? 1 : 0.3;
  if (fresh) for (let i = 0; i < 120; i++) simTick(sim);
  simDraw(sim);
  kickSim(fresh ? 0.25 : 0.3);
}

function popBox(title, rows){
  const box = el('div');
  box.appendChild(el('p', '', '')).appendChild(el('b', '', title));
  rows.forEach(([k, v]) => {
    if (v === undefined || v === null || v === '') return;
    const p = el('p');
    p.appendChild(el('span', '', k + ': '));
    p.appendChild(el('span', '', String(v)));
    box.appendChild(p);
  });
  return box;
}
function devicePop(d){
  return popBox(d.display, [
    ['.mesh', d.host + '.mesh'],
    [t('dev_ip'), d.ip],
    [t('dev_platform'), systemText(d)],
    [t('dev_mode'), d.mode ? t('mode_' + d.mode) : ''],
    [t('dev_latency'), d.online && d.rttMs ? fmtMs(d.rttMs) + ' · ' + t('q_' + d.quality) : ''],
    [t('dev_conns'), connText(d)],
    [t('dev_direct'), d.online ? directText(d) : ''],
    [d.online ? t('dev_since') : t('dev_last'), d.online ? fmtAgo(d.connectedAt) : fmtClock(d.lastSeen)],
  ]);
}
function serverPop(s){
  const l = s.link;
  return popBox(s.name, [
    ['host', s.host],
    ['.mesh', s.meshHost ? s.meshHost + '.mesh · ' + s.meshIp : ''],
    [t('status_label'), s.online ? t('sec_connected') : (s.stale ? fill('srv_stale', {t: ageText(s.age || 0)}) : t('srv_offline'))],
    [t('dev_latency'), l && l.rttMs ? fmtMs(l.rttMs) + ' · ' + t('q_' + l.quality) : (s.rttMs ? fmtMs(s.rttMs) : '')],
    [t('nav_devices'), s.noView ? t('srv_noview') : ''],
    [t('dev_since'), l ? fmtAgo(l.connectedAt) : ''],
  ]);
}
/* ---------- состояние сервера: коротко (ТЗ 27) ---------- */
// Процессор, память и самый заполненный диск — из того же
// GET /api/server/stats, что у «Мониторинга». Опрос раз в 10 с, только пока
// открыт «Обзор» и вкладка браузера видна.
let OVS = null, OVS_TIMER = null;
async function loadServerSummary(){
  clearTimeout(OVS_TIMER);
  if (TAB !== 'Home' || $('viewMain').hidden) return;
  const {ok, data} = await api('/api/server/stats');
  $('ovsErr').hidden = ok;
  if (!ok) $('ovsErr').textContent = errOf(data);
  else { OVS = data; renderServerSummary(); }
  OVS_TIMER = setTimeout(() => { if (!document.hidden) loadServerSummary(); else OVS_TIMER = setTimeout(loadServerSummary, 2000); }, 10000);
}
// ovsLevel — цвет по загрузке, как в «Мониторинге»: от 70 % жёлтый, от 90 % красный.
function ovsLevel(p){ return p >= 90 ? 'err' : p >= 70 ? 'warn' : 'ok'; }
function ovsPct(used, total){ return total ? used / total * 100 : 0; }
function ovsTile(id, pct, sub, none){
  const box = $(id);
  box.classList.remove('ok', 'warn', 'err');
  box.classList.add(ovsLevel(pct));
  box.querySelector('.v').textContent = none ? '—' : (pct < 10 ? pct.toFixed(1) : Math.round(pct)) + ' %';
  box.querySelector('.ovs-bar i').style.width = Math.min(100, none ? 0 : pct) + '%';
  const s = box.querySelector('.s');
  s.textContent = sub;
  s.title = sub;
}
function renderServerSummary(){
  const s = OVS;
  if (!s) return;
  const cpu = s.cpu || {}, mem = s.mem || {};
  ovsTile('ovsCpu', cpu.percent || 0, s.ready ? fill('ovs_cores', {n: cpu.count || '—'}) : t('ovs_collecting'), !s.ready);
  ovsTile('ovsMem', ovsPct(mem.used, mem.total), fmtBytes(mem.used) + ' / ' + fmtBytes(mem.total), !mem.total);
  const disk = (s.disks || []).slice().sort((a, b) => ovsPct(b.used, b.total) - ovsPct(a.used, a.total))[0];
  if (disk) ovsTile('ovsDisk', ovsPct(disk.used, disk.total), disk.mount + ' · ' + fmtBytes(disk.used) + ' / ' + fmtBytes(disk.total), false);
  else ovsTile('ovsDisk', 0, t('ovs_none'), true);
}
const openMonitor = () => showTab('Monitor');
$('ovsTiles').addEventListener('click', openMonitor);
$('ovsTiles').addEventListener('keydown', e => { if (e.key === 'Enter' || e.key === ' '){ e.preventDefault(); openMonitor(); } });
$('ovsMore').addEventListener('click', openMonitor);
document.addEventListener('visibilitychange', () => { if (!document.hidden && TAB === 'Home') loadServerSummary(); });

// fitOverview — где на странице начинается «Обзор»: первый экран
// (плитки, схема, клиенты) кончается у низа окна, даже когда над разделом
// плашка режима показа или ошибка службы сети.
function fitOverview(){
  const sec = $('tabHome');
  if (sec.hidden) return;
  const y = Math.round(sec.getBoundingClientRect().top + scrollY);
  if (sec.style.getPropertyValue('--ov-top') !== y + 'px') sec.style.setProperty('--ov-top', y + 'px');
}
let MAP_RESIZE = null;
addEventListener('resize', () => {
  clearTimeout(MAP_RESIZE);
  MAP_RESIZE = setTimeout(() => { fitOverview(); placeMap(); if (MESH) renderMesh(); }, 150);
});

/* ---------- события каркаса ---------- */
onPanel('lang', () => { MAP_SIG = ''; renderTraffic(); renderServerSummary(); });
onPanel('show:Home', () => { fitOverview(); loadServerSummary(); });
onPanel('start', fitOverview);
onPanel('remap', () => { MAP_SIG = ''; });
onPanel('show', () => placeMap());
onPanel('start', loadTraffic);
onPanel('refresh', loadTraffic);
onPanel('clients', () => renderHomeClients());
onPanel('status', st => {
  $('stOS').textContent = st.os;
  $('stUptime').textContent = fmtUptime(st.uptimeSeconds);
  $('stVersion').textContent = st.version;
});
onPanel('mesh', (m, role) => {
  fitOverview();
  const isMain = role === 'main';
  $('meshTiles').hidden = !isMain;
  $('mapCard').hidden = !isMain;
  $('tabHome').classList.toggle('nomap', !isMain);
  if (isMain) renderOverviewMesh(m);
});
