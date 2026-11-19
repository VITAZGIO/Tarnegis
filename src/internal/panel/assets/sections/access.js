// Раздел «Доступ»: простой режим, расширенный (группы и правила, проверка
// «может ли A к B»), журнал сети.
function renderAccess(){
  $('accEnabled').checked = !!ACCESS.enabled;
  const box = $('accGroups');
  box.innerHTML = '';
  ACCESS.groups.forEach(g => {
    const chip = document.createElement('span');
    chip.className = 'g';
    chip.textContent = groupLabel(g);
    const x = document.createElement('button');
    x.textContent = '×';
    x.addEventListener('click', () => {
      ACCESS.groups = ACCESS.groups.filter(v => v !== g);
      ACCESS.rules = ACCESS.rules.filter(r => r.from !== g && r.to !== g);
      renderAccess();
    });
    chip.appendChild(x);
    box.appendChild(chip);
  });
  if (!ACCESS.groups.length) accNote(box, 'acc_no_groups');

  const rules = $('accRules');
  rules.innerHTML = '';
  const opts = withServers => ['*'].concat(ACCESS.groups, withServers ? ['@servers'] : []);
  ACCESS.rules.forEach((r, i) => {
    const row = document.createElement('div');
    row.className = 'acc-rule';
    const sel = (val, withServers, key) => {
      const s = document.createElement('select');
      opts(withServers).forEach(g => {
        const o = document.createElement('option');
        o.value = g; o.textContent = groupLabel(g);
        s.appendChild(o);
      });
      s.value = val;
      s.addEventListener('change', () => { ACCESS.rules[i][key] = s.value; });
      return s;
    };
    const arrow = document.createElement('span');
    arrow.textContent = '→';
    const ports = document.createElement('input');
    ports.type = 'text';
    ports.placeholder = t('acc_ports_ph');
    ports.value = r.ports || '';
    ports.addEventListener('input', () => { ACCESS.rules[i].ports = ports.value; });
    const del = document.createElement('button');
    del.className = 'btn ghost small';
    del.textContent = '×';
    del.addEventListener('click', () => { ACCESS.rules.splice(i, 1); renderAccess(); });
    [sel(r.from, false, 'from'), arrow, sel(r.to, true, 'to'), ports, del].forEach(el => row.appendChild(el));
    rules.appendChild(row);
  });
  if (!ACCESS.rules.length) accNote(rules, 'acc_no_rules');
}
$('accEnabled').addEventListener('change', e => { ACCESS.enabled = e.target.checked; });
$('accAddGroupBtn').addEventListener('click', () => {
  const g = $('accNewGroup').value.trim();
  if (!g || ACCESS.groups.includes(g)) return;
  ACCESS.groups.push(g);
  $('accNewGroup').value = '';
  renderAccess();
});
$('accAddRuleBtn').addEventListener('click', () => {
  ACCESS.rules.push({from:'*', to:'*', ports:''});
  renderAccess();
});
$('accSaveBtn').addEventListener('click', async () => {
  showMsg($('accessMsg'), '', '');
  // Включение и выключение правил — только с подтверждения (ТЗ 24).
  if (!!ACCESS.enabled !== !!(ACCESS_RAW && ACCESS_RAW.enabled) &&
      !await appConfirm(t(ACCESS.enabled ? 'acc_on_confirm' : 'acc_off_confirm'), t(ACCESS.enabled ? 'acc_on_yes' : 'acc_off_yes'))) return;
  const {ok, data} = await api('/api/access', ACCESS);
  if (!ok){ showMsg($('accessMsg'), errText(data.error, data), 'err'); return; }
  showMsg($('accessMsg'), data.warning || t('acc_saved'), data.warning ? 'err' : 'ok');
  loadAccess();
});
/* ---------- журнал на «Обзоре» ---------- */
/* Опрос с after=<последний seq>. Журнал meshd живёт в памяти: после
   перезапуска meshd seq снова с 1 — ответ с seq меньше нашего after (или
   новое время старта meshd) значит «начать заново». Старые строки при этом
   остаются, ключ записи — «эпоха:seq», так повторов не бывает. */
const JR_MAX = 200;
const JR = {seq: 0, epoch: 0, items: [], keys: new Set(), filter: localStorage.getItem('panelJr') || 'all',
            timer: null, busy: false, started: 0, old: false, err: ''};
const JR_CONN = ['connect', 'disconnect', 'new-device', 'pending', 'approve', 'server-up', 'server-down'];

function jrRestart(){
  JR.seq = 0;
  JR.epoch++;
  if (JR.items.length) JR.items.push({k: 'r' + JR.epoch, marker: true, time: Date.now() / 1000});
}

