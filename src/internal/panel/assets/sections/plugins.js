// Раздел «Плагины» (ТЗ 29) — как в программе на устройстве: сетка карточек
// добавленных плагинов, «+» открывает каталог, карточка — страницу плагина
// со всеми его настройками и данными по сетям. Экраны — «вкладки» каркаса
// с адресом #plugins/grid, #plugins/add, #plugins/<плагин>; переход с сетки
// пишет запись в историю (location.hash), поэтому «Назад» браузера и мыши
// возвращает на сетку. Добавить плагин — включить его у хоста
// (/api/node/config), убрать — /api/node/plugin/remove (с данными или без).
let NODE = null, NODE_TIMER = null;

const PL_LIST = ['mirror', 'files', 'clipboard', 'backup', 'machines', 'adblock', 'sitecheck'];
// Значки плагинов (24×24, линией) и кнопок экрана.
const PL_PATH = {
  mirror: 'M3 6.5h6l2 2h10V19H3zM8.5 13.5h7M13 11l2.5 2.5L13 16',
  files: 'M6 3h8l4 4v14H6zM14 3v4h4M9 12h6M9 16h6',
  clipboard: 'M9 3.5h6v3H9zM7.5 5H5v16h14V5h-2.5M8.5 12h7M8.5 16h5',
  backup: 'M20.5 12a8.5 8.5 0 1 1-2.5-6M20.5 4v4.5H16M12 8v4.5l3 2',
  machines: 'M3 4h18v12H3zM8 20h8M12 16v4M6.5 12l3-3 3 2.5 4.5-4.5',
  adblock: 'M12 3l8 3v6c0 4.8-3.4 8-8 9-4.6-1-8-4.2-8-9V6zM8.5 8.5l7 7',
  sitecheck: 'M12 3a9 9 0 1 0 0 18a9 9 0 1 0 0-18M3 12h18M12 3c3.2 3.2 3.2 14.8 0 18M12 3c-3.2 3.2-3.2 14.8 0 18',
  plus: 'M12 5v14M5 12h14',
  back: 'M15 5l-7 7 7 7',
  more: 'M5 12h.01M12 12h.01M19 12h.01',
};
function plSvg(d){
  const s = document.createElementNS(NS, 'svg');
  s.setAttribute('viewBox', '0 0 24 24');
  s.setAttribute('fill', 'none');
  s.setAttribute('stroke', 'currentColor');
  s.setAttribute('stroke-width', '1.8');
  s.setAttribute('stroke-linecap', 'round');
  s.setAttribute('stroke-linejoin', 'round');
  const p = document.createElementNS(NS, 'path');
  p.setAttribute('d', d);
  s.appendChild(p);
  return s;
}
function plIcon(p){
  const box = el('span', 'pl-ic');
  box.appendChild(plSvg(PL_PATH[p]));
  return box;
}
const plState = () => (NODE && NODE.unit && NODE.unit.installed && NODE.state) || null;
const plOn = p => { const st = plState(); return !!(st && st.config[p]); };

/* ---------- сети ---------- */
// Сети из meshd по порядку: безымянная — «Сеть N». gone — данные сети на
// сервере есть, а самой сети в meshd уже нет (только когда список сетей
// известен).
function meshNets(){ return (MESH && MESH.effectiveRole === 'main' && !MESH.adminError && MESH.networks) || null; }
function netName(id){
  const nets = (MESH && MESH.networks) || [];
  const i = nets.findIndex(x => x.id === id);
  if (i < 0) return id.slice(0, 8);
  return nets[i].name || t('node_net_unnamed').replace('{n}', i + 1);
}
function netGone(id){
  const nets = meshNets();
  return !!nets && !nets.some(x => x.id === id);
}
// plNets — живые сети сервера с данными плагинов: и те, что уже пользовались
// плагинами, и те, что ещё нет (в них тоже можно завести папку).
function plNets(st){
  const out = (st.networks || []).filter(n => !netGone(n.id));
  (meshNets() || []).forEach(m => {
    if (!out.some(n => n.id === m.id)) out.push({id: m.id, folders: []});
  });
  const order = ((MESH && MESH.networks) || []).map(n => n.id);
  return out.sort((a, b) => (order.indexOf(a.id) + 1 || 999) - (order.indexOf(b.id) + 1 || 999));
}

/* ---------- переходы между экранами ---------- */
// PL_PUSHED — экран открыт с сетки (запись в истории есть): «назад» на
// странице — тот же шаг истории, что и кнопка браузера.
let PL_PUSHED = false;
function plGo(sub){
  PL_PUSHED = true;
  location.hash = '#plugins/' + sub;
}
function plHome(){
  if (SUB.Plugins === 'grid') return;
  if (PL_PUSHED){ PL_PUSHED = false; history.back(); return; }
  history.replaceState(null, '', '#plugins/grid');
  showSub('Plugins', 'grid');
}
// Пункт меню «Плагины» всегда ведёт на сетку, а не на последний плагин.
document.addEventListener('click', e => {
  if (e.target.closest('.side button[data-tab="Plugins"]')){ SUB.Plugins = 'grid'; PL_PUSHED = false; }
}, true);
document.querySelectorAll('[data-pl-back]').forEach(b => {
  b.appendChild(plSvg(PL_PATH.back));
  b.addEventListener('click', plHome);
});

