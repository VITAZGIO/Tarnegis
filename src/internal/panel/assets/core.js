// core.js — общее для всех разделов панели: запросы, переводы, каркас
// (разделы, вкладки, адрес), подсказки «?», данные сети и клиентов.
// Разделы лежат в sections/<имя>.js и не зовут друг друга: каркас сообщает
// события (emitPanel), разделы на них подписываются (onPanel).
const $ = id => document.getElementById(id);

/* ---------- события каркаса ---------- */
// lang — сменили язык (перерисовать тексты, собранные кодом);
// show / show:<Раздел> — открыли раздел; sub:<Раздел> — вкладку в нём;
// status — пришло /api/status; clients — список клиентов (first — впервые);
// access — правила доступа (сырые, как на сервере); mesh — картина сети
// (m, роль); meshLoaded — после каждого опроса сети; remap — схему надо
// пересчитать; start — вход выполнен, данные грузятся; refresh — раз в 15 с.
const PANEL_HOOKS = {};
function onPanel(ev, fn){ (PANEL_HOOKS[ev] = PANEL_HOOKS[ev] || []).push(fn); }
function emitPanel(ev, ...args){ (PANEL_HOOKS[ev] || []).forEach(fn => fn(...args)); }
// api — запрос к панели. Связь с панелью может идти через сеть устройств
// этого же сервера: пока meshd перезапускается (кнопка «Обновить meshd»),
// соединение рвётся или повисает без ответа. Поэтому — ограничение по
// времени (только у чтения — действия бывают долгими) и ответ-ошибка вместо
// исключения: опрос страницы не должен останавливаться навсегда.
const API_TIMEOUT_MS = 20000;
const api = (path, body) => {
  const ctl = new AbortController();
  const timer = body ? 0 : setTimeout(() => ctl.abort(), API_TIMEOUT_MS);
  return fetch(path, {
    method: body ? 'POST' : 'GET',
    headers: {'Content-Type': 'application/json'},
    credentials: 'same-origin',
    body: body ? JSON.stringify(body) : undefined,
    signal: ctl.signal
  }).then(async r => ({ok: r.ok, status: r.status, data: await r.json().catch(() => ({}))}))
    .catch(() => ({ok: false, status: 0, data: {error: t('err_no_link')}}))
    .finally(() => clearTimeout(timer));
};

/* ---------- своё окно подтверждения ---------- */
let confirmResolve = null;
function appConfirm(message, okLabel){
  $('confirmText').textContent = message;
  $('confirmOkBtn').textContent = okLabel || t('confirm_yes');
  $('confirmOverlay').hidden = false;
  return new Promise(resolve => { confirmResolve = resolve; });
}
function closeConfirm(result){
  $('confirmOverlay').hidden = true;
  const r = confirmResolve;
  confirmResolve = null;
  if (r) r(result);
}
$('confirmOkBtn').addEventListener('click', () => closeConfirm(true));
$('confirmCancelBtn').addEventListener('click', () => closeConfirm(false));

/* ---------- язык интерфейса ---------- */
let LANG = localStorage.getItem('panelLang') || 'ru';
// Переводы собираются из assets/i18n/<раздел>.<язык>.js (они идут сразу
// после core.js и дописывают сюда свои ключи).
const I18N = {ru: {}, en: {}};
function t(key){ return (I18N[LANG] && I18N[LANG][key]) || key; }
function applyI18N(){
  document.documentElement.lang = LANG;
  document.querySelectorAll('[data-i18n]').forEach(el => { el.textContent = t(el.dataset.i18n); });
  document.querySelectorAll('[data-i18n-ph]').forEach(el => { el.placeholder = t(el.dataset.i18nPh); });
  document.querySelectorAll('[data-i18n-title]').forEach(el => { el.title = t(el.dataset.i18nTitle); });
  $('lang').value = LANG;
  renderPageHead();
  // Списки собираются кодом, поэтому их надо перерисовать целиком.
  emitPanel('lang');
  if (!$('viewMain').hidden){
    emitPanel('clients', false);
    if (MESH){ $('netChips').dataset.sig = ''; renderMesh(); }
  }
}
$('lang').addEventListener('change', e => {
  LANG = e.target.value;
  localStorage.setItem('panelLang', LANG);
  applyI18N();
});

function errText(code, extra){
  let s = t('err_' + code) || code;
  if (extra && extra.retryAfterSecond) s = s.replace('{s}', extra.retryAfterSecond);
  return s;
}
function showMsg(el, text, kind){
  el.innerHTML = '';
  if (!text) return;
  const div = document.createElement('div');
  div.className = 'msg ' + kind;
  div.textContent = text;
  el.appendChild(div);
}

/* ---------- переключение экранов и вкладок ---------- */
function showView(name){
  ['Login','MustChange','Main'].forEach(v => { $('view'+v).hidden = (v !== name); });
  $('pageHead').hidden = name !== 'Main';
}

const TABS = ['Home','Devices','Access','Mesh','Settings','Plugins','Monitor'];
// Раздел и вкладка — в адресе (#nets/servers): ссылка и перезагрузка ведут
// на тот же экран (ТЗ 23).
const TAB_SLUG = {Home:'home', Devices:'devices', Access:'access', Mesh:'nets', Settings:'settings', Plugins:'plugins',
                  Monitor:'monitor'};