async function loadJournal(){
  clearTimeout(JR.timer);
  if ($('viewMain').hidden) return;
  const main = MESH && MESH.effectiveRole === 'main';
  $('jrCard').hidden = !main;
  if (main && !JR.busy){
    if (MESH.startedAt && JR.started && MESH.startedAt !== JR.started) jrRestart();
    if (MESH.startedAt) JR.started = MESH.startedAt;
    JR.busy = true;
    try {
      for (let page = 0; page < 6; page++){
        const after = JR.seq;
        const {ok, data} = await api('/api/mesh/events?after=' + after + '&limit=1000');
        if (!ok){ JR.old = isOldMeshd(data); JR.err = JR.old ? '' : meshErr(data); break; }
        JR.old = false; JR.err = '';
        if (after > 0 && data.seq < after){ jrRestart(); continue; }
        const evs = data.events || [];
        evs.forEach(e => {
          const k = JR.epoch + ':' + e.seq;
          if (JR.keys.has(k)) return;
          JR.keys.add(k);
          JR.items.push({k, e, time: e.time});
          if (e.seq > JR.seq) JR.seq = e.seq;
        });
        if (evs.length < 1000) break;
      }
      if (JR.items.length > JR_MAX) JR.items.splice(0, JR.items.length - JR_MAX).forEach(x => JR.keys.delete(x.k));
    } catch (e) { /* сеть моргнула — следующий опрос */ }
    JR.busy = false;
    renderJournal();
  }
  JR.timer = setTimeout(loadJournal, document.hidden ? 15000 : 5000);
}

// Имя устройства по id: сначала в сети события, потом во всех; не нашлось —
// id как есть (устройство могли уже убрать).
function evName(net, id){
  if (!id) return '';
  const nets = (MESH && MESH.networks) || [];
  const order = nets.filter(n => n.id === net).concat(nets.filter(n => n.id !== net));
  for (const n of order){
    const d = (n.devices || []).find(x => x.id === id);
    if (d) return d.display || d.name || id;
  }
  const s = ((MESH && MESH.servers) || []).find(x => x.id === id);
  return s ? s.name : id;
}
function evNet(id){
  const nets = (MESH && MESH.networks) || [];
  const i = nets.findIndex(n => n.id === id);
  if (i >= 0) return netLabel(nets[i], i);
  return id ? id.slice(0, 8) + '…' : '';
}
const PORT_KEYS = {22: 'port_ssh', 3389: 'port_rdp', 47835: 'port_files', 47837: 'port_clip'};
function portText(p){
  if (!p) return t('port_any');
  return PORT_KEYS[p] ? t(PORT_KEYS[p]) + ' (' + p + ')' : t('port_n').replace('{n}', p);
}
function evText(e){
  const d = evName(e.net, e.device), p = evName(e.net, e.peer);
  const own = evText16(e, d);
  if (own) return own;
  switch (e.type){
    case 'connect': return fill('ev_connect', {d});
    case 'disconnect': return fill('ev_disconnect', {d});
    case 'new-device': return fill('ev_new', {d: e.text || d});
    case 'denied': return fill('ev_denied', {d, p, port: portText(e.port)});
    case 'rename':
      if (e.device) return fill('ev_rename_dev', {d: e.text || d});
      return e.text ? fill('ev_rename_net', {n: evNet(e.net), t: e.text}) : fill('ev_rename_net_clear', {n: evNet(e.net)});
    case 'forget': return fill('ev_forget', {d});
    case 'net-delete': return fill('ev_net_delete', {n: e.text || evNet(e.net)});
    case 'server-up': return fill('ev_server_up', {s: e.text || d});
    case 'server-down': return fill('ev_server_down', {s: e.text || d});
  }
  return e.type + (e.text ? ': ' + e.text : '');
}
function jrTime(unix){
  const d = new Date(unix * 1000), now = new Date();
  const loc = LANG === 'ru' ? 'ru-RU' : 'en-US';
  const hm = d.toLocaleTimeString(loc, {hour: '2-digit', minute: '2-digit'});
  return d.toDateString() === now.toDateString() ? hm : d.toLocaleDateString(loc, {day: '2-digit', month: '2-digit'}) + ' ' + hm;
}
function renderJournal(){
  document.querySelectorAll('#jrFilter button').forEach(b => b.classList.toggle('on', b.dataset.f === JR.filter));
  renderOldHints();
  const box = $('journal');
  box.innerHTML = '';
  const many = ((MESH && MESH.networks) || []).length > 1;
  const list = JR.items.filter(x => x.marker ? JR.filter === 'all'
    : JR.filter === 'denied' ? x.e.type === 'denied'
    : JR.filter === 'conn' ? JR_CONN.includes(x.e.type) : true).slice().reverse();
  $('jrErr').hidden = !JR.err;
  $('jrErr').textContent = JR.err;
  list.forEach(x => {
    const row = el('div', 'jr-row' + (x.marker ? ' marker' : x.e.type === 'denied' ? ' denied' : ''));
    row.appendChild(el('time', '', jrTime(x.time)));
    const txt = el('span', 'tx', x.marker ? t('ev_restart') : evText(x.e));
    if (!x.marker && many && x.e.net) txt.appendChild(el('span', 'badge dim', evNet(x.e.net)));
    row.appendChild(txt);
    box.appendChild(row);
  });
  if (!list.length && !JR.old) box.appendChild(el('div', 'empty', t(JR.items.length ? 'jr_none_filter' : 'jr_none')));
}
$('jrFilter').addEventListener('click', e => {
  const b = e.target.closest('button');
  if (!b) return;
  JR.filter = b.dataset.f;
  localStorage.setItem('panelJr', JR.filter);
  renderJournal();
});