/* ---------- опрос ---------- */
async function loadNode(){
  clearTimeout(NODE_TIMER);
  if (TAB !== 'Plugins') return;
  const {ok, status, data} = await api('/api/node/state');
  if (ok) { NODE = data; renderNode(); }
  else if (status === 404) { NODE = null; renderNode(); }
  scheduleNode();
}
// Опрос — только пока раздел открыт и окно видно.
function scheduleNode(){
  clearTimeout(NODE_TIMER);
  if (TAB !== 'Plugins') return;
  const fast = NODE && NODE.job && NODE.job.running;
  NODE_TIMER = setTimeout(() => {
    if (document.hidden){ scheduleNode(); return; }
    loadNode();
  }, fast ? 1500 : 4000);
}
document.addEventListener('visibilitychange', () => { if (!document.hidden && TAB === 'Plugins') loadNode(); });

/* ---------- всё вместе ---------- */
function renderNode(){
  renderHostBar();
  renderNodeJob(NODE && NODE.job);
  const st = plState();
  renderNodeWarnings(st ? NODE.warnings : null);
  renderGone(st);
  renderGrid(st);
  renderCat(st);
  renderHostCard(st);
  const sub = SUB.Plugins;
  if (!PL_LIST.includes(sub)) return;
  // Плагина нет (хост не стоит, плагин убрали) — его страницы тоже.
  if (NODE && (!st || !st.config[sub])){
    PL_PUSHED = false;
    history.replaceState(null, '', '#plugins/grid');
    showSub('Plugins', 'grid');
    return;
  }
  if (st) renderPage(sub, st);
}

/* ---------- хост плагинов: строка над сеткой и карточка на «+» ---------- */
function plHostText(){
  if (!NODE) return t('pl_host_none');
  if (!NODE.unit || !NODE.unit.installed) return t('svc_missing');
  if (!NODE.state) return t('pl_host_down');
  const meshd = NODE.state.meshd || 'unknown';
  return (NODE.unit.active ? t('svc_active') : t('svc_stopped')) + ' · ' + t('node_meshd') + ': ' + t('node_meshd_' + meshd);
}
function renderHostBar(){
  const installed = !!(NODE && NODE.unit && NODE.unit.installed);
  const working = !!(NODE && NODE.job && NODE.job.running);
  $('plHostState').textContent = plHostText();
  $('nodeInstallBtn').hidden = !NODE || installed;
  $('nodeInstallBtn').disabled = working;
  $('plHostMore').hidden = !installed;
  const err = $('nodeErr');
  err.hidden = !(installed && NODE.error);
  if (!err.hidden) err.textContent = NODE.error;
  const st = plState();
  $('plUseBox').hidden = !st;
  if (!st) return;
  const quota = (st.config.quotaMB || 0) * 1048576;
  const k = quota ? Math.min(1, st.usedBytes / quota) : 0;
  const fillEl = $('plUseFill');
  fillEl.style.width = (k * 100).toFixed(1) + '%';
  fillEl.classList.toggle('full', k > 0.9);
  $('plUseText').textContent = fill('pl_used_of', {u: fmtBytes(st.usedBytes), q: fmtBytes(quota)});
}
function renderHostCard(st){
  const installed = !!(NODE && NODE.unit && NODE.unit.installed);
  const working = !!(NODE && NODE.job && NODE.job.running);
  $('nodeHostCard').hidden = !NODE;
  $('nodeSvcState').textContent = plHostText();
  $('nodeInstallBtn2').hidden = installed;
  $('nodeReinstallBtn').hidden = !installed;
  $('nodeRemoveBtn').hidden = !installed;
  $('nodeRemoveDataBtn').hidden = !installed;
  $('nodeInstallBtn2').disabled = $('nodeReinstallBtn').disabled = working;
  $('nodeMeshdRow').hidden = true; // состояние meshd — уже в строке статуса
  $('nodeUsedRow').hidden = !st;
  $('nodeQuotaRow').hidden = !st;
  if (st){
    $('nodeUsed').textContent = fill('pl_used_of', {u: fmtBytes(st.usedBytes), q: fmtBytes((st.config.quotaMB || 0) * 1048576)});
    if (document.activeElement !== $('nodeQuota')) $('nodeQuota').value = st.config.quotaMB || '';
  }
  const evCard = $('nodeEventsCard');
  const evs = (st && st.events) || [];
  evCard.hidden = !evs.length;
  if (evs.length) plJournal($('nodeEvents'), evs);
}
function plJournal(box, evs){
  box.innerHTML = '';
  evs.slice().reverse().forEach(e => {
    box.appendChild(el('div', 'l' + (e.error ? ' err' : ''),
      new Date(e.time * 1000).toLocaleString(LANG === 'ru' ? 'ru-RU' : 'en-US') + '  ' +
      (e.net ? '[' + netName(e.net) + '] ' : '') + e.text));
  });
}

