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
// jrClass — цвет строки консоли по типу события.
const JR_OK = ['connect', 'new-device', 'approve', 'server-up', 'unfreeze'];
const JR_WARN = ['disconnect', 'server-down', 'pending', 'freeze', 'forget', 'net-delete'];
function jrClass(x){
  if (x.marker) return 'marker';
  const tp = x.e.type;
  return tp === 'denied' ? 'denied' : JR_OK.includes(tp) ? 'conn' : JR_WARN.includes(tp) ? 'warn' : '';
}
// jrList — что показано сейчас (по фильтру), старые сверху.
function jrList(){
  return JR.items.filter(x => x.marker ? JR.filter === 'all'
    : JR.filter === 'denied' ? x.e.type === 'denied'
    : JR.filter === 'conn' ? JR_CONN.includes(x.e.type) : true);
}
function jrLine(x){ return x.marker ? t('ev_restart') : evText(x.e); }
function jrAtEnd(box){ return box.scrollHeight - box.scrollTop - box.clientHeight < 30; }
// Новые — снизу, как в консоли: стоял внизу — остаёшься внизу; отлистал
// вверх читать — не дёргаем, а показываем «к последним».
function renderJournal(){
  document.querySelectorAll('#jrFilter button').forEach(b => b.classList.toggle('on', b.dataset.f === JR.filter));
  renderOldHints();
  const box = $('journal');
  const stick = !box.dataset.drawn || jrAtEnd(box);
  box.innerHTML = '';
  const many = ((MESH && MESH.networks) || []).length > 1;
  const list = jrList();
  $('jrErr').hidden = !JR.err;
  $('jrErr').textContent = JR.err;
  list.forEach(x => {
    const row = el('div', 'jr-row ' + jrClass(x));
    row.appendChild(el('time', '', jrTime(x.time)));
    const txt = el('span', 'tx', jrLine(x));
    if (!x.marker && many && x.e.net) txt.appendChild(el('span', 'net', '[' + evNet(x.e.net) + ']'));
    row.appendChild(txt);
    box.appendChild(row);
  });
  if (!list.length && !JR.old) box.appendChild(el('div', 'empty', t(JR.items.length ? 'jr_none_filter' : 'jr_none')));
  if (box.offsetParent) box.dataset.drawn = '1';
  if (stick) box.scrollTop = box.scrollHeight;
  $('jrLast').hidden = jrAtEnd(box);
}
// jrText — показанное сейчас текстом: полная дата и время, как в программе.
function jrText(){
  const many = ((MESH && MESH.networks) || []).length > 1;
  const p = n => String(n).padStart(2, '0');
  return jrList().map(x => {
    const d = new Date(x.time * 1000);
    const ts = d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
    return ts + '  ' + jrLine(x) + (!x.marker && many && x.e.net ? '  [' + evNet(x.e.net) + ']' : '');
  }).join('\n');
}
$('journal').addEventListener('scroll', () => { $('jrLast').hidden = jrAtEnd($('journal')); });
$('jrLast').addEventListener('click', () => {
  const box = $('journal');
  box.scrollTop = box.scrollHeight;
  $('jrLast').hidden = true;
});
$('jrCopy').addEventListener('click', async () => {
  const txt = jrText();
  if (!txt){ toast(t('jr_nothing'), 'err'); return; }
  toast(await copyText(txt) ? t('copied') : t('copy_failed'), 'ok');
});
$('jrSave').addEventListener('click', () => {
  const txt = jrText();
  if (!txt){ toast(t('jr_nothing'), 'err'); return; }
  const p = n => String(n).padStart(2, '0'), d = new Date();
  const a = el('a');
  a.href = URL.createObjectURL(new Blob([txt + '\n'], {type: 'text/plain;charset=utf-8'}));
  a.download = 'ssh_tunnel-journal-' + d.getFullYear() + p(d.getMonth() + 1) + p(d.getDate()) + '-' + p(d.getHours()) + p(d.getMinutes()) + '.txt';
  document.body.appendChild(a);
  a.click();
  setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
});
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
function smOwnRule(r){
  if (String(r.ports || '').trim()) return false;
  // ~all — «новые устройства видны всем и видят всех» (ТЗ 28, access_newdev.go).
  if (r.from === '*' && r.to === SM_NEWALL) return true;
  return smOwn(r.from) && (smOwn(r.to) || r.to === '*' || r.to === SRV);
}
const SM_NEWALL = '~all';
// smLone — единственное ручное правило «все → все» без портов: так начинал
// редактор, и разрешает оно то же, что «все видят всех». Пока простой режим
// не заведён, оно — заготовка: первая галочка заменит его ролями.
function smLone(r){ return r.from === '*' && r.to === '*' && !String(r.ports || '').trim(); }
function smActive(){ return (ACCESS_RAW.groups || []).some(g => SM_ROLE_OF[g]); }
function smHand(){
  const hand = (ACCESS_RAW.rules || []).filter(r => !smOwnRule(r));
  if (!smActive() && hand.length === 1 && smLone(hand[0])) return [];
  return hand;
}
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
  // Правила выключены — все видят всех; простой режим ещё не заведён —
  // у каждого роль «Моё устройство» (как раньше давало «Включить простой режим»).
  const off = !ACCESS_RAW.enabled, fresh = !smActive();
  users.forEach(a => {
    roles[a] = smRoleOf(groupsOf[a]) || (fresh ? 'mine' : '');
    M[a] = {};
    users.concat([SRV]).forEach(b => { M[a][b] = off || smMatch(rules, groupsOf, a, b) >= 0; });
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
    // «Новое устройство — без доступа»: «→ *» открыл бы путь и к новым.
    if (cols.every(ok) && ND_MODE !== 'none'){ sig[a] = ['*']; return; }
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
    if (g === '*') return ND_MODE !== 'none' && cols.every(ok);
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
  // ~all («новое устройство видно всем») остаётся с обоими правилами: в нём
  // могут быть учётные записи, которых эта страница ещё не знает (заведены
  // только что в другом окне или на другом сервере). Известных страница из
  // него выводит — их галочки дальше пишутся ролями и личными группами.
  if ((ACCESS_RAW.groups || []).includes(SM_NEWALL)){
    keepOwn.add(SM_NEWALL);
    own.push({from: SM_NEWALL, to: '*', ports: ''}, {from: '*', to: SM_NEWALL, ports: ''});
  }

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
// Отдельного выключателя нет (ТЗ 28): доска — это и есть правила, первая
// правленая галочка включает их. Пока ничего не правили и правила выключены,
// доска показывает «все видят всех» — как и есть на деле.
async function smSaveNow(M, roles, hand){
  const enc = smEncode(M, roles, hand);
  if (enc.error){ smMsg(enc.error, 'err'); toast(enc.error, 'err'); return false; }
  const by = smClients(), clients = {};
  Object.keys(enc.clientGroups).forEach(u => { if (by[u]) clients[by[u].id] = enc.clientGroups[u]; });
  // Видели «все видят всех» — так и будет для новых устройств.
  if (!ND_MODE && !ACCESS_RAW.enabled) await ndSave('all', true);
  const r = await api('/api/access/simple', {enabled: true, groups: enc.groups, rules: enc.rules, clients});
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
  renderNewDev();
  if (!main || !CLIENTS_READY) return;

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
    return {on: !ACCESS_RAW.enabled || smMatch(ACCESS_RAW.rules || [], groupsOf, ua || '?', ub || '?') >= 0, lock: 'foreign', ua, ub};
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

  // Доска (компьютер): сетка, клетки делят ширину между собой.
  const brd = el('div', 'smb' + (cols.length + 1 > 8 ? ' many' : ''));
  brd.style.gridTemplateColumns = 'minmax(120px,max-content) repeat(' + (cols.length + 1) + ',minmax(34px,1fr))';
  brd.appendChild(el('div', 'hd corner', t('sm_who_whom')));
  cols.forEach(l => { const th = el('div', 'hd', l.name); th.title = l.name; brd.appendChild(th); });
  const ths = el('div', 'hd', srvName); ths.title = t('sm_servers_d'); brd.appendChild(ths);
  lines.forEach(a => {
    const td = el('div', 'nm');
    const b = el('b', '', a.name);
    b.title = a.name;
    td.appendChild(b);
    if (a.devices.length > 1) td.appendChild(el('span', 'badge dim', fill('sm_n_dev', {n: a.devices.length})));
    td.appendChild(a.client ? roleTag(w.roles[a.user]) : el('span', 'badge dim', t('sm_foreign_tag')));
    brd.appendChild(td);
    cols.forEach(x => brd.appendChild(mkCell(a, x)));
    brd.appendChild(mkCell(a, SRV));
  });
  const wrap = $('smTable');
  const keep = [wrap.scrollLeft, wrap.scrollTop];
  wrap.innerHTML = '';
  wrap.appendChild(brd);
  [wrap.scrollLeft, wrap.scrollTop] = keep;

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
  $('smTable').hidden = !lines.length;
  accFit();
  $('smCounts').textContent = fill('sm_counts', {g: (ACCESS_RAW.groups || []).length, gm: SM_MAX_GROUPS,
    r: (ACCESS_RAW.rules || []).length, rm: SM_MAX_RULES});
}

/* ---------- новое устройство (ТЗ 28) ---------- */
// ND_MODE: 'all' — видно всем и видит всех, 'none' — без доступа, пока не
// отметят, '' — не выбрано (сервер до ТЗ 28: новым ничего не добавляется);
// null — ещё не загружено. Общее для серверов сети.
let ND_MODE = null;
async function loadNewDev(){
  const {ok, data} = await api('/api/access/newdevice');
  if (ok) ND_MODE = data.mode || '';
  renderNewDev();
}
function renderNewDev(){
  const shown = ND_MODE || (ND_MODE === '' && !ACCESS_RAW.enabled ? 'all' : '');
  document.querySelectorAll('input[name="smNewDev"]').forEach(r => { r.checked = r.value === shown; });
  $('smNewDevNote').hidden = shown !== '' || ND_MODE === null;
  $('smNewDevNote').textContent = t('nd_legacy');
}
async function ndSave(mode, quiet){
  const {ok, data} = await api('/api/access/newdevice', {mode});
  if (!ok){ toast(errOf(data), 'err'); renderNewDev(); return false; }
  ND_MODE = mode;
  renderNewDev();
  if (!quiet) smMsg(t(mode === 'all' ? 'nd_saved_all' : 'nd_saved_none'), 'ok');
  return true;
}
document.querySelectorAll('input[name="smNewDev"]').forEach(r => r.addEventListener('change', async () => {
  if (!r.checked) return;
  if (!await ndSave(r.value)) return;
  // «Без доступа»: при выключенных правилах новое устройство увидело бы
  // всех, а правила «→ *» и «~all» открывают путь к новым. Записываем доску
  // как есть заново — у тех, кто уже есть, ничего не меняется.
  if (r.value === 'none' && CLIENTS_READY && (!ACCESS_RAW.enabled ||
      (ACCESS_RAW.rules || []).some(x => smOwnRule(x) && (x.to === '*' || x.to === SM_NEWALL)))){
    const w = smCur();
    await smSaveNow(w.M, w.roles);
    renderSimple();
  }
}));

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
  const w = smCur(), a = d.user;
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

/* ---------- высота по окну ---------- */
// Доска и журнал занимают окно до низа: длинное листается внутри них, а не
// вся страница (ТЗ 28).
function accFit(){
  const phone = matchMedia('(max-width:760px)').matches;
  const pad = phone ? 100 : 44;
  const fit = (node, min, prop) => {
    if (!node || !node.offsetParent) return;
    const top = node.getBoundingClientRect().top + scrollY;
    node.style[prop] = Math.max(min, Math.floor(innerHeight - top - pad)) + 'px';
  };
  fit($('smTable'), 240, 'maxHeight');
  fit($('journal'), 260, 'height');
}
addEventListener('resize', () => { if (TAB === 'Access') accFit(); });

/* ---------- события каркаса ---------- */
onPanel('lang', () => {
  // Справка — те же тексты, что в «?» (свои строки, не ввод людей).
  $('accHelp').innerHTML = t('hint_acc') + t('hint_acc_rules');
  $('smRolesHelp').innerHTML = t('sm_roles_help');
  $('ckFrom').dataset.sig = '';
  if (!$('viewMain').hidden) renderJournal();
});
// Группы нужны редактору правил — грузим при каждом открытии раздела.
onPanel('show:Access', () => { loadAccess(); requestAnimationFrame(accFit); });
onPanel('sub:Access', sub => {
  remesh();
  requestAnimationFrame(() => { accFit(); if (sub === 'journal') renderJournal(); });
});
onPanel('clients', first => { if (first) renderSimple(); });
onPanel('access', raw => {
  ACCESS_RAW = raw;
  loadNewDev(); // выбор общий — мог поменяться на другом сервере
  renderAccess();
  renderSimple();
});
onPanel('mesh', (m, role) => {
  if (role === 'main'){ fillCheckSelects(); renderSimple(); }
});
onPanel('meshLoaded', () => { if (!JR.timer && !JR.busy) loadJournal(); });
onPanel('journal', () => loadJournal());
onPanel('deviceCard', (box, d) => renderDevAccess(box, d));