/* ---------- простой режим доступа ---------- */
/* Поверх обычной политики (/api/access: группы + правила, членство —
   /api/clients/groups). Простому режиму принадлежат только группы с «~»:
     ~mine, ~server, ~guest — роли (метка роли и общий источник правил);
     ~tun_<16 hex>          — личная группа SSH-пользователя, заводится,
                              только когда без неё не выразить строку;
   и правила «~группа → ~группа | * | @servers» без портов. Всё остальное —
   ручное из «Расширенного»: сохраняется как есть, стоит в начале списка, а
   то, что оно разрешает, в таблице под замком.
   Таблица (кто → к кому, любой порт) пересчитывается из правил тем же
   решением, что у meshd, и при сохранении собирается обратно в правила
   заново целиком; перед отправкой результат проверяется обратным
   пересчётом — не совпало или не влезло в 64 группы / 256 правил —
   ничего не сохраняется. */
const SM_ROLES = ['mine', 'server', 'guest'];
const SM_GROUP = {mine: '~mine', server: '~server', guest: '~guest'};
const SM_MAX_GROUPS = 64, SM_MAX_RULES = 256;
const SRV = '@servers';
let ACCESS_RAW = {enabled: false, groups: [], rules: []}; // как на сервере, без заготовки редактора
let SM_WORK = null, SM_REV = 0, SM_TIMER = null, SM_SAVING = false;

function smOwn(g){ return typeof g === 'string' && g.startsWith('~'); }
function smOwnRule(r){ return !String(r.ports || '').trim() && smOwn(r.from) && (smOwn(r.to) || r.to === '*' || r.to === SRV); }
function smActive(){ return (ACCESS_RAW.groups || []).some(g => SM_ROLE_OF[g]); }
function smHand(){ return (ACCESS_RAW.rules || []).filter(r => !smOwnRule(r)); }
function smClients(){
  const by = {};
  CLIENTS.forEach(c => { if (c.username) by[c.username] = c; });
  return by;
}
function smRoleOf(groups){
  for (const g of groups || []) if (SM_ROLE_OF[g]) return SM_ROLE_OF[g];
  return '';
}
// Решение meshd (policy.member/match) для «любого порта»: подходит только
// правило без портов. partial — есть правило только на часть портов.
function smMember(groupsOf, user, g){
  if (g === '*') return true;
  if (user === SRV) return g === SRV;
  return (groupsOf[user] || []).includes(g);
}
function smMatch(rules, groupsOf, from, to){
  if (from === SRV) return -1;
  for (let i = 0; i < rules.length; i++){
    const r = rules[i];
    if (!String(r.ports || '').trim() && smMember(groupsOf, from, r.from) && smMember(groupsOf, to, r.to)) return i;
  }
  return -1;
}
function smPartial(rules, groupsOf, from, to){
  return rules.some(r => String(r.ports || '').trim() && smMember(groupsOf, from, r.from) && smMember(groupsOf, to, r.to));
}
// Сколько устройств у пользователя во всех сетях: у одиночного «к себе»
// ничего не значит, и этой клеткой можно пожертвовать ради короткого правила.
function smDevCount(){
  const n = {};
  ((MESH && MESH.networks) || []).forEach(net => (net.devices || []).forEach(d => { if (d.user) n[d.user] = (n[d.user] || 0) + 1; }));
  return n;
}
function smGroupsOf(){
  const g = {};
  CLIENTS.forEach(c => { if (c.username) g[c.username] = (c.groups || []).slice(); });
  return g;
}

// smDecode — таблица и роли из того, что сейчас на сервере.
function smDecode(){
  const by = smClients(), users = Object.keys(by).sort(), groupsOf = smGroupsOf();
  const rules = ACCESS_RAW.rules || [];
  const M = {}, roles = {};
  users.forEach(a => {
    roles[a] = smRoleOf(groupsOf[a]);
    M[a] = {};
    users.concat([SRV]).forEach(b => { M[a][b] = smMatch(rules, groupsOf, a, b) >= 0; });
  });
  return {M, roles};
}
function smCopy(w){ return {M: JSON.parse(JSON.stringify(w.M)), roles: Object.assign({}, w.roles)}; }
function smCur(){ return SM_WORK || smDecode(); }