// Журнал установки хоста (ТЗ 34) — внутри карточки с кнопкой: на сетке —
// строка хоста, на «+» — карточка «Хост плагинов». Увиденный журнал
// остаётся до перезагрузки или «×».
let NODE_JOB_SEEN = '', NODE_JOB_HIDDEN = '';
$('nodeJobClose').addEventListener('click', () => {
  NODE_JOB_HIDDEN = NODE_JOB_SEEN;
  $('nodeJob').hidden = true;
});
function renderNodeJob(job){
  const box = $('nodeJob'), host = SUB.Plugins === 'add' ? $('nodeHostCard') : $('plHostBar');
  if (box.parentNode !== host) host.appendChild(box);
  const recent = job && job.started !== NODE_JOB_HIDDEN && (job.running || job.error || job.started === NODE_JOB_SEEN ||
    (job.finished && Date.now() - new Date(job.finished) < 60000));
  box.hidden = !recent;
  if (!recent) return;
  NODE_JOB_SEEN = job.started;
  const title = $('nodeJobTitle');
  title.innerHTML = '';
  if (job.running){
    title.appendChild(el('span', 'spin'));
    title.appendChild(document.createTextNode(t('node_job_install')));
  } else {
    title.textContent = job.error ? t('job_failed') + ': ' + job.error : t('job_done');
  }
  const log = $('nodeJobLog');
  log.innerHTML = '';
  (job.lines || []).forEach(l => log.appendChild(el('div', 'l', l)));
  log.scrollTop = log.scrollHeight;
}

function renderNodeWarnings(ws){
  const card = $('nodeWarnCard'), box = $('nodeWarns');
  card.hidden = !ws || !ws.length;
  box.innerHTML = '';
  (ws || []).forEach(w => {
    const m = el('div', 'msg ' + (['access_rules', 'no_mesh', 'adblock_no_sources', 'adblock_failed'].includes(w.code) ? 'warn' : 'err'),
      t('warn_' + w.code).replace('{ports}', w.ports || '').replace('{users}', (w.users || []).join(', ')).replace('{detail}', w.detail || ''));
    box.appendChild(m);
    if (w.fix){
      const row = el('div', 'row-inline');
      row.style.margin = '-4px 0 12px';
      const b = el('button', 'btn small', t('warn_fix'));
      b.title = t('warn_fix_hint');
      b.addEventListener('click', async () => {
        b.disabled = true;
        const {ok, data} = await api('/api/node/fix', {code: w.code});
        b.disabled = false;
        if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
        toast(t('warn_fixed'), 'ok');
        loadNode();
        loadMesh();
      });
      row.appendChild(b);
      row.appendChild(el('span', 'note', t('warn_fix_hint')));
      box.appendChild(row);
    } else if (w.code === 'access_rules'){
      const b = el('button', 'btn ghost small', t('warn_go_access'));
      b.style.margin = '-4px 0 12px';
      b.addEventListener('click', () => showTab('Access'));
      box.appendChild(b);
    }
  });
}

// Данные удалённых сетей — над сеткой: сами не стираются, решает человек.
function renderGone(st){
  const box = $('plGone');
  const gone = st ? (st.networks || []).filter(n => netGone(n.id)) : [];
  const sig = gone.map(n => n.id + ':' + n.usedBytes).join(',') + LANG;
  if (box.dataset.sig === sig) return;
  box.dataset.sig = sig;
  box.innerHTML = '';
  gone.forEach(n => {
    const card = el('div', 'card gone');
    card.appendChild(el('h1', '', t('node_net_gone') + ' · ' + netName(n.id)));
    let bytes = (n.usedBytes || 0) + (n.backupBytes || 0);
    (n.folders || []).forEach(f => { bytes += f.usedBytes || 0; });
    card.appendChild(el('p', 'note', t('node_net_gone_hint') + ' ' + fill('pl_gone_size', {s: fmtBytes(bytes)})));
    const del = el('button', 'btn danger small', t('node_net_delete'));
    del.addEventListener('click', async () => {
      const yes = await appConfirm(t('confirm_net_delete').replace('{n}', netName(n.id)), t('node_net_delete'));
      if (!yes) return;
      const {ok, data} = await api('/api/node/net/delete', {net: n.id});
      if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
      loadNode();
    });
    card.appendChild(del);
    box.appendChild(card);
  });
}

/* ---------- сетка карточек ---------- */
// plStatus — одна строка под именем: работает ли и сколько занято.
function plStatus(p, st){
  if (st.listening && !st.listening[p]) return [t('pl_st_down'), true];
  // Данные удалённых сетей не считаются: они — отдельной карточкой над сеткой.
  const nets = (st.networks || []).filter(n => !netGone(n.id));
  const sum = f => nets.reduce((a, n) => a + (f(n) || 0), 0);
  switch (p){
    case 'mirror': {
      const n = sum(x => (x.folders || []).length);
      return [n ? fill('pl_st_folders', {n, s: fmtBytes(sum(x => (x.folders || []).reduce((a, f) => a + f.usedBytes, 0)))}) : t('pl_st_nofolders'), false];
    }
    case 'files': { const b = sum(x => x.filesBytes); return [b ? fill('pl_st_used', {s: fmtBytes(b)}) : t('pl_st_working'), false]; }
    case 'clipboard': { const n = sum(x => x.clipboard && x.clipboard.items); return [n ? fill('pl_st_items', {n}) : t('pl_st_working'), false]; }
    case 'backup': {
      const n = sum(x => (x.backups || []).length);
      return [n ? fill('pl_st_copies', {n, s: fmtBytes(sum(x => x.backupBytes))}) : t('pl_st_nocopies'), false];
    }
    case 'machines': { const b = sum(x => x.machines && x.machines.bytes); return [b ? fill('pl_st_used', {s: fmtBytes(b)}) : t('pl_st_working'), false]; }
    case 'adblock': {
      const ad = st.adblock || {};
      if (ad.updating) return [t('node_ad_updating'), false];
      return [ad.version ? fill('pl_st_names', {n: ad.names}) : t('node_ad_none'), !!ad.error];
    }
  }
  return [t('pl_st_working'), false];
}
let PL_MENU = null;
function plCloseMenu(){ if (PL_MENU){ PL_MENU.remove(); PL_MENU = null; } }
document.addEventListener('click', e => { if (PL_MENU && !e.target.closest('.pl-menu, .pl-tile .more')) plCloseMenu(); });
document.addEventListener('keydown', e => { if (e.key === 'Escape') plCloseMenu(); });