function subsOf(tab){
  const bar = document.querySelector('.subtabs[data-tab="' + tab + '"]');
  return bar ? [...bar.querySelectorAll('button')].map(b => b.dataset.sub) : [];
}
const SUB = {};
TABS.forEach(tab => {
  const subs = subsOf(tab);
  if (!subs.length) return;
  let v = null;
  try { v = localStorage.getItem('panelSub' + tab); } catch (_){}
  SUB[tab] = subs.includes(v) ? v : subs[0];
});
function parseHash(){
  const [slug, sub] = location.hash.replace(/^#/, '').split('/');
  const tab = TABS.find(t => TAB_SLUG[t] === slug);
  if (tab && sub && subsOf(tab).includes(sub)) SUB[tab] = sub;
  return tab;
}
function writeHash(){
  const h = '#' + TAB_SLUG[TAB] + (SUB[TAB] ? '/' + SUB[TAB] : '');
  if (location.hash !== h) history.replaceState(null, '', h);
}
let TAB = parseHash() || localStorage.getItem('panelTab') || 'Home';
if (!TABS.includes(TAB)) TAB = 'Home';
// showSub — вкладка внутри раздела.
function showSub(tab, sub){
  const subs = subsOf(tab);
  if (!subs.length) return;
  if (!subs.includes(sub)) sub = subs[0];
  SUB[tab] = sub;
  try { localStorage.setItem('panelSub' + tab, sub); } catch (_){}
  const sec = $('tab' + tab);
  sec.querySelectorAll(':scope > .sub').forEach(d => { d.hidden = d.dataset.sub !== sub; });
  sec.querySelectorAll(':scope > .subtabs button').forEach(b => b.classList.toggle('on', b.dataset.sub === sub));
  refreshSubtabs(tab);
  if (tab === TAB){ writeHash(); renderPageHead(); }
  emitPanel('sub:' + tab, sub);
}
// remesh — перерисовать всё, что нарисовано по картине сети, со схемой
// заново (раздел или вкладка стали видны, сменили сеть или язык).
function remesh(){
  emitPanel('remap');
  if (MESH) renderMesh();
}
document.querySelectorAll('.subtabs').forEach(bar => {
  bar.querySelectorAll('button').forEach(b => b.addEventListener('click', () => {
    showSub(bar.dataset.tab, b.dataset.sub);
    scrollTo(0, 0);
  }));
});
// Вкладка, где сейчас нечего показать (например, плагины до установки
// хоста), не показывается; первая — всегда.
function refreshSubtabs(tab){
  const sec = $('tab' + tab);
  const btns = [...sec.querySelectorAll(':scope > .subtabs button')];
  btns.forEach((b, i) => {
    const sub = sec.querySelector(':scope > .sub[data-sub="' + b.dataset.sub + '"]');
    const has = sub && [...sub.querySelectorAll('.card, .cards > *')].some(c => !c.hidden);
    const want = i > 0 && !has && SUB[tab] !== b.dataset.sub;
    if (b.hidden !== want) b.hidden = want; // без лишних записей: наблюдатель не зациклится
  });
}
document.querySelectorAll('.subtabs').forEach(bar => {
  const tab = bar.dataset.tab;
  new MutationObserver(() => refreshSubtabs(tab))
    .observe($('tab' + tab), {subtree: true, childList: true, attributes: true, attributeFilter: ['hidden']});
  refreshSubtabs(tab);
});
addEventListener('hashchange', () => {
  const tab = parseHash();
  if (tab) showTab(tab);
});
// Название раздела в шапке (ТЗ 30): title — ключ перевода названия, help —
// пояснение под «?» (бывшая серая строка под заголовком и бывшая подсказка
// у заголовка), первым ключом ищется глава Книги (BOOK_HELP).
const PAGE_HEAD = {
  Home:     {title: 'nav_home',     help: ['home_sub']},
  Devices:  {title: 'nav_devices',  help: ['devices_sub', 'hint_devices_list']},
  Access:   {title: 'nav_access',   help: ['access_sub']},
  Mesh:     {title: 'nav_mesh',     help: ['mesh_sub', 'hint_mesh_what']},
  Settings: {title: 'nav_settings', help: ['settings_sub']},
  Plugins:  {title: 'nav_plugins',  help: ['plugins_sub', 'hint_plugins_what']},
  Monitor:  {title: 'nav_monitor',  help: ['mon_sub']},
};
// renderPageHead — «Доступ · Журнал»: вкладка — через точку, если она не
// повторяет название (первая вкладка «Плагинов» — сами «Плагины»).
function renderPageHead(){
  const h = PAGE_HEAD[TAB];
  if (!h) return;
  const box = $('pageTitle');
  box.textContent = t(h.title);
  const sub = SUB[TAB];
  if (sub){
    const b = document.querySelector('.subtabs[data-tab="' + TAB + '"] button[data-sub="' + sub + '"]');
    const label = b ? t(b.dataset.i18n) : '';
    if (label && label !== t(h.title)){
      box.appendChild(el('span', 'pt-sub', ' · ' + label));
    }
  }
  const help = $('pageHelp');
  help.dataset.hint = h.help[0];
  help.dataset.hints = h.help.join(' ');
  document.title = box.textContent + ' — ssh_tunnel panel';
}
function showTab(name){
  if (!TABS.includes(name)) name = 'Home';
  const changed = TAB !== name;
  TAB = name;
  localStorage.setItem('panelTab', name);
  if (SUB[name]) showSub(name, SUB[name]); else writeHash();
  TABS.forEach(v => { $('tab'+v).hidden = (v !== name); });
  document.querySelectorAll('.side button').forEach(b => {
    b.classList.toggle('on', b.dataset.tab === name);
  });
  hidePop();
  renderPageHead();
  if (changed) scrollTo(0, 0);
  emitPanel('show:' + name);
  emitPanel('show', name);
  remesh();
}
document.querySelectorAll('.side button').forEach(b => {
  b.addEventListener('click', () => showTab(b.dataset.tab));
});

function fmtUptime(sec){
  sec = Math.max(0, sec|0);
  const h = Math.floor(sec/3600), m = Math.floor((sec%3600)/60), s = sec%60;
  return [h,m,s].map(x => String(x).padStart(2,'0')).join(':');
}
function fmtDate(iso){
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  return d.toLocaleDateString(LANG === 'ru' ? 'ru-RU' : 'en-US');
}
function fmtBytes(n){
  n = Number(n) || 0;
  const u = LANG === 'ru' ? ['Б','КБ','МБ','ГБ','ТБ'] : ['B','KB','MB','GB','TB'];
  let i = 0;
  while (n >= 1024 && i < u.length - 1){ n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(1)) + ' ' + u[i];
}
// Пустое время в Go — не пустая строка, а «01.01.0001»: клиент, который ещё
// ни разу не подключался, приезжает сюда как 0001-01-01T00:00:00Z. Без этой
// проверки в карточке оказывается «был на связи 01.01.1».
function neverSeen(iso){
  return !iso || String(iso).startsWith('0001-01-01');
}
function onlineText(c){
  return c.sessions > 0
    ? t('sessions_online').replace('{n}', c.sessions)
    : (neverSeen(c.lastSeenAt) ? t('never_connected') : t('last_seen') + ' ' + fmtDate(c.lastSeenAt));
}
function stateBadge(state){
  const span = document.createElement('span');
  span.className = 'badge ' + (state === 'active' ? 'ok' : state === 'frozen' ? 'warn' : 'dim');
  span.textContent = t('state_' + state);
  return span;
}
// cell — ячейка таблицы с уже готовым текстом: собираем узлы, а не строки
// HTML, чтобы имя устройства от человека не могло превратиться в разметку.
function cell(row, text, cls){
  const td = document.createElement('td');
  if (cls) td.className = cls;
  td.textContent = text;
  row.appendChild(td);
  return td;
}

/* ---------- данные ---------- */
let CLIENTS = [];
// Клиенты хоть раз пришли: до этого таблицу доступа не рисуем — без списка
// клиентов все устройства выглядят «с другого сервера».
let CLIENTS_READY = false;

async function loadSession(){
  const {data} = await api('/api/session');
  if (!data.authenticated){ showView('Login'); return; }
  if (data.mustChange){ showView('MustChange'); return; }
  showView('Main');
  $('demoBanner').hidden = !data.demo;
  showTab(TAB);
  loadAccess();
  // Данные сети нужны почти всем разделам: схема и плитки, список
  // устройств, сети, роль сервера в настройках.
  loadMesh();
  loadStatus();
  await loadClients();
  emitPanel('start');
}

// STATUS — последний ответ /api/status (версия панели нужна нескольким разделам).
let STATUS = null;
async function loadStatus(){
  const {ok, data} = await api('/api/status');
  if (!ok) return;
  STATUS = data;
  emitPanel('status', data);
}

async function loadClients(){
  const {ok, data} = await api('/api/clients');
  if (!ok) return;
  CLIENTS = data.clients || [];
  const first = !CLIENTS_READY;
  CLIENTS_READY = true;
  emitPanel('clients', first);
}

/* ---------- права доступа в сети устройств ---------- */
let ACCESS = {enabled:false, groups:[], rules:[]};
async function loadAccess(){
  const {ok, data} = await api('/api/access');
  if (!ok) return;
  ACCESS = data.access || {};
  // Как на сервере — для простого режима; ACCESS ниже получает заготовку
  // «все → все» для редактора.
  const raw = JSON.parse(JSON.stringify(ACCESS));
  raw.groups = raw.groups || [];
  raw.rules = raw.rules || [];
  ACCESS.groups = ACCESS.groups || [];
  ACCESS.rules = ACCESS.rules || [];
  // Первое включение: начинаем с «все → все», чтобы ничего не отвалилось.
  if (!ACCESS.rules.length && !ACCESS.enabled) ACCESS.rules = [{from:'*', to:'*', ports:''}];
  emitPanel('access', raw);
}
// Группы простого режима доступа (sections/access.js) — нужны и подписям групп.
const SM_ROLE_OF = {'~mine': 'mine', '~server': 'server', '~guest': 'guest'};
const SM_PERSONAL = /^~tun_[0-9a-f]{16}$/;
function groupLabel(g){
  if (g === '*') return t('acc_all');
  if (g === '@servers') return t('acc_servers');
  // Группы простого режима — по-человечески: роль или имя устройства.
  if (SM_ROLE_OF[g]) return t('sm_role_' + SM_ROLE_OF[g]) + ' (' + g + ')';
  if (SM_PERSONAL.test(g)){
    const c = CLIENTS.find(x => x.username === g.slice(1));
    if (c) return c.name + ' (' + t('sm_personal_g') + ')';
  }
  return g;
}
function accNote(box, key){
  const e = document.createElement('span');
  e.className = 'note';
  e.textContent = t(key);
  box.appendChild(e);
}
// navigator.clipboard требует безопасный контекст (HTTPS или localhost) — в
// режиме --lan панель отдаётся по голому HTTP на адрес в сети, и там этого
// API у браузера просто нет. Временный textarea с document.execCommand
// устарел, но работает и без HTTPS: для --lan это основной путь, а не
// запасной.
async function copyText(text){
  if (navigator.clipboard && window.isSecureContext) {
    try { await navigator.clipboard.writeText(text); return true; } catch (e) { /* попробуем иначе */ }
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.top = '0';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.focus();
  ta.select();
  ta.setSelectionRange(0, ta.value.length);
  let ok = false;
  try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
  document.body.removeChild(ta);
  return ok;
}

/* ---------- вход, пароль, выход ---------- */
$('loginBtn').addEventListener('click', async () => {
  showMsg($('loginMsg'), '', '');
  const username = $('loginUser').value.trim();
  const password = $('loginPass').value;
  $('loginBtn').disabled = true;
  const {ok, data} = await api('/api/login', {username, password});
  $('loginBtn').disabled = false;
  if (!ok){ showMsg($('loginMsg'), errText(data.error, data), 'err'); return; }
  $('loginPass').value = '';
  loadSession();
});
['loginUser','loginPass'].forEach(id => $(id).addEventListener('keydown', e => {
  if (e.key === 'Enter') $('loginBtn').click();
}));

async function doChangePassword(curId, p1Id, p2Id, msgEl, btn, onDone){
  showMsg(msgEl, '', '');
  const currentPassword = $(curId).value;
  const p1 = $(p1Id).value, p2 = $(p2Id).value;
  if (p1 !== p2){ showMsg(msgEl, errText('password_mismatch'), 'err'); return; }
  btn.disabled = true;
  const {ok, data} = await api('/api/change-password', {currentPassword, newPassword: p1});
  btn.disabled = false;
  if (!ok){ showMsg(msgEl, errText(data.error, data), 'err'); return; }
  $(curId).value = ''; $(p1Id).value = ''; $(p2Id).value = '';
  onDone();
}

$('changeBtn').addEventListener('click', () => doChangePassword(
  'curPass','newPass1','newPass2', $('changeMsg'), $('changeBtn'), loadSession));

/* ---------- подсказки «?» ---------- */
/* Одно всплывающее окно на страницу. На компьютере — по наведению и фокусу,
   на телефоне — по нажатию (второе нажатие или нажатие мимо закрывает). */
const hintPop = $('hintPop');
let hintOwner = null, hintPinned = false;
function fillPop(content){
  hintPop.innerHTML = '';
  if (content instanceof Node){ hintPop.appendChild(content); return; }
  // Строки подсказок — свои, из I18N, в них бывает <b> и <p>. Ввод людей
  // (имена устройств) сюда не попадает: он идёт узлами через textContent.
  if (content.includes('<')){ hintPop.innerHTML = content; return; }
  const p = document.createElement('p');
  p.textContent = content;
  hintPop.appendChild(p);
}
// hintContent — текст подсказки; у «?» в шапке (data-hints) — несколько
// строк перевода подряд, каждая своим абзацем.
function hintContent(h){
  if (!h.dataset.hints) return t(h.dataset.hint);
  return h.dataset.hints.split(' ').map(k => {
    const v = t(k);
    return v.startsWith('<p>') ? v : '<p>' + v.replace(/&/g, '&amp;').replace(/</g, '&lt;') + '</p>';
  }).join('');
}
function placePop(r){
  const pw = hintPop.offsetWidth, ph = hintPop.offsetHeight;
  let left = Math.max(12, Math.min(r.left + r.width/2 - pw/2, innerWidth - pw - 12));
  let top = r.bottom + 9, above = false;
  if (top + ph > innerHeight - 8 && r.top - ph - 9 > 8){ top = r.top - ph - 9; above = true; }
  hintPop.style.left = left + 'px';
  hintPop.style.top = top + 'px';
  hintPop.classList.toggle('above', above);
}
function showPop(owner, content, pinned){
  if (hintOwner && hintOwner !== owner) hintOwner.classList.remove('open');
  hintOwner = owner;
  hintPinned = !!pinned;
  fillPop(content);
  if (owner.classList.contains('hint')) owner.classList.add('open');
  hintPop.classList.toggle('pinned', hintPinned);
  if (hintPinned && owner.dataset && BOOK_HELP[owner.dataset.hint]) addBookLink(owner);
  placePop(owner.getBoundingClientRect());
  hintPop.classList.add('show');
}
// Ссылка «Подробнее в Книге» — только в закреплённой подсказке (обычная не
// ловит мышь) и только если глава есть: нет главы — нет ссылки.
function addBookLink(owner){
  const put = () => {
    if (hintOwner !== owner || !hintPinned || hintPop.querySelector('.bk-more')) return;
    const id = bookChapterFor(owner.dataset.hint);
    if (!id) return;
    const a = document.createElement('a');
    a.className = 'bk-more';
    a.href = '#';
    a.textContent = t('book_more');
    a.addEventListener('click', e => { e.preventDefault(); hidePop(); bkGoto(id); });
    hintPop.appendChild(a);
    placePop(owner.getBoundingClientRect());
  };
  put();
  if (BK.lang !== LANG || !BK.chapters.length) bkEnsure().then(put);
}
function hidePop(){
  hintPop.classList.remove('show', 'pinned');
  if (hintOwner) hintOwner.classList.remove('open');
  hintOwner = null;
  hintPinned = false;
}
document.addEventListener('mouseover', e => {
  const h = e.target.closest('.hint');
  if (h && !hintPinned && h !== hintOwner) showPop(h, hintContent(h));
});
document.addEventListener('mouseout', e => {
  const h = e.target.closest('.hint');
  if (h && h === hintOwner && !hintPinned && !h.contains(e.relatedTarget)) hidePop();
});
document.addEventListener('focusin', e => {
  const h = e.target.closest('.hint');
  if (h) showPop(h, hintContent(h));
});
document.addEventListener('focusout', e => {
  if (e.target.closest('.hint') && !hintPinned) hidePop();
});
// Перехват на всплытии не годится: «?» стоит и внутри кнопок выбора роли, и
// внутри подписей к полям — нажатие на знак не должно выбирать роль или
// ставить курсор в поле.
document.addEventListener('click', e => {
  if (e.target.closest('#hintPop')) return;      // ссылка внутри закреплённой подсказки
  const h = e.target.closest('.hint');
  if (h){
    e.preventDefault();
    e.stopPropagation();
    if (hintOwner === h && hintPinned) hidePop(); else showPop(h, hintContent(h), true);
    return;
  }
  if (hintPinned) hidePop();
}, true);
document.addEventListener('keydown', e => { if (e.key === 'Escape') hidePop(); });
addEventListener('scroll', () => { if (hintOwner) hidePop(); }, true);
addEventListener('resize', () => { if (hintOwner) hidePop(); });

/* ---------- уведомления ---------- */
function toast(text, kind){
  const el = document.createElement('div');
  el.className = 'toast' + (kind === 'err' ? ' err' : '');
  el.textContent = text;
  $('toasts').appendChild(el);
  setTimeout(() => {
    el.classList.add('out');
    setTimeout(() => el.remove(), 260);
  }, kind === 'err' ? 6500 : 3200);
}

/* ---------- мелочи для сети устройств ---------- */
function fmtAgo(unix){
  if (!unix) return '—';
  const s = Math.max(0, Date.now()/1000 - unix);
  if (s < 60) return t('ago_now');
  if (s < 3600) return t('ago_min').replace('{n}', Math.floor(s/60));
  if (s < 86400) return t('ago_hour').replace('{n}', Math.floor(s/3600));
  return t('ago_day').replace('{n}', Math.floor(s/86400));
}
function fmtClock(unix){
  if (!unix) return '—';
  const d = new Date(unix*1000);
  return d.toLocaleString(LANG === 'ru' ? 'ru-RU' : 'en-US',
    {day:'2-digit', month:'2-digit', hour:'2-digit', minute:'2-digit'});
}
function fmtMs(v){ return v ? (v < 10 ? v.toFixed(1) : Math.round(v)) + ' ' + (LANG === 'ru' ? 'мс' : 'ms') : '—'; }
const QLEVEL = {excellent:4, good:3, fair:2, poor:1, unknown:0};
function qualBars(q){
  const s = document.createElement('span');
  s.className = 'qual q' + (QLEVEL[q] || 0);
  for (let i = 0; i < 4; i++) s.appendChild(document.createElement('span'));
  return s;
}
function el(tag, cls, text){
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}
// animateNum плавно докручивает число в плитке до нового значения.
function animateNum(node, value, fmt){
  const from = Number(node.dataset.v || 0);
  node.dataset.v = value;
  if (from === value || matchMedia('(prefers-reduced-motion: reduce)').matches){
    node.textContent = fmt(value);
    return;
  }
  const start = performance.now(), dur = 600;
  const step = now => {
    const k = Math.min(1, (now - start) / dur), e = 1 - Math.pow(1 - k, 3);
    node.textContent = fmt(from + (value - from) * e);
    if (k < 1 && node.dataset.v == value) requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
}

const NS = 'http://www.w3.org/2000/svg';
// Значки — одни и те же на схеме и в карточках: координаты вокруг нуля.
const GLYPH = {
  windows:'M-5 -5h4.3v4.3h-4.3zM.7 -5h4.3v4.3h-4.3zM-5 .7h4.3v4.3h-4.3zM.7 .7h4.3v4.3h-4.3z',
  linux:'M-6 -4.5h12v9h-12zM-3.5 -1.6l2 1.6-2 1.6M.6 1.6h3',
  android:'M-3.5 -6.2h7a1.2 1.2 0 0 1 1.2 1.2v10a1.2 1.2 0 0 1-1.2 1.2h-7a1.2 1.2 0 0 1-1.2-1.2v-10a1.2 1.2 0 0 1 1.2-1.2zM-1.2 3.6h2.4',
  other:'M-6 -4.2h12v7.4h-12zM-7.6 5.2h15.2',
  server:'M-6 -5.6h12v4.6h-12zM-6 1h12v4.6h-12zM-3.4 -3.3h.01M-3.4 3.3h.01',
  main:'M-2.4 0a2.4 2.4 0 1 0 4.8 0a2.4 2.4 0 1 0-4.8 0M-6.2 -6.2l3.9 3.9M6.2 -6.2l-3.9 3.9M-6.2 6.2l3.9-3.9M6.2 6.2l-3.9-3.9',
};
const PLATFORM_NAME = {windows:'Windows', linux:'Linux', android:'Android', darwin:'macOS'};
function glyphOf(platform){ return GLYPH[platform] || GLYPH.other; }
// OS_ICON — значки систем в колонке «Система» (ТЗ 34): официальные формы из
// Simple Icons (CC0, simpleicons.org; windows — из версии 10, в новых его
// нет), поле 24×24. Одноцветные: цвет берут у текста (currentColor).
const OS_ICON = {
  windows:'M0,0H11.377V11.372H0ZM12.623,0H24V11.372H12.623ZM0,12.623H11.377V24H0Zm12.623,0H24V24H12.623',
  android:'M18.4395 5.5586c-.675 1.1664-1.352 2.3318-2.0274 3.498-.0366-.0155-.0742-.0286-.1113-.043-1.8249-.6957-3.484-.8-4.42-.787-1.8551.0185-3.3544.4643-4.2597.8203-.084-.1494-1.7526-3.021-2.0215-3.4864a1.1451 1.1451 0 0 0-.1406-.1914c-.3312-.364-.9054-.4859-1.379-.203-.475.282-.7136.9361-.3886 1.5019 1.9466 3.3696-.0966-.2158 1.9473 3.3593.0172.031-.4946.2642-1.3926 1.0177C2.8987 12.176.452 14.772 0 18.9902h24c-.119-1.1108-.3686-2.099-.7461-3.0683-.7438-1.9118-1.8435-3.2928-2.7402-4.1836a12.1048 12.1048 0 0 0-2.1309-1.6875c.6594-1.122 1.312-2.2559 1.9649-3.3848.2077-.3615.1886-.7956-.0079-1.1191a1.1001 1.1001 0 0 0-.8515-.5332c-.5225-.0536-.9392.3128-1.0488.5449zm-.0391 8.461c.3944.5926.324 1.3306-.1563 1.6503-.4799.3197-1.188.0985-1.582-.4941-.3944-.5927-.324-1.3307.1563-1.6504.4727-.315 1.1812-.1086 1.582.4941zM7.207 13.5273c.4803.3197.5506 1.0577.1563 1.6504-.394.5926-1.1038.8138-1.584.4941-.48-.3197-.5503-1.0577-.1563-1.6504.4008-.6021 1.1087-.8106 1.584-.4941z',
  linux:'M12.504 0c-.155 0-.315.008-.48.021-4.226.333-3.105 4.807-3.17 6.298-.076 1.092-.3 1.953-1.05 3.02-.885 1.051-2.127 2.75-2.716 4.521-.278.832-.41 1.684-.287 2.489a.424.424 0 00-.11.135c-.26.268-.45.6-.663.839-.199.199-.485.267-.797.4-.313.136-.658.269-.864.68-.09.189-.136.394-.132.602 0 .199.027.4.055.536.058.399.116.728.04.97-.249.68-.28 1.145-.106 1.484.174.334.535.47.94.601.81.2 1.91.135 2.774.6.926.466 1.866.67 2.616.47.526-.116.97-.464 1.208-.946.587-.003 1.23-.269 2.26-.334.699-.058 1.574.267 2.577.2.025.134.063.198.114.333l.003.003c.391.778 1.113 1.132 1.884 1.071.771-.06 1.592-.536 2.257-1.306.631-.765 1.683-1.084 2.378-1.503.348-.199.629-.469.649-.853.023-.4-.2-.811-.714-1.376v-.097l-.003-.003c-.17-.2-.25-.535-.338-.926-.085-.401-.182-.786-.492-1.046h-.003c-.059-.054-.123-.067-.188-.135a.357.357 0 00-.19-.064c.431-1.278.264-2.55-.173-3.694-.533-1.41-1.465-2.638-2.175-3.483-.796-1.005-1.576-1.957-1.56-3.368.026-2.152.236-6.133-3.544-6.139zm.529 3.405h.013c.213 0 .396.062.584.198.19.135.33.332.438.533.105.259.158.459.166.724 0-.02.006-.04.006-.06v.105a.086.086 0 01-.004-.021l-.004-.024a1.807 1.807 0 01-.15.706.953.953 0 01-.213.335.71.71 0 00-.088-.042c-.104-.045-.198-.064-.284-.133a1.312 1.312 0 00-.22-.066c.05-.06.146-.133.183-.198.053-.128.082-.264.088-.402v-.02a1.21 1.21 0 00-.061-.4c-.045-.134-.101-.2-.183-.333-.084-.066-.167-.132-.267-.132h-.016c-.093 0-.176.03-.262.132a.8.8 0 00-.205.334 1.18 1.18 0 00-.09.4v.019c.002.089.008.179.02.267-.193-.067-.438-.135-.607-.202a1.635 1.635 0 01-.018-.2v-.02a1.772 1.772 0 01.15-.768c.082-.22.232-.406.43-.533a.985.985 0 01.594-.2zm-2.962.059h.036c.142 0 .27.048.399.135.146.129.264.288.344.465.09.199.14.4.153.667v.004c.007.134.006.2-.002.266v.08c-.03.007-.056.018-.083.024-.152.055-.274.135-.393.2.012-.09.013-.18.003-.267v-.015c-.012-.133-.04-.2-.082-.333a.613.613 0 00-.166-.267.248.248 0 00-.183-.064h-.021c-.071.006-.13.04-.186.132a.552.552 0 00-.12.27.944.944 0 00-.023.33v.015c.012.135.037.2.08.334.046.134.098.2.166.268.01.009.02.018.034.024-.07.057-.117.07-.176.136a.304.304 0 01-.131.068 2.62 2.62 0 01-.275-.402 1.772 1.772 0 01-.155-.667 1.759 1.759 0 01.08-.668 1.43 1.43 0 01.283-.535c.128-.133.26-.2.418-.2zm1.37 1.706c.332 0 .733.065 1.216.399.293.2.523.269 1.052.468h.003c.255.136.405.266.478.399v-.131a.571.571 0 01.016.47c-.123.31-.516.643-1.063.842v.002c-.268.135-.501.333-.775.465-.276.135-.588.292-1.012.267a1.139 1.139 0 01-.448-.067 3.566 3.566 0 01-.322-.198c-.195-.135-.363-.332-.612-.465v-.005h-.005c-.4-.246-.616-.512-.686-.71-.07-.268-.005-.47.193-.6.224-.135.38-.271.483-.336.104-.074.143-.102.176-.131h.002v-.003c.169-.202.436-.47.839-.601.139-.036.294-.065.466-.065zm2.8 2.142c.358 1.417 1.196 3.475 1.735 4.473.286.534.855 1.659 1.102 3.024.156-.005.33.018.513.064.646-1.671-.546-3.467-1.089-3.966-.22-.2-.232-.335-.123-.335.59.534 1.365 1.572 1.646 2.757.13.535.16 1.104.021 1.67.067.028.135.06.205.067 1.032.534 1.413.938 1.23 1.537v-.043c-.06-.003-.12 0-.18 0h-.016c.151-.467-.182-.825-1.065-1.224-.915-.4-1.646-.336-1.77.465-.008.043-.013.066-.018.135-.068.023-.139.053-.209.064-.43.268-.662.669-.793 1.187-.13.533-.17 1.156-.205 1.869v.003c-.02.334-.17.838-.319 1.35-1.5 1.072-3.58 1.538-5.348.334a2.645 2.645 0 00-.402-.533 1.45 1.45 0 00-.275-.333c.182 0 .338-.03.465-.067a.615.615 0 00.314-.334c.108-.267 0-.697-.345-1.163-.345-.467-.931-.995-1.788-1.521-.63-.4-.986-.87-1.15-1.396-.165-.534-.143-1.085-.015-1.645.245-1.07.873-2.11 1.274-2.763.107-.065.037.135-.408.974-.396.751-1.14 2.497-.122 3.854a8.123 8.123 0 01.647-2.876c.564-1.278 1.743-3.504 1.836-5.268.048.036.217.135.289.202.218.133.38.333.59.465.21.201.477.335.876.335.039.003.075.006.11.006.412 0 .73-.134.997-.268.29-.134.52-.334.74-.4h.005c.467-.135.835-.402 1.044-.7zm2.185 8.958c.037.6.343 1.245.882 1.377.588.134 1.434-.333 1.791-.765l.211-.01c.315-.007.577.01.847.268l.003.003c.208.199.305.53.391.876.085.4.154.78.409 1.066.486.527.645.906.636 1.14l.003-.007v.018l-.003-.012c-.015.262-.185.396-.498.595-.63.401-1.746.712-2.457 1.57-.618.737-1.37 1.14-2.036 1.191-.664.053-1.237-.2-1.574-.898l-.005-.003c-.21-.4-.12-1.025.056-1.69.176-.668.428-1.344.463-1.897.037-.714.076-1.335.195-1.814.12-.465.308-.797.641-.984l.045-.022zm-10.814.049h.01c.053 0 .105.005.157.014.376.055.706.333 1.023.752l.91 1.664.003.003c.243.533.754 1.064 1.189 1.637.434.598.77 1.131.729 1.57v.006c-.057.744-.48 1.148-1.125 1.294-.645.135-1.52.002-2.395-.464-.968-.536-2.118-.469-2.857-.602-.369-.066-.61-.2-.723-.4-.11-.2-.113-.602.123-1.23v-.004l.002-.003c.117-.334.03-.752-.027-1.118-.055-.401-.083-.71.043-.94.16-.334.396-.4.69-.533.294-.135.64-.202.915-.47h.002v-.002c.256-.268.445-.601.668-.838.19-.201.38-.336.663-.336zm7.159-9.074c-.435.201-.945.535-1.488.535-.542 0-.97-.267-1.28-.466-.154-.134-.28-.268-.373-.335-.164-.134-.144-.333-.074-.333.109.016.129.134.199.2.096.066.215.2.36.333.292.2.68.467 1.167.467.485 0 1.053-.267 1.398-.466.195-.135.445-.334.648-.467.156-.136.149-.267.279-.267.128.016.034.134-.147.332a8.097 8.097 0 01-.69.468zm-1.082-1.583V5.64c-.006-.02.013-.042.029-.05.074-.043.18-.027.26.004.063 0 .16.067.15.135-.006.049-.085.066-.135.066-.055 0-.092-.043-.141-.068-.052-.018-.146-.008-.163-.065zm-.551 0c-.02.058-.113.049-.166.066-.047.025-.086.068-.14.068-.05 0-.13-.02-.136-.068-.01-.066.088-.133.15-.133.08-.031.184-.047.259-.005.019.009.036.03.03.05v.02h.003z',
};
// osIcon — <svg class="os-ic"> для windows / android / linux, иначе null:
// незнакомой системе значок не рисуем.
function osIcon(platform){
  const d = OS_ICON[platform];
  if (!d) return null;
  const svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('class', 'os-ic');
  svg.setAttribute('aria-hidden', 'true');
  const p = document.createElementNS(NS, 'path');
  p.setAttribute('d', d);
  svg.appendChild(p);
  return svg;
}
function glyphSVG(path){
  const svg = document.createElementNS(NS, 'svg');
  svg.setAttribute('viewBox', '-8 -8 16 16');
  const p = document.createElementNS(NS, 'path');
  p.setAttribute('d', path);
  svg.appendChild(p);
  return svg;
}

/* ---------- сеть устройств: данные ---------- */
let MESH = null, MESH_TIMER = null, MESH_FILTER = 'all', MESH_NET = null;
let MESH_EDIT = null;      // id устройства, которое сейчас переименовывают
let SRV_CODE = '';         // код «Добавить сервер», пока он показан

let MESH_FRESH = true; // страницу открыли: попросить свежие картины других серверов
async function loadMesh(){
  clearTimeout(MESH_TIMER);
  const fresh = MESH_FRESH;
  MESH_FRESH = false;
  try {
    const {ok, data} = await api('/api/mesh/state' + (fresh ? '?fresh=1' : ''));
    if (ok){
      MESH = data;
      renderMesh();
      emitPanel('meshLoaded');
    }
  } finally {
    scheduleMesh();
  }
}
function scheduleMesh(){
  clearTimeout(MESH_TIMER);
  if ($('viewMain').hidden) return;
  const fast = MESH && MESH.job && MESH.job.running;
  MESH_TIMER = setTimeout(() => {
    if (document.hidden){ scheduleMesh(); return; }
    loadMesh();
  }, fast ? 1500 : 5000);
}
// Вернулись на вкладку — снова свежие картины (сервер сам не чаще раза в 5 с).
document.addEventListener('visibilitychange', () => { if (!document.hidden) MESH_FRESH = true; });

/* ---------- одна картина сети на всех серверах (ТЗ 24) ---------- */
function srvById(id){ return ((MESH && MESH.servers) || []).find(s => s.id === id); }
// devConns — связь устройства с каждым сервером сети (старая панель без
// conns — одна связь с тем сервером, через который оно пришло).
function devConns(d){
  if (d.conns && d.conns.length) return d.conns;
  return [{server: d.server, online: d.online, rttMs: d.rttMs, rttAvgMs: d.rttAvgMs, quality: d.quality,
           bytesUp: d.bytesUp, bytesDown: d.bytesDown}];
}
function ageText(sec){ return sec < 60 ? t('ago_now') : fill('ago_back', {t: fmtAgo(Date.now() / 1000 - sec)}); }
function qOfMs(v){ return !v ? 'unknown' : v < 60 ? 'good' : v < 150 ? 'fair' : 'poor'; }
// connText — «Германия · 5 мс, London · 30 мс» (только те, с кем на связи).
function connText(d){
  const on = devConns(d).filter(c => c.online);
  if (!on.length) return t('dev_conns_none');
  return on.map(c => serverName(c.server) + (c.rttMs ? ' · ' + fmtMs(c.rttMs) : '')).join(', ');
}
function whyText(m){
  const key = 'why_' + String(m.code || 'unknown').replace(/-/g, '_');
  return fill(I18N[LANG][key] ? key : 'why_unknown', {s: serverName(m.server), b: serverName(m.by || ''), h: m.host || ''});
}
// copyFlash — «Скопировано» облачком над anchor (ТЗ 30): облачко висит
// поверх страницы (position:fixed в <body>), поэтому ни строка, ни колонка
// не сдвигаются; само значение на миг подсвечивается цветом, без смены
// размеров. place (ТЗ 34, необязательно) — прямоугольник {left, right, top,
// bottom} или функция, его отдающая: облачко — в его середине (в списке
// устройств — пустое место между «Адрес» и «Система» той же строки). Узко —
// от левого края прямоугольника, но всё равно в той же строке: соседнюю
// строку облачко не закрывает.
function copyFlash(anchor, ok, place){
  document.querySelectorAll('.copy-tip').forEach(x => x.remove());
  const tip = el('div', 'copy-tip' + (ok ? '' : ' fail'), ok ? t('copied_short') : t('copy_failed_short'));
  document.body.appendChild(tip);
  const r = anchor.getBoundingClientRect();
  const w = tip.offsetWidth, h = tip.offsetHeight;
  const box = typeof place === 'function' ? place() : place;
  let left, top;
  if (box && box.bottom - box.top >= h && box.right > 0){
    const gap = box.right - box.left;
    left = gap >= w + 12 ? box.left + (gap - w) / 2 : box.left + 6;
    top = box.top + (box.bottom - box.top - h) / 2;
  } else {
    top = r.top - h - 6;
    if (top < 4) top = r.bottom + 6;
    left = r.left + r.width / 2 - w / 2;
  }
  tip.style.left = Math.max(6, Math.min(left, innerWidth - w - 6)) + 'px';
  tip.style.top = top + 'px';
  anchor.classList.remove('copy-ok', 'copy-bad');
  void anchor.offsetWidth;                       // перезапуск подсветки при повторном щелчке
  anchor.classList.add(ok ? 'copy-ok' : 'copy-bad');
  setTimeout(() => { tip.classList.add('out'); anchor.classList.remove('copy-ok', 'copy-bad'); }, 1100);
  setTimeout(() => tip.remove(), 1400);
}
// copyable — щелчок по node копирует text; «Скопировано» — copyFlash.
// opts: hint — ключ подсказки при наведении (по умолчанию copy_hint),
// place — где показать «Скопировано» (см. copyFlash).
function copyable(node, text, opts){
  if (!text || text === '—') return node;
  opts = opts || {};
  node.classList.add('copy');
  node.title = t(opts.hint || 'copy_hint');
  node.addEventListener('click', async e => {
    e.stopPropagation();
    e.preventDefault();
    // Место — в момент щелчка: пока пишется буфер, опрос может перерисовать
    // список, и у старой строки размеров уже не будет.
    const box = opts.place && opts.place();
    copyFlash(node, await copyText(text), box);
  });
  return node;
}

function meshDevices(){
  const nets = (MESH && MESH.networks) || [];
  if (!nets.length) return [];
  if (!nets.some(n => n.id === MESH_NET)) MESH_NET = nets[0].id;
  return nets.find(n => n.id === MESH_NET).devices || [];
}
function serverName(id){
  const s = ((MESH && MESH.servers) || []).find(x => x.id === id);
  return s ? s.name : '—';
}
function svcText(u){
  if (!u || !u.installed) return t('svc_missing');
  return u.active ? t('svc_active') : t('svc_stopped') + (u.detail ? ' (' + u.detail + ')' : '');
}

// renderMesh — картина сети пришла или поменялась: каркас рисует общее
// (ошибка службы, выбор сети), разделы — своё по событию «mesh».
function renderMesh(){
  const m = MESH;
  if (!m) return;
  const role = m.effectiveRole;
  const err = $('meshAdminErr');
  err.hidden = role !== 'main' || !m.adminError || (m.job && m.job.running);
  err.textContent = t('mesh_admin_err').replace('{e}', m.adminError || '');
  renderNetChips();
  emitPanel('mesh', m, role);
}

/* ---------- выбор сети (вверху) и раздел «Сети» ---------- */
function netLabel(n, i){ return n.name || t('net_n').replace('{n}', i + 1); }
function mainNets(){ return (MESH && MESH.effectiveRole === 'main' && MESH.networks) || []; }
function pickNet(id){ MESH_NET = id; remesh(); }
function renderNetChips(){
  const box = $('netChips');
  const nets = mainNets();
  box.hidden = nets.length < 2;
  if (box.hidden) return;
  meshDevices(); // поправит MESH_NET, если выбранной сети больше нет
  const sig = LANG + '|' + MESH_NET + '|' + nets.map(n => n.id + ':' + (n.name || '') + ':' + (n.devices || []).length).join(',');
  if (box.dataset.sig === sig) return;
  box.dataset.sig = sig;
  box.innerHTML = '';
  box.appendChild(el('span', 'lbl', t('mesh_network')));
  nets.forEach((n, i) => {
    const b = el('button', n.id === MESH_NET ? 'on' : '', netLabel(n, i) + ' · ' + (n.devices || []).length);
    b.type = 'button';
    b.addEventListener('click', () => pickNet(n.id));
    box.appendChild(b);
  });
}
/* ======================================================================
   Сети, журнал и простой режим доступа (заход 2, ТЗ 5)
   ====================================================================== */

/* ---------- старый meshd ---------- */
// Старый meshd отвечает на новые ручки текстом meshsvc.ErrUnsupported —
// страница показывает подсказку обновить meshd и работает дальше.
const OLD_MESHD = 'эта версия meshd не умеет этого';
let OLD_SEEN = false;
function isOldMeshd(data){
  const old = !!(data && typeof data.error === 'string' && data.error.startsWith(OLD_MESHD));
  if (old && !OLD_SEEN){ OLD_SEEN = true; renderOldHints(); }
  return old;
}
function meshErr(data){ return isOldMeshd(data) ? t('old_meshd') : errOf(data); }
// Старый meshd не присылает дату появления сети — так его видно и без ошибки.
function oldMeshdLikely(){
  const nets = mainNets();
  return OLD_SEEN || (nets.length > 0 && nets.every(n => !n.created));
}
function renderOldHints(){
  const old = oldMeshdLikely();
  ['netsOldHint', 'jrOldHint', 'smOldHint'].forEach(id => {
    const e = $(id);
    if (!e) return;
    e.hidden = !old;
    e.textContent = t('old_meshd');
  });
}
document.querySelectorAll('[data-go-settings]').forEach(b => b.addEventListener('click', () => showTab('Settings')));

// Текст ошибки сервера: перевод по коду, если он есть, иначе как прислали.
function errOf(data){
  const code = (data && data.error) || 'internal';
  return (I18N[LANG] && I18N[LANG]['err_' + code]) || (code === 'internal' ? t('err_internal') : code);
}
function fill(key, vals){
  let s = t(key);
  Object.keys(vals || {}).forEach(k => { s = s.split('{' + k + '}').join(vals[k]); });
  return s;
}
function fmtDay(unix){
  if (!unix) return '—';
  return new Date(unix * 1000).toLocaleDateString(LANG === 'ru' ? 'ru-RU' : 'en-US', {day:'numeric', month:'long', year:'numeric'});
}

/* ---------- общее для разделов про устройства ---------- */
// directText — с кем у устройства прямое соединение, одной строкой.
function directText(d){
  const list = (d.direct || []).map(l => {
    const o = meshDevices().find(x => x.ip === l.ip);
    return (o ? o.display : l.ip) + (l.rttMs ? ' · ' + fmtMs(l.rttMs) : '');
  });
  return list.join(', ');
}

function natLabel(n){
  if (!n) return t('nat_unknown');
  if (!n.udp) return t('nat_udp_blocked');
  let s = n.mapping === 'independent' ? t('nat_easy') : n.mapping === 'dependent' ? t('nat_hard') : t('nat_unknown');
  if (n.filtering === 'open') s += ', ' + t('nat_open');
  if (n.ipv6) s += ', IPv6';
  return s;
}
// focusDevice — открыть устройство в «Устройствах» (схема, «Сети»).
function focusDevice(id){ emitPanel('focusDevice', id); }
// refreshJournal — после действия, которое пишет в журнал сети.
function refreshJournal(){ emitPanel('journal'); }
// systemText — «Windows 10 Pro», «Android 14»: название системы один раз,
// даже если устройство само прислало его в версии («Windows Windows 10…»).
function systemText(d){
  const name = PLATFORM_NAME[d.platform] || d.platform || '';
  const os = (d.os || '').trim();
  if (!os) return name;
  if (!name) return os;
  return os.toLowerCase().includes(name.toLowerCase()) ? os : name + ' ' + os;
}

// meshdLabel — какой meshd стоит: «7 · v1.10.5»; собранный руками — «7 · файл
// от 19 ноября 2026 г.» (ТЗ 25: по одному номеру протокола не видно, обновился ли).
function meshdLabel(m){
  if (!m || !m.version) return '—';
  if (m.release) return m.version + ' · ' + m.release;
  if (m.built) return m.version + ' · ' + fill('meshd_file', {d: fmtDay(m.built)});
  return m.version;
}
// svgEl — элемент SVG с атрибутами (схема, графики «Мониторинга»).
function svgEl(tag, attrs, parent){
  const e = document.createElementNS(NS, tag);
  for (const k in attrs) e.setAttribute(k, attrs[k]);
  if (parent) parent.appendChild(e);
  return e;
}