// smEncode — правила и группы для таблицы M и ролей roles. hand — ручные
// правила, которые остаются как есть (по умолчанию — те, что на сервере).
function smEncode(M, roles, hand){
  hand = hand || smHand();
  const by = smClients(), users = Object.keys(by).sort(), cols = users.concat([SRV]);
  const cnt = smDevCount();
  const free = a => (cnt[a] || 0) <= 1;           // «к себе» не важно
  const handGroupsOf = u => (by[u].groups || []).filter(g => !smOwn(g));
  const members = R => users.filter(u => roles[u] === R);

  // Ручные правила считаем при «все личные группы есть»: они могут на них
  // ссылаться. Итог всё равно проверяется обратным пересчётом.
  const probe = {};
  users.forEach(u => { probe[u] = handGroupsOf(u).concat(roles[u] ? [SM_GROUP[roles[u]]] : [], ['~' + u]); });
  const H = {};
  users.forEach(a => { H[a] = {}; cols.forEach(b => { H[a][b] = smMatch(hand, probe, a, b) >= 0; }); });

  const sig = {};
  users.forEach(a => {
    const row = M[a] || {};
    const ok = c => !!row[c] || (c === a && free(a));
    const left = new Set(cols.filter(c => row[c] && !H[a][c] && !(c === a && free(a))));
    // Всё нужное уже дают ручные правила — своих не надо.
    if (!left.size){ sig[a] = []; return; }
    if (cols.every(ok)){ sig[a] = ['*']; return; }
    const out = [];
    {
      SM_ROLES.forEach(R => {
        const mem = members(R);
        if (mem.length && mem.some(m => left.has(m)) && mem.every(ok)){
          out.push(SM_GROUP[R]);
          mem.forEach(m => left.delete(m));
        }
      });
      if (left.delete(SRV)) out.push(SRV);
      left.forEach(b => out.push('~' + b));
    }
    sig[a] = out.sort();
  });

  const own = [], personal = new Set(), done = new Set();
  const emit = (from, targets) => targets.forEach(to => {
    own.push({from, to, ports: ''});
    if (SM_PERSONAL.test(to)) personal.add(to.slice(1));
  });
  // Источник — роль: её правила получает каждый в роли, поэтому берём самую
  // частую строку среди тех, что никому в роли не дают лишнего; у кого
  // строка шире — добавка личной группой.
  const covers = (m, g) => {
    const row = M[m] || {};
    const ok = c => !!row[c] || (c === m && free(m));
    if (g === '*') return cols.every(ok);
    if (g === SRV) return ok(SRV);
    if (SM_ROLE_OF[g]) return members(SM_ROLE_OF[g]).every(ok);
    if (SM_PERSONAL.test(g)) return ok(g.slice(1));
    return false;
  };
  SM_ROLES.forEach(R => {
    const mem = members(R);
    if (!mem.length) return;
    const counts = {};
    mem.forEach(m => { const k = sig[m].join('|'); counts[k] = (counts[k] || 0) + 1; });
    let base = null, baseN = 0;
    Object.keys(counts).forEach(k => {
      const tg = k ? k.split('|') : [];
      if (tg.length && counts[k] > baseN && mem.every(m => tg.every(g => covers(m, g)))){ base = tg; baseN = counts[k]; }
    });
    if (!base) return;
    emit(SM_GROUP[R], base);
    mem.forEach(m => {
      const extra = sig[m].filter(g => !base.includes(g));
      if (extra.length){ personal.add(m); emit('~' + m, extra); }
      done.add(m);
    });
  });
  users.forEach(a => {
    if (done.has(a) || !sig[a].length) return;
    personal.add(a);
    emit('~' + a, sig[a]);
  });
  // Личные и прочие «~»-группы, на которые ссылаются ручные правила, остаются.
  const keepOwn = new Set();
  hand.forEach(r => [r.from, r.to].forEach(g => {
    if (!smOwn(g)) return;
    keepOwn.add(g);
    if (SM_PERSONAL.test(g) && by[g.slice(1)]) personal.add(g.slice(1));
  }));

  const clientGroups = {};
  users.forEach(u => {
    clientGroups[u] = [...new Set(handGroupsOf(u).concat(roles[u] ? [SM_GROUP[roles[u]]] : [],
      personal.has(u) ? ['~' + u] : []))].sort();
  });
  const handGroups = (ACCESS_RAW.groups || []).filter(g => !smOwn(g));
  const groups = [...new Set(handGroups.concat(Object.values(SM_GROUP), [...personal].map(u => '~' + u), [...keepOwn]))].sort();
  const rules = hand.concat(own);

  if (groups.length > SM_MAX_GROUPS) return {error: fill('sm_err_groups', {n: groups.length, max: SM_MAX_GROUPS})};
  if (rules.length > SM_MAX_RULES) return {error: fill('sm_err_rules', {n: rules.length, max: SM_MAX_RULES})};
  for (const a of users) for (const b of cols){
    if (b === a && free(a)) continue;
    if ((smMatch(rules, clientGroups, a, b) >= 0) !== !!(M[a] && M[a][b])) return {error: t('sm_err_verify')};
  }
  return {groups, rules, clientGroups, own: own.length};
}

// Сохранение — одним запросом (ТЗ 16): права и группы всех клиентов сразу,
// сервер проверяет всё до первой записи. Раньше было несколько запросов, и
// закрытая на середине вкладка оставляла доступ уже, чем задумано.
async function smSaveNow(M, roles, hand){
  const enc = smEncode(M, roles, hand);
  if (enc.error){ smMsg(enc.error, 'err'); toast(enc.error, 'err'); return false; }
  const by = smClients(), clients = {};
  Object.keys(enc.clientGroups).forEach(u => { if (by[u]) clients[by[u].id] = enc.clientGroups[u]; });
  const r = await api('/api/access/simple', {enabled: !!ACCESS_RAW.enabled, groups: enc.groups, rules: enc.rules, clients});
  if (!r.ok){
    const e = errOf(r.data); smMsg(e, 'err'); toast(e, 'err');
    await smReload();
    return false;
  }
  const warn = r.data.warning || '';
  await smReload();
  smMsg(warn || t('acc_saved'), warn ? 'err' : 'ok');
  return true;
}
async function smReload(){ await loadClients(); await loadAccess(); }