function renderGrid(st){
  const g = $('plGrid');
  if (!st){ g.innerHTML = ''; g.dataset.sig = ''; return; }
  const added = PL_LIST.filter(p => st.config[p]);
  const sig = LANG + '|' + added.map(p => p + ':' + plStatus(p, st).join(':')).join(',');
  if (g.dataset.sig === sig || PL_MENU) return;
  g.dataset.sig = sig;
  g.innerHTML = '';
  added.forEach(p => {
    const [text, warn] = plStatus(p, st);
    const tile = el('div', 'pl-tile');
    tile.tabIndex = 0;
    tile.setAttribute('role', 'link');
    tile.appendChild(plIcon(p));
    tile.appendChild(el('span', 'nm', t('plugin_' + p)));
    tile.appendChild(el('span', 'st' + (warn ? ' warn' : ''), text));
    const more = el('button', 'icon-btn more');
    more.type = 'button';
    more.title = t('pl_more');
    more.setAttribute('aria-label', t('pl_more'));
    more.appendChild(plSvg(PL_PATH.more));
    more.addEventListener('click', e => {
      e.stopPropagation();
      const open = PL_MENU && PL_MENU.parentNode === tile;
      plCloseMenu();
      if (open) return;
      const m = el('div', 'pl-menu');
      [['pl_open', () => plGo(p)], ['pl_remove', () => plRemove(p, false)], ['pl_remove_data', () => plRemove(p, true), 'danger']].forEach(([k, fn, cls]) => {
        const b = el('button', cls || '', t(k));
        b.type = 'button';
        b.addEventListener('click', ev => { ev.stopPropagation(); plCloseMenu(); fn(); });
        m.appendChild(b);
      });
      tile.appendChild(m);
      PL_MENU = m;
    });
    tile.appendChild(more);
    tile.addEventListener('click', () => plGo(p));
    tile.addEventListener('keydown', e => { if (e.key === 'Enter' && e.target === tile) plGo(p); });
    g.appendChild(tile);
  });
  // Последняя карточка — «+».
  const plus = el('button', 'pl-tile plus');
  plus.type = 'button';
  plus.appendChild(plSvg(PL_PATH.plus));
  plus.appendChild(el('span', 'nm', added.length < PL_LIST.length ? t('sub_pl_add') : t('pl_all_added')));
  plus.addEventListener('click', () => plGo('add'));
  g.appendChild(plus);
}

/* ---------- каталог ---------- */
function renderCat(st){
  const box = $('plCat');
  const sig = LANG + '|' + (st ? PL_LIST.map(p => st.config[p] ? 1 : 0).join('') : '-');
  if (box.dataset.sig === sig) return;
  box.dataset.sig = sig;
  box.innerHTML = '';
  if (!st) box.appendChild(el('div', 'msg warn', t('pl_need_host')));
  PL_LIST.forEach(p => {
    const has = !!(st && st.config[p]);
    const it = el('div', 'pl-item');
    it.appendChild(plIcon(p));
    const tx = el('div', 'tx');
    tx.appendChild(el('div', 'nm', t('plugin_' + p)));
    tx.appendChild(el('div', 'd', t('pl_desc_' + p)));
    it.appendChild(tx);
    const b = el('button', 'btn' + (has ? ' ghost' : ''), has ? t('pl_added') : t('pl_add'));
    b.type = 'button';
    b.disabled = has || !st;
    b.addEventListener('click', () => plAdd(p, b));
    it.appendChild(b);
    box.appendChild(it);
  });
}
async function plAdd(p, btn){
  btn.disabled = true;
  const {ok, data} = await api('/api/node/config', {[p]: true});
  if (!ok){ btn.disabled = false; toast(data.error || t('err_internal'), 'err'); return; }
  if (NODE && data && data.config) NODE.state = data;
  toast(fill('pl_added_toast', {n: t('plugin_' + p)}), 'ok');
  plHome();
  renderNode();
  loadNode();
}

/* ---------- убрать плагин ---------- */
async function plRemove(p, withData){
  const name = t('plugin_' + p);
  const yes = await appConfirm(fill(withData ? 'confirm_pl_remove_data' : 'confirm_pl_remove', {n: name}),
    t(withData ? 'pl_remove_data' : 'pl_remove'));
  if (!yes) return;
  const {ok, data} = await api('/api/node/plugin/remove', {plugin: p, deleteData: withData});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); loadNode(); return; }
  if (NODE && data && data.config) NODE.state = data;
  toast(fill('pl_removed', {n: name}), 'ok');
  if (SUB.Plugins === p) plHome();
  renderNode();
  loadNode();
}