// Правка: копится в SM_WORK, уходит одним сохранением через полсекунды
// после последнего нажатия; пока сохраняется — следующее ждёт.
function smEdit(fn){
  if (!SM_WORK) SM_WORK = smCopy(smDecode());
  fn(SM_WORK);
  SM_REV++;
  renderSimple();
  emitPanel('detail');
  clearTimeout(SM_TIMER);
  SM_TIMER = setTimeout(smFlush, 500);
}
async function smFlush(){
  if (SM_SAVING || !SM_WORK) return;
  SM_SAVING = true;
  const rev = SM_REV, snap = smCopy(SM_WORK);
  const ok = await smSaveNow(snap.M, snap.roles);
  SM_SAVING = false;
  if (!ok || rev === SM_REV) SM_WORK = null; else { smFlush(); return; }
  renderSimple();
  emitPanel('detail');
  runCheck();
}
function smMsg(text, kind){ showMsg($('smMsg'), text, kind); }

// Замки: что разрешено ручными правилами, простому режиму не снять.
function smLock(a, b){
  const by = smClients();
  if (!by[a] || (b !== SRV && !by[b])) return 'foreign';
  const groupsOf = smGroupsOf();
  const hand = smHand();
  const i = smMatch(hand, groupsOf, a, b);
  if (i >= 0) return 'hand:' + groupLabel(hand[i].from) + ' → ' + groupLabel(hand[i].to);
  return '';
}
function smToggle(a, b){
  const lock = smLock(a, b);
  if (lock){ toast(lock === 'foreign' ? t('sm_locked_foreign') : fill('sm_locked', {r: lock.slice(5)}), 'err'); return; }
  smEdit(w => { w.M[a][b] = !w.M[a][b]; });
}
function smCols(){ return Object.keys(smClients()).sort().concat([SRV]); }
function smRow(a, val){
  smEdit(w => smCols().forEach(b => { if (!val && smLock(a, b)) return; w.M[a][b] = val; }));
}
// Роли — заготовки: «Моё» ходит ко всем, кроме гостей; «Сервер» сам
// никуда; «Гость» никуда и к нему никто. Дальше — галочками.
function smSetRole(a, R){
  smEdit(w => {
    w.roles[a] = R;
    smCols().forEach(b => {
      const want = R === 'mine' && (b === SRV || w.roles[b] !== 'guest');
      if (want || !smLock(a, b)) w.M[a][b] = want;
    });
    Object.keys(w.M).forEach(x => {
      if (R === 'guest' && x !== a && !smLock(x, a)) w.M[x][a] = false;
      // Новичок с ролью «Моё» видит всех «Моих» и наоборот.
      if (R === 'mine' && x !== a && w.roles[x] === 'mine' && !smLock(x, a)) w.M[x][a] = true;
    });
  });
}

// Строки таблицы: SSH-пользователи, у которых есть устройства в выбранной
// сети. Несколько устройств под одним пользователем — одна строка.
function smLines(){
  const by = smClients(), lines = [], seen = {};
  meshDevices().forEach(d => {
    const u = d.user || '';
    const key = by[u] ? u : '?' + (u || d.id);
    if (!seen[key]){ seen[key] = {key, user: u, client: by[u] || null, devices: []}; lines.push(seen[key]); }
    seen[key].devices.push(d);
  });
  lines.forEach(l => {
    l.name = l.devices.length === 1 ? (l.devices[0].display || l.devices[0].name) : (l.client ? l.client.name : l.devices[0].display);
  });
  return lines.sort((a, b) => (!a.client - !b.client) || a.name.localeCompare(b.name));
}
function roleTag(R){ return el('span', 'badge ' + (R === 'mine' ? 'ok' : R === 'guest' ? 'warn' : R ? 'dim' : 'err'), t('sm_role_' + (R || 'none'))); }