/* ---------- страница плагина ---------- */
function nodeRow(main, sub){
  const row = el('div', 'nrow');
  const g = el('div', 'grow');
  g.appendChild(el('div', '', main));
  if (sub) g.appendChild(el('div', 'sub', sub));
  row.appendChild(g);
  return row;
}
// plPart — свой блок кода внутри страницы (до или после постоянных карточек).
function plPart(page, cls, first){
  let box = page.querySelector(':scope > .pl-dyn.' + cls);
  if (!box){
    box = el('div', 'pl-dyn ' + cls);
    if (first) page.prepend(box); else page.appendChild(box);
  }
  return box;
}
function renderPage(p, st){
  const page = document.querySelector('.pl-page[data-pl="' + p + '"]');
  if (p === 'adblock') renderNodeAd(st);
  // Не перерисовывать, пока в странице что-то вводят (квота, имя папки).
  const ae = document.activeElement;
  if (page.contains(ae) && ['INPUT', 'TEXTAREA', 'SELECT'].includes(ae.tagName)) return;
  const top = plPart(page, 'top', true), bot = plPart(page, 'bot', false);
  top.innerHTML = '';
  bot.innerHTML = '';
  top.appendChild(plHead(p, st));
  top.appendChild(plAbout(p, st));
  if (['mirror', 'files', 'clipboard', 'backup', 'machines'].includes(p)){
    const nets = plNets(st);
    if (!nets.length) bot.appendChild(plNote(t('pl_no_nets')));
    nets.forEach(n => bot.appendChild(plNetCard(p, n)));
  }
  const evs = (st.events || []).filter(e => e.plugin === p);
  if (evs.length){
    const card = el('div', 'card');
    card.appendChild(el('h1', '', t('node_events_title')));
    const box = el('div', 'console');
    plJournal(box, evs);
    card.appendChild(box);
    bot.appendChild(card);
  }
}
function plNote(text){
  const c = el('div', 'card');
  c.appendChild(el('p', 'note', text));
  c.firstChild.style.margin = '0';
  return c;
}
function plHead(p, st){
  const head = el('div', 'pl-head');
  const back = el('button', 'icon-btn pl-back');
  back.type = 'button';
  back.title = t('pl_back');
  back.setAttribute('aria-label', t('pl_back'));
  back.appendChild(plSvg(PL_PATH.back));
  back.addEventListener('click', plHome);
  head.appendChild(back);
  head.appendChild(plIcon(p));
  const tt = el('div', '');
  const h1 = el('h1', 'pl-name', t('plugin_' + p));
  const hint = $('plHints').content.querySelector('[data-hint="hint_plugin_' + p + '"]');
  if (hint) h1.appendChild(hint.cloneNode(true));
  tt.appendChild(h1);
  const [text, warn] = plStatus(p, st);
  tt.appendChild(el('div', 'pl-st' + (warn ? ' warn' : ''), text));
  head.appendChild(tt);
  head.appendChild(el('span', 'sp'));
  const rm = el('button', 'btn ghost small', t('pl_remove'));
  rm.addEventListener('click', () => plRemove(p, false));
  const rmd = el('button', 'btn danger small', t('pl_remove_data'));
  rmd.addEventListener('click', () => plRemove(p, true));
  head.appendChild(rm);
  head.appendChild(rmd);
  return head;
}
// plAbout — что делает плагин, кому он доступен, его собственные настройки.
function plAbout(p, st){
  const card = el('div', 'card');
  card.appendChild(el('p', 'note', t('pl_desc_' + p)));
  card.appendChild(el('div', 'nsec', t('pl_nets_title')));
  const one = p === 'adblock' || p === 'sitecheck';
  card.appendChild(el('p', 'note', t(one ? 'pl_nets_one' : 'pl_nets_all')));
  const nets = plNets(st);
  if (nets.length){
    const la = nets.map(n => netName(n.id) + ' — ' + ((n.lastActive || {})[p] ? fmtAgo(n.lastActive[p]) : t('node_never')));
    card.appendChild(el('div', 'pl-last', t('node_last') + ': ' + la.join(', ')));
  }
  if (p === 'files'){
    const lab = el('label', 'chk');
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = !!st.config.filesWrite;
    cb.addEventListener('change', async () => {
      cb.disabled = true;
      const {ok, data} = await api('/api/node/config', {filesWrite: cb.checked});
      cb.disabled = false;
      if (!ok){ cb.checked = !cb.checked; toast(data.error || t('err_internal'), 'err'); return; }
      cb.blur();
      loadNode();
    });
    lab.appendChild(cb);
    lab.appendChild(el('span', '', t('plugin_files_write')));
    card.appendChild(lab);
  }
  if (p === 'machines'){
    const row = el('div', 'row-inline pl-quota');
    const lab = el('label', '', t('node_mach_days'));
    lab.htmlFor = 'nodeMachDays';
    const inp = document.createElement('input');
    inp.type = 'number'; inp.id = 'nodeMachDays'; inp.min = '1'; inp.max = '3650';
    inp.value = st.config.machinesDays || 30;
    const save = el('button', 'btn ghost small', t('folder_quota_save'));
    save.addEventListener('click', async () => {
      const {ok, data} = await api('/api/node/config', {machinesDays: Number(inp.value) || 0});
      if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
      toast(t('pl_saved'), 'ok');
      inp.blur();
      loadNode();
    });
    row.appendChild(lab); row.appendChild(inp); row.appendChild(save);
    card.appendChild(row);
  }
  return card;
}

// plNetCard — данные плагина в одной сети (раньше — «Сети на сервере»).
function plNetCard(p, n){
  const card = el('div', 'card');
  card.appendChild(el('h1', '', netName(n.id)));
  if (p === 'mirror') plMirror(card, n);
  if (p === 'files') card.appendChild(nodeRow(n.filesBytes ? fmtBytes(n.filesBytes) : t('pl_files_empty'), t('pl_files_note')));
  if (p === 'clipboard') plClipboard(card, n);
  if (p === 'machines') plMachines(card, n);
  if (p === 'backup') plBackups(card, n);
  return card;
}

function plMirror(card, n){
  (n.folders || []).forEach(f => {
    const row = nodeRow(f.name, fmtBytes(f.usedBytes) + ' / ' + fmtBytes(f.quotaMB * 1048576) + ' · ' + f.files + ' ' +
      t('folder_files') + (f.conflicts ? ' · ' + f.conflicts + ' ' + t('folder_conflicts') : ''));
    const q = document.createElement('input');
    q.type = 'number'; q.min = '1'; q.value = f.quotaMB; q.title = t('folder_quota_label');
    q.setAttribute('aria-label', t('folder_quota_label') + ': ' + f.name);
    const save = el('button', 'btn ghost small', t('folder_quota_save'));
    save.addEventListener('click', async () => {
      const {ok, data} = await api('/api/node/folder/quota', {net: n.id, id: f.id, quotaMB: Number(q.value) || 0});
      if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
      toast(t('folder_quota_saved'), 'ok');
      q.blur();
      loadNode();
    });
    const rm = el('button', 'btn ghost small', t('folder_remove'));
    rm.addEventListener('click', async () => {
      const yes = await appConfirm(t('confirm_folder_remove').replace('{n}', f.name), t('folder_remove'));
      if (!yes) return;
      const {ok, data} = await api('/api/node/folder/remove', {net: n.id, id: f.id});
      if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
      loadNode();
    });
    row.querySelector('.grow').appendChild(el('div', 'sub', nodeVisibleTo(n, f)));
    row.appendChild(q);
    row.appendChild(save);
    row.appendChild(rm);
    card.appendChild(row);
  });
  if (!(n.folders || []).length) card.appendChild(el('p', 'note', t('pl_no_folders')));
  const form = el('div', 'row-inline');
  form.style.marginTop = '8px';
  const nameInput = document.createElement('input');
  nameInput.type = 'text';
  nameInput.placeholder = t('folder_name_label');
  nameInput.style.flex = '1';
  nameInput.style.minWidth = '120px';
  const quotaInput = document.createElement('input');
  quotaInput.type = 'number';
  quotaInput.min = '1';
  quotaInput.value = '2048';
  quotaInput.title = t('folder_quota_label');
  quotaInput.style.width = '96px';
  const addBtn = el('button', 'btn ghost', t('node_add_folder'));
  const add = async () => {
    const name = nameInput.value.trim();
    if (!name) { nameInput.focus(); return; }
    const {ok, data} = await api('/api/node/folder/add', {net: n.id, name, quotaMB: Number(quotaInput.value) || 2048});
    if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
    nameInput.value = '';
    nameInput.blur();
    loadNode();
  };
  addBtn.addEventListener('click', add);
  nameInput.addEventListener('keydown', e => { if (e.key === 'Enter') add(); });
  form.appendChild(nameInput);
  form.appendChild(quotaInput);
  form.appendChild(addBtn);
  card.appendChild(form);
  renderNodeAccess(n, card);
}

function plClipboard(card, n){
  const c = n.clipboard || {};
  if (!c.items && !c.devices){ card.appendChild(el('p', 'note', t('pl_clip_none'))); return; }
  const row = nodeRow(c.items + ' ' + t('clip_items') + ' · ' + fmtBytes(c.bytes), c.devices + ' ' + t('clip_devices'));
  const clear = el('button', 'btn ghost small', t('clip_clear'));
  clear.addEventListener('click', async () => {
    const yes = await appConfirm(t('confirm_clip_clear'), t('clip_clear'));
    if (!yes) return;
    const {ok, data} = await api('/api/node/clipboard/clear', {net: n.id});
    if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
    loadNode();
  });
  row.appendChild(clear);
  card.appendChild(row);
}