function renderSimple(){
  const card = $('smCard');
  if (!card) return;
  const main = MESH && MESH.effectiveRole === 'main';
  renderOldHints();
  const active = smActive();
  $('smStatus').innerHTML = '';
  const st = el('div', 'sm-status ' + (ACCESS_RAW.enabled ? 'on' : 'off'));
  st.appendChild(el('b', '', t(ACCESS_RAW.enabled ? 'sm_on' : 'sm_off')));
  st.appendChild(el('span', '', t(ACCESS_RAW.enabled ? 'sm_on_d' : 'sm_off_d')));
  const tg = el('button', 'btn small ' + (ACCESS_RAW.enabled ? 'ghost' : ''), t(ACCESS_RAW.enabled ? 'sm_turn_off' : 'sm_turn_on'));
  tg.addEventListener('click', smSetEnabled);
  st.appendChild(tg);
  $('smStatus').appendChild(st);

  $('smOff').hidden = active;
  $('smBody').hidden = !active;
  if (!active || !main || !CLIENTS_READY) return;

  const w = smCur(), lines = smLines(), by = smClients();
  const cols = lines.filter(l => l.client);
  const shared = lines.filter(l => l.client && l.devices.length > 1);
  $('smShared').hidden = !shared.length;
  $('smShared').textContent = shared.length ? fill('sm_shared', {list: shared.map(l => l.devices.map(d => d.display).join(' + ')).join('; ')}) : '';
  const foreign = lines.filter(l => !l.client);
  $('smForeign').hidden = !foreign.length;
  $('smForeign').textContent = foreign.length ? fill('sm_foreign', {list: foreign.map(l => l.name).join(', ')}) : '';

  const cellState = (a, b) => {
    const ua = a.user, ub = b === SRV ? SRV : b.user;
    if (a.client && (b === SRV || b.client)){
      const self = b !== SRV && a.key === b.key;
      if (self && a.devices.length < 2 && (smDevCount()[ua] || 0) < 2) return {self: true};
      const lock = smLock(ua, ub);
      const on = !!(w.M[ua] && w.M[ua][ub]);
      const part = !on && smPartial(ACCESS_RAW.rules || [], smGroupsOf(), ua, ub);
      return {on, lock, part, ua, ub};
    }
    // Чужой пользователь: только то, что дают ручные правила и «→ *».
    const groupsOf = smGroupsOf();
    return {on: smMatch(ACCESS_RAW.rules || [], groupsOf, ua || '?', ub || '?') >= 0, lock: 'foreign', ua, ub};
  };
  const mkCell = (a, b) => {
    const s = cellState(a, b);
    if (s.self) return el('span', 'cell self', '—');
    const c = el('button', 'cell' + (s.on ? ' y' : '') + (s.lock ? ' lock' : '') + (s.part ? ' part' : ''), s.on ? '✓' : s.part ? '◐' : '·');
    c.type = 'button';
    c.title = s.lock === 'foreign' ? t('sm_locked_foreign') : s.lock ? fill('sm_locked', {r: s.lock.slice(5)})
      : s.part ? t('sm_partial') : '';
    c.addEventListener('click', () => smToggle(s.ua, s.ub));
    return c;
  };
  const srvName = t('sm_servers');

  // Таблица (компьютер).
  const tbl = el('table', 'sm-table');
  const hr = el('tr');
  hr.appendChild(el('th', 'l', t('sm_who_whom')));
  cols.forEach(l => { const th = el('th', '', l.name); th.title = l.name; hr.appendChild(th); });
  const ths = el('th', '', srvName); ths.title = t('sm_servers_d'); hr.appendChild(ths);
  tbl.appendChild(hr);
  lines.forEach(a => {
    const tr = el('tr');
    const td = el('td', 'l');
    td.appendChild(el('span', 'nm', a.name));
    if (a.devices.length > 1) td.appendChild(el('span', 'badge dim', fill('sm_n_dev', {n: a.devices.length})));
    td.appendChild(a.client ? roleTag(w.roles[a.user]) : el('span', 'badge dim', t('sm_foreign_tag')));
    tr.appendChild(td);
    cols.forEach(b => { const c = el('td'); c.appendChild(mkCell(a, b)); tr.appendChild(c); });
    const c = el('td'); c.appendChild(mkCell(a, SRV)); tr.appendChild(c);
    tbl.appendChild(tr);
  });
  const wrap = $('smTable');
  wrap.innerHTML = '';
  wrap.appendChild(tbl);

  // Список (телефон): по устройству — галочки «куда может ходить».
  const list = $('smList');
  list.innerHTML = '';
  lines.forEach(a => {
    const box = el('div', 'sm-item');
    const h = el('div', 'sm-ih');
    h.appendChild(el('b', '', a.name));
    h.appendChild(a.client ? roleTag(w.roles[a.user]) : el('span', 'badge dim', t('sm_foreign_tag')));
    box.appendChild(h);
    const checks = el('div', 'sm-checks');
    cols.concat([SRV]).forEach(b => {
      const s = cellState(a, b);
      if (s.self) return;
      const lab = el('label', s.lock ? 'lock' : '');
      const cb = el('input');
      cb.type = 'checkbox';
      cb.checked = s.on;
      cb.disabled = !!s.lock;
      cb.title = s.lock === 'foreign' ? t('sm_locked_foreign') : s.lock ? fill('sm_locked', {r: s.lock.slice(5)}) : '';
      cb.addEventListener('change', () => smToggle(s.ua, s.ub));
      lab.appendChild(cb);
      lab.appendChild(document.createTextNode(' ' + (b === SRV ? srvName : b.key === a.key ? t('sm_self') : b.name)));
      checks.appendChild(lab);
    });
    box.appendChild(checks);
    list.appendChild(box);
  });
  $('smEmpty').hidden = lines.length > 0;
  $('smCounts').textContent = fill('sm_counts', {g: (ACCESS_RAW.groups || []).length, gm: SM_MAX_GROUPS,
    r: (ACCESS_RAW.rules || []).length, rm: SM_MAX_RULES});
}

async function smSetEnabled(){
  const on = !ACCESS_RAW.enabled;
  if (on && !(ACCESS_RAW.rules || []).length){
    if (!await appConfirm(t('sm_enable_norules'), t('sm_turn_on'))) return;
  } else if (!await appConfirm(t(on ? 'acc_on_confirm' : 'acc_off_confirm'), t(on ? 'acc_on_yes' : 'acc_off_yes'))) return;
  const {ok, data} = await api('/api/access', {enabled: on, groups: ACCESS_RAW.groups || [], rules: ACCESS_RAW.rules || []});
  if (!ok){ smMsg(errOf(data), 'err'); return; }
  await loadAccess();
  smMsg(data.warning || t('acc_saved'), data.warning ? 'err' : 'ok');
  runCheck();
}