function plMachines(card, n){
  const m = n.machines;
  if (!m || !m.reporters || !m.reporters.length){ card.appendChild(el('p', 'note', t('pl_mach_none'))); return; }
  card.appendChild(el('div', 'nsec', t('node_mach_title') + ' · ' + fmtBytes(m.bytes) +
    (m.oldest ? ' · ' + t('node_mach_since') + ' ' + new Date(m.oldest * 1000).toLocaleDateString(LANG === 'ru' ? 'ru-RU' : 'en-US') : '')));
  m.reporters.forEach(r => {
    card.appendChild(nodeRow((r.machines || []).join(', ') || '—',
      t('node_mach_from') + ' ' + (r.name || r.id.slice(0, 8)) + ' · ' + (r.shared ? t('node_mach_shared') : t('node_mach_private')) +
      (r.last ? ' · ' + fmtAgo(r.last) : '')));
  });
  const clr = el('button', 'btn ghost small', t('node_mach_clear'));
  clr.style.marginTop = '8px';
  clr.addEventListener('click', async () => {
    const yes = await appConfirm(t('confirm_mach_clear').replace('{n}', netName(n.id)), t('node_mach_clear'));
    if (!yes) return;
    const {ok, data} = await api('/api/node/machines/clear', {net: n.id});
    if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
    loadNode();
  });
  card.appendChild(clr);
}

function plBackups(card, n){
  const bs = n.backups || [];
  if (!bs.length){ card.appendChild(el('p', 'note', t('pl_backups_none'))); return; }
  card.appendChild(el('div', 'nsec', fmtBytes(n.backupBytes) + ' ' + t('backup_stored')));
  bs.forEach(b => {
    const dev = b.deviceName || b.device.slice(0, 8);
    const row = nodeRow(dev + ' — ' + b.name, fmtBytes(b.bytes) + ' · ' + b.versions + ' ' + t('backup_versions') +
      ' (' + t('backup_keep') + ' ' + b.keep + ') · ' + t('backup_last') + ' ' + fmtAgo(b.last));
    const del = el('button', 'btn ghost small', t('backup_delete'));
    del.addEventListener('click', async () => {
      const yes = await appConfirm(t('confirm_backup_delete').replace('{n}', b.name).replace('{d}', dev), t('backup_delete'));
      if (!yes) return;
      const {ok, data} = await api('/api/node/backup/delete', {net: n.id, scope: b.scope, device: b.device, set: b.set});
      if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
      loadNode();
    });
    row.appendChild(del);
    card.appendChild(row);
  });
}

// ---- Доступ к зеркальным папкам сервера (ТЗ 18) ----
const NODE_ACC_OPEN = {}; // «сеть/устройство» → открыт список папок

function nodeVisibleTo(n, f){
  if (f.old) return t('node_visible_all');
  const ds = f.devices || [];
  if (!ds.length) return t('node_visible_none');
  const devs = (NODE.access || {})[n.id] || [];
  return t('node_visible_to') + ds.map(id => { const d = devs.find(x => x.id === id); return d ? d.name : id; }).join(', ');
}

async function nodeSetAccess(n, f, d, on){
  const {ok, data} = await api('/api/node/folder/access', {net: n.id, folder: f, device: d.id, on});
  if (!ok){ toast(t('node_access_failed') + ': ' + (data.error || t('err_internal')), 'err'); return false; }
  return true;
}

function renderNodeAccess(n, card){
  const folders = n.folders || [];
  if (!folders.length) return;
  card.appendChild(el('div', 'nsec', t('node_access_title')));
  const devs = (NODE.access || {})[n.id];
  if (!devs){ card.appendChild(el('p', 'note', t('node_access_nomeshd'))); return; }
  card.appendChild(el('p', 'note', t('node_access_hint')));
  if (folders.some(f => f.old)) card.appendChild(el('div', 'msg warn', t('node_access_old')));
  if (!devs.length){ card.appendChild(el('p', 'note', t('node_access_nodev'))); return; }
  devs.forEach(d => {
    const key = n.id + '/' + d.id;
    const state = d.gone ? t('node_access_gone') : (d.online ? t('node_access_online') : t('node_access_offline'));
    const row = nodeRow(d.name, state + ' · ' + t('node_access_count').replace('{n}', d.folders).replace('{m}', folders.length));
    if (!d.online) row.style.opacity = '.65';
    const btn = el('button', 'btn ghost small', NODE_ACC_OPEN[key] ? t('node_access_hide') : t('node_access_edit'));
    btn.addEventListener('click', () => { NODE_ACC_OPEN[key] = !NODE_ACC_OPEN[key]; renderNode(); });
    row.appendChild(btn);
    card.appendChild(row);
    if (!NODE_ACC_OPEN[key]) return;
    const box = el('div', 'accbox');
    folders.forEach(f => {
      const lab = document.createElement('label');
      lab.className = 'acchk';
      const cb = document.createElement('input');
      cb.type = 'checkbox';
      cb.checked = !f.old && (f.devices || []).includes(d.id);
      lab.appendChild(cb);
      lab.appendChild(el('span', '', f.name + (f.old ? ' — ' + t('node_access_old_one') : '')));
      // Галочка — свой запрос сразу; не сохранилось — вернуть как было.
      cb.addEventListener('change', async () => {
        const want = cb.checked;
        cb.disabled = true;
        const ok = await nodeSetAccess(n, f.id, d, want);
        cb.disabled = false;
        if (!ok){ cb.checked = !want; return; }
        cb.blur();
        loadNode();
      });
      box.appendChild(lab);
    });
    const bar = el('div', 'row-inline');
    [['node_access_all', true], ['node_access_none', false]].forEach(([k, on]) => {
      const b = el('button', 'btn ghost small', t(k));
      b.addEventListener('click', async () => { if (await nodeSetAccess(n, '*', d, on)) loadNode(); });
      bar.appendChild(b);
    });
    box.appendChild(bar);
    card.appendChild(box);
  });
}

// ---- Общий список блокировки (ТЗ 15) ----
const adLines = v => v.split('\n').map(x => x.trim()).filter(Boolean);
function renderNodeAd(st){
  const card = $('nodeAdCard'), ad = st.adblock || {};
  $('nodeAdNames').textContent = ad.updating ? t('node_ad_updating') : (ad.version ? String(ad.names) : t('node_ad_none'));
  $('nodeAdUpdated').textContent = ad.updated ? fmtAgo(ad.updated) : '—';
  $('nodeAdDevices').textContent = String(ad.devices || 0);
  $('nodeAdErr').hidden = !ad.error;
  $('nodeAdErr').textContent = ad.error || '';
  const f = $('nodeAdFetched');
  f.innerHTML = '';
  (ad.fetched || []).forEach(x => f.appendChild(nodeRow(x.url, x.error ? t('node_ad_src_err') + ': ' + x.error : String(x.names))));
  if (!card.contains(document.activeElement)){
    $('nodeAdSources').value = (ad.sources || []).join('\n');
    $('nodeAdDeny').value = (ad.deny || []).join('\n');
    $('nodeAdAllow').value = (ad.allow || []).join('\n');
  }
  $('nodeAdUpdateBtn').disabled = !!ad.updating;
}
$('nodeAdSaveBtn').addEventListener('click', async () => {
  const {ok, data} = await api('/api/node/adblock/set',
    {sources: adLines($('nodeAdSources').value), deny: adLines($('nodeAdDeny').value), allow: adLines($('nodeAdAllow').value)});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  toast(t('node_ad_saved'), 'ok');
  document.activeElement.blur();
  loadNode();
});
$('nodeAdUpdateBtn').addEventListener('click', async () => {
  const {ok, data} = await api('/api/node/adblock/update', {});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  loadNode();
});

// ---- Проверить сайт с сервера (ТЗ 15) ----
async function nodeSiteCheck(){
  const site = $('nodeSiteInput').value.trim();
  if (!site){ $('nodeSiteInput').focus(); return; }
  const out = $('nodeSiteOut');
  out.innerHTML = '';
  out.appendChild(el('p', 'note', t('node_site_checking')));
  $('nodeSiteBtn').disabled = true;
  const {ok, data} = await api('/api/node/sitecheck', {site, lang: LANG});
  $('nodeSiteBtn').disabled = false;
  out.innerHTML = '';
  if (!ok){ out.appendChild(el('div', 'msg err', data.error || t('err_internal'))); return; }
  const p = data.server || {};
  out.appendChild(el('div', 'msg ' + (p.ok ? 'ok' : 'warn'), data.text || data.verdict));
  (p.steps || []).forEach(s => {
    out.appendChild(nodeRow((s.ok ? '● ' : '○ ') + t('site_step_' + s.id) + (s.ok ? '' : ' — ' + (s.kind || '')),
      [s.ms ? s.ms + ' ms' : '', s.note || ''].filter(Boolean).join(' · ')));
  });
}
$('nodeSiteBtn').addEventListener('click', nodeSiteCheck);
$('nodeSiteInput').addEventListener('keydown', e => { if (e.key === 'Enter') nodeSiteCheck(); });

/* ---------- кнопки хоста ---------- */
async function nodeInstall(){
  const {ok, data} = await api('/api/node/install', {});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  loadNode();
}
['nodeInstallBtn', 'nodeInstallBtn2', 'nodeReinstallBtn'].forEach(id => $(id).addEventListener('click', nodeInstall));
$('plHostMore').addEventListener('click', () => plGo('add'));
async function nodeRemove(deleteData){
  const yes = await appConfirm(t(deleteData ? 'confirm_node_remove_data' : 'confirm_node_remove'),
    t(deleteData ? 'node_remove_data' : 'node_remove'));
  if (!yes) return;
  const {ok, data} = await api('/api/node/remove', {deleteData});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  loadNode();
}
$('nodeRemoveBtn').addEventListener('click', () => nodeRemove(false));
$('nodeRemoveDataBtn').addEventListener('click', () => nodeRemove(true));
$('nodeQuotaSave').addEventListener('click', async () => {
  const {ok, data} = await api('/api/node/config', {quotaMB: Number($('nodeQuota').value) || 0});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  toast(t('pl_saved'), 'ok');
  $('nodeQuota').blur();
  loadNode();
});

/* ---------- события каркаса ---------- */
onPanel('show:Plugins', () => loadNode());
// Сменили экран (кнопкой, «Назад» браузера, по ссылке) — нарисовать его сразу,
// не дожидаясь опроса, и с самого верха.
onPanel('sub:Plugins', sub => {
  plCloseMenu();
  if (sub === 'grid') PL_PUSHED = false;
  scrollTo(0, 0);
  if (NODE) renderNode();
});
onPanel('lang', () => {
  ['plGrid', 'plCat', 'plGone'].forEach(id => { $(id).dataset.sig = ''; });
  if (NODE) renderNode();
});
// Имена сетей и «удалённые» сети — из картины сети; страницу плагина
// перерисует ближайший опрос хоста.
onPanel('meshLoaded', () => { if (TAB === 'Plugins' && plState()){ renderGone(plState()); renderGrid(plState()); } });