// Включить простой режим. Ручные правила не трогаются никогда, кроме одного
// случая с явного согласия: единственное правило «все → все» (так начинает
// редактор) заменяется ролью «Моё» у всех — иначе галочкам нечего снимать.
async function smEnable(){
  const hand = smHand();
  const by = smClients();
  const lone = hand.length === 1 && hand[0].from === '*' && hand[0].to === '*' && !String(hand[0].ports || '').trim();
  const strange = (ACCESS_RAW.groups || []).filter(g => smOwn(g) && !SM_ROLE_OF[g] && !(SM_PERSONAL.test(g) && by[g.slice(1)]));
  const foreign = ((MESH && MESH.networks) || []).some(n => (n.devices || []).some(d => !by[d.user]));
  let msg;
  if (!hand.length) msg = t('sm_enable_empty');
  else if (lone) msg = t('sm_enable_default') + (foreign ? '\n\n' + t('sm_enable_foreign') : '');
  else msg = fill('sm_enable_hand', {n: hand.length});
  if (strange.length) msg += '\n\n' + fill('sm_enable_strange', {list: strange.join(', ')});
  if (!await appConfirm(msg, t('sm_enable_yes'))) return;
  const keep = lone ? [] : hand;
  const w = smDecode();
  // При смене «все → все» таблица считается заново: роль «Моё» у всех.
  Object.keys(w.roles).forEach(u => { if (!w.roles[u]) w.roles[u] = 'mine'; });
  if (lone || !hand.length){
    Object.keys(w.M).forEach(a => Object.keys(w.M[a]).forEach(b => {
      w.M[a][b] = w.roles[a] === 'mine' && (b === SRV || w.roles[b] !== 'guest');
    }));
  }
  await smSaveNow(w.M, w.roles, keep);
}

/* ---------- проверка «может ли A к B» ---------- */
const CK_PORTS = [[22, 'port_ssh'], [3389, 'port_rdp'], [47835, 'port_files'], [47837, 'port_clip'], [0, 'port_any']];
function fillCheckSelects(){
  const devs = meshDevices();
  const sig = LANG + '|' + MESH_NET + '|' + devs.map(d => d.id + ':' + d.display).join(',');
  const from = $('ckFrom'), to = $('ckTo'), port = $('ckPort');
  if (from.dataset.sig === sig) return;
  from.dataset.sig = sig;
  const keep = [from.value, to.value, port.value];
  [from, to].forEach(s => {
    s.innerHTML = '';
    devs.forEach(d => { const o = el('option', '', d.display || d.name); o.value = d.id; s.appendChild(o); });
  });
  port.innerHTML = '';
  CK_PORTS.forEach(([p, k]) => { const o = el('option', '', t(k) + (p ? ' (' + p + ')' : '')); o.value = p; port.appendChild(o); });
  if (devs.some(d => d.id === keep[0])) from.value = keep[0];
  if (devs.some(d => d.id === keep[1])) to.value = keep[1]; else if (devs.length > 1) to.selectedIndex = 1;
  if (keep[2] !== '') port.value = keep[2];
  runCheck();
}
let CK_SEQ = 0;
async function runCheck(){
  const v = $('ckVerdict');
  if (!v) return;
  const from = $('ckFrom').value, to = $('ckTo').value, port = Number($('ckPort').value || 0);
  const name = id => evName(MESH_NET, id);
  if (!from || !to){ v.className = 'verdict'; v.textContent = t('ck_need_two'); return; }
  if (from === to){ v.className = 'verdict n'; v.textContent = t('ck_same'); return; }
  const seq = ++CK_SEQ;
  const {ok, data} = await api('/api/mesh/check', {net: MESH_NET, from, to, port});
  if (seq !== CK_SEQ) return;
  if (!ok){ v.className = 'verdict n'; v.textContent = meshErr(data); return; }
  const vals = {a: name(from), b: name(to), port: portText(port)};
  let text;
  switch (data.reason){
    case 'rule': {
      const r = (ACCESS_RAW.rules || [])[data.rule];
      text = fill('ck_yes', vals) + (r ? ' ' + fill('ck_by_rule', {n: data.rule + 1, r: groupLabel(r.from) + ' → ' + groupLabel(r.to) + (r.ports ? ' (' + r.ports + ')' : '')}) : '');
      break;
    }
    case 'policy off': text = fill('ck_yes_off', vals); break;
    case 'no rule': text = fill('ck_no', vals); break;
    case 'server cannot initiate': text = t('ck_server'); break;
    case 'unknown device': text = t('ck_unknown'); break;
    default: text = data.reason || '';
  }
  if (data.reason === 'no rule' && data.visible) text += ' ' + t('ck_visible');
  v.className = 'verdict ' + (data.allowed ? 'y' : 'n');
  v.textContent = text;
}
['ckFrom', 'ckTo', 'ckPort'].forEach(id => $(id).addEventListener('change', runCheck));
$('smEnableBtn').addEventListener('click', smEnable);
// «Расширенное: группы и правила» — вкладка «Расширенный» (ТЗ 23); карточка
// правил там видна всегда.
$('accessCard').hidden = false;
$('accAdvBtn').addEventListener('click', () => { showSub('Access', 'adv'); scrollTo(0, 0); });
// evText16 — записи журнала об одобрении, заморозке и цепочке; '' — не наши.
function evText16(e, d){
  switch (e.type){
    case 'pending': return fill('ev_pending', {d: e.text || d});
    case 'approve': return fill('ev_approve', {d: e.text || d});
    case 'freeze': return fill('ev_freeze', {d: e.text || d});
    case 'unfreeze': return fill('ev_unfreeze', {d: e.text || d});
    case 'approval': return fill(e.text === 'on' ? 'ev_approval_on' : 'ev_approval_off', {n: evNet(e.net)});
    case 'exit': return fill('ev_exit', {d, t: e.text || ''});
  }
  return '';
}

/* ---------- «Куда может ходить» в карточке устройства ---------- */
function renderDevAccess(box, d){
  if (!d || !(MESH && MESH.effectiveRole === 'main')) return;
  const by = smClients();
  const part = el('div', 'dpart');
  const h = el('h2');
  h.appendChild(el('span', '', t('dev_part_access')));
  part.appendChild(h);
  if (!d.user){ part.appendChild(el('p', 'note', t('old_meshd'))); box.appendChild(part); return; }
  if (!by[d.user]){ part.appendChild(el('p', 'note', t('sm_locked_foreign'))); box.appendChild(part); return; }
  if (!smActive()){
    part.appendChild(el('p', 'note', t('sm_dev_inactive')));
    const go = el('button', 'btn ghost small', t('sm_open_access'));
    go.addEventListener('click', () => showTab('Access'));
    part.appendChild(go);
    box.appendChild(part);
    return;
  }
  const w = smCur(), a = d.user;
  if (!ACCESS_RAW.enabled) part.appendChild(el('p', 'msg err', t('sm_off_d')));
  const mates = meshDevices().filter(x => x.user === a && x.id !== d.id);
  if (mates.length) part.appendChild(el('p', 'note', fill('sm_dev_shared', {list: mates.map(x => x.display).join(', ')})));

  part.appendChild(el('div', 'k sm-k', t('sm_role')));
  const seg = el('div', 'seg');
  SM_ROLES.forEach(R => {
    const b = el('button', w.roles[a] === R ? 'on' : '', t('sm_role_' + R));
    b.type = 'button';
    b.title = t('sm_role_' + R + '_d');
    b.addEventListener('click', () => smSetRole(a, R));
    seg.appendChild(b);
  });
  part.appendChild(seg);

  const kh = el('div', 'k sm-k');
  kh.appendChild(el('span', '', t('sm_where')));
  const all = el('button', 'btn ghost small', t('sm_to_all'));
  all.addEventListener('click', () => smRow(a, true));
  const none = el('button', 'btn ghost small', t('sm_to_none'));
  none.addEventListener('click', () => smRow(a, false));
  kh.appendChild(all);
  kh.appendChild(none);
  part.appendChild(kh);
  const checks = el('div', 'sm-checks');
  const lines = smLines().filter(l => l.client);
  const targets = lines.map(l => ({b: l.user, name: l.user === a ? t('sm_self') : l.name, self: l.user === a}))
    .filter(x => !x.self || mates.length).concat([{b: SRV, name: t('sm_servers')}]);
  targets.forEach(x => {
    const lock = smLock(a, x.b);
    const lab = el('label', lock ? 'lock' : '');
    const cb = el('input');
    cb.type = 'checkbox';
    cb.checked = !!(w.M[a] && w.M[a][x.b]);
    cb.disabled = !!lock;
    cb.title = lock ? fill('sm_locked', {r: lock.slice(5)}) : '';
    cb.addEventListener('change', () => smToggle(a, x.b));
    lab.appendChild(cb);
    lab.appendChild(document.createTextNode(' ' + x.name));
    checks.appendChild(lab);
  });
  part.appendChild(checks);
  box.appendChild(part);
}

/* ---------- события каркаса ---------- */
onPanel('lang', () => {
  // Справка — те же тексты, что в «?» (свои строки, не ввод людей).
  $('accHelp').innerHTML = t('hint_acc') + t('hint_acc_rules');
  $('smRolesHelp').innerHTML = t('sm_roles_help');
  $('ckFrom').dataset.sig = '';
  if (!$('viewMain').hidden) renderJournal();
});
// Группы нужны редактору правил — грузим при каждом открытии раздела.
onPanel('show:Access', () => loadAccess());
onPanel('sub:Access', () => remesh());
onPanel('clients', first => { if (first) renderSimple(); });
onPanel('access', raw => {
  ACCESS_RAW = raw;
  renderAccess();
  renderSimple();
});
onPanel('mesh', (m, role) => {
  if (role === 'main'){ fillCheckSelects(); renderSimple(); }
});
onPanel('meshLoaded', () => { if (!JR.timer && !JR.busy) loadJournal(); });
onPanel('journal', () => loadJournal());
onPanel('deviceCard', (box, d) => renderDevAccess(box, d));
