// Раздел «Устройства»: один список клиентов и устройств сети, карточка
// устройства в правой половине (ТЗ 27), добавление, права, настройки клиента.
/* ---------- устройства: один список на клиентов и устройства сети ---------- */
/* Клиент — учётная запись SSH на этом сервере (ключ, заморозка, удаление);
   устройство сети — то, что знает meshd (адрес .mesh, связь, имя). Обычно
   это одно и то же устройство: meshd помнит, под каким SSH-пользователем оно
   пришло (user), — по нему строки и склеиваются. Устройство с другого
   сервера сети — только «сеть», клиент, ещё не включавший сеть, — только
   «вход». */
let DEV_SEL = '';     // ключ выбранной строки: 'c:<id клиента>' или 'm:<id устройства сети>'
let CONFIG_FOR = '';  // id клиента, чьи настройки (с ключом) сейчас открыты
function devItems(){
  const byUser = {};
  CLIENTS.forEach(c => { if (c.username) byUser[c.username] = c; });
  const items = [], paired = new Set();
  meshDevices().forEach(d => {
    const c = d.user && byUser[d.user];
    if (c && !paired.has(c.id)){ paired.add(c.id); items.push({key: 'c:' + c.id, client: c, mesh: d}); }
    else items.push({key: 'm:' + d.id, client: null, mesh: d});
  });
  CLIENTS.forEach(c => { if (!paired.has(c.id)) items.push({key: 'c:' + c.id, client: c, mesh: null}); });
  return items;
}
function itemName(it){ return it.mesh ? it.mesh.display : it.client.name; }
function itemOnline(it){ return !!((it.mesh && it.mesh.online) || (it.client && it.client.sessions > 0)); }
function itemPlatform(it){ return it.mesh ? it.mesh.platform : it.client.deviceType; }
function itemSystem(it){ return it.mesh ? systemText(it.mesh) : t('device_' + it.client.deviceType); }

function renderDevList(){
  const all = devItems();
  const q = $('meshSearch').value.trim().toLowerCase();
  const list = all.filter(it => {
    const on = itemOnline(it);
    if (MESH_FILTER === 'on' && !on) return false;
    if (MESH_FILTER === 'off' && on) return false;
    if (!q) return true;
    const d = it.mesh || {}, c = it.client || {};
    return [d.display, d.name, d.host, d.ip, d.platform, d.os, d.hostname, d.via, d.server && serverName(d.server),
            c.name, c.username, c.deviceType]
      .some(v => v && String(v).toLowerCase().includes(q));
  }).sort((a, b) => (itemOnline(b) - itemOnline(a)) || itemName(a).localeCompare(itemName(b)));

  // Ничего не выбрано — справа фирменный фон (ТЗ 27), первое устройство
  // сами не открываем.

  const box = $('devRows');
  box.innerHTML = '';
  $('devRowsHead').hidden = !list.length;
  const empty = $('devRowsEmpty');
  empty.hidden = list.length > 0;
  empty.textContent = all.length ? t('mesh_no_match') : t('dev_none');
  list.forEach(it => {
    const row = el('button', 'drow' + (it.key === DEV_SEL ? ' on' : ''));
    row.type = 'button';
    row.dataset.key = it.key;
    row.appendChild(el('span', 'dot' + (itemOnline(it) ? ' on' : '')));
    const nm = el('span', 'nm');
    const n = el('div', 'n', itemName(it));
    if (it.client && it.client.state !== 'active') n.appendChild(stateBadge(it.client.state));
    nm.appendChild(n);
    const sub = el('div', 'h', it.mesh ? it.mesh.host + '.mesh'
      : t('device_' + it.client.deviceType) + ' · ' + onlineText(it.client));
    // «Ждёт одобрения» / «заморожено» — под именем: длинное имя его не спрячет.
    const hb = holdBadge(it.mesh);
    if (hb){ hb.style.margin = '0 6px 0 0'; sub.prepend(hb); }
    nm.appendChild(sub);
    row.appendChild(nm);
    row.appendChild(copyable(el('span', 'c-ip', (it.mesh && it.mesh.ip) || '—'), it.mesh && it.mesh.ip));
    row.appendChild(el('span', 'c-sys', itemSystem(it) || '—'));
    const ms = el('span', 'c-ms');
    if (it.mesh && it.mesh.online){
      ms.appendChild(qualBars(it.mesh.quality));
      ms.appendChild(document.createTextNode(fmtMs(it.mesh.rttMs)));
    } else ms.textContent = '—';
    row.appendChild(ms);
    row.addEventListener('click', () => selectDev(it.key, true));
    box.appendChild(row);
  });
}

function selectDev(key, open){
  if (DEV_SEL !== key){
    DEV_SEL = key;
    MESH_EDIT = null;
    // Открытые формы другого клиента закрываем, чтобы не спутать, чьи они.
    if (key !== 'c:' + ACCESS_CLIENT) $('clientAccessCard').hidden = true;
    if (CONFIG_FOR && key !== 'c:' + CONFIG_FOR) closeClientConfig();
  }
  $('addClientForm').hidden = true;
  document.querySelectorAll('#devRows .drow').forEach(r => r.classList.toggle('on', r.dataset.key === key));
  renderDetail();
  if (open) openDevPane(true);
}

/* ---------- правая половина: фирменный фон или карточка (ТЗ 27) ---------- */
$('devBrand').appendChild(brandBlock());
// openDevPane — показать карточку и формы вместо фона; top — с начала.
function openDevPane(top){
  $('devsBox').classList.add('open');
  if (top) $('devPane').scrollTop = 0;
}
// closeDevPane — назад к фону: ×, Esc, «Назад». Открытые формы закрываем —
// приватный ключ из «Настроек» на странице не остаётся.
function closeDevPane(){
  $('devsBox').classList.remove('open');
  $('addClientForm').hidden = true;
  $('clientAccessCard').hidden = true;
  if (CONFIG_FOR || !$('clientConfigCard').hidden) closeClientConfig();
}
// devPaneCheck — показывать справа нечего (устройство пропало, формы
// закрыты) — возвращаем фон, а не пустую карточку.
function devPaneCheck(){
  const any = !$('addClientForm').hidden || !$('clientConfigCard').hidden || !$('clientAccessCard').hidden ||
    devItems().some(i => i.key === DEV_SEL);
  if (!any) $('devsBox').classList.remove('open');
}
$('devBackBtn').addEventListener('click', closeDevPane);
$('devCloseBtn').addEventListener('click', closeDevPane);
// Esc — до остальных обработчиков (capture): если открыто окно поверх
// страницы (QR, «Вы уверены?») или идёт переименование, Esc закрывает их,
// а не карточку.
document.addEventListener('keydown', e => {
  if (e.key !== 'Escape' || TAB !== 'Devices' || !$('devsBox').classList.contains('open')) return;
  if (MESH_EDIT || document.querySelector('.overlay:not([hidden])') || $('brandTest')) return;
  closeDevPane();
}, true);

// fact — «подпись: значение» в сетке карточки.
function fact(box, k, v, node){
  const f = el('div');
  f.appendChild(el('div', 'k', k));
  const vv = el('div', 'v');
  if (node) vv.appendChild(node);
  vv.appendChild(document.createTextNode(v));
  f.appendChild(vv);
  box.appendChild(f);
}

function renderDetail(){
  if (MESH_EDIT) return; // идёт переименование — не смахиваем поле ввода
  // Открыт список цепочки выхода — опрос не должен его закрыть.
  if (document.activeElement && document.activeElement.closest && document.activeElement.closest('#devInfo .dexit select')) return;
  const box = $('devInfo');
  box.innerHTML = '';
  const it = devItems().find(i => i.key === DEV_SEL);
  $('devInfoCard').hidden = !it;
  if (!it){
    devPaneCheck();
    return;
  }
  const d = it.mesh, c = it.client, on = itemOnline(it);

  const top = el('div', 'dtop' + (on ? '' : ' offline'));
  const pic = el('div', 'pic');
  pic.appendChild(glyphSVG(glyphOf(itemPlatform(it))));
  top.appendChild(pic);
  const nm = el('div', 'nm');
  const nn = el('div', 'n', itemName(it));
  const hb = holdBadge(d);
  if (hb){ hb.style.marginLeft = '8px'; nn.appendChild(hb); }
  nm.appendChild(nn);
  const hh = el('div', 'h');
  hh.appendChild(d ? copyable(el('span', '', d.host + '.mesh'), d.host + '.mesh') : document.createTextNode(c.username || ''));
  nm.appendChild(hh);
  top.appendChild(nm);
  if (d){
    const pencil = el('button', 'icon-btn');
    pencil.title = t('dev_rename');
    pencil.innerHTML = '<svg viewBox="0 0 24 24"><path d="M4 20h4L19 9l-4-4L4 16v4Z"/><path d="m13.5 6.5 4 4"/></svg>';
    pencil.addEventListener('click', () => startRename(nm, d));
    top.appendChild(pencil);
  }
  top.appendChild(el('span', 'dot' + (on ? ' on' : '')));
  box.appendChild(top);

  // --- сеть устройств ---
  if (d){
    const part = el('div', 'dpart');
    part.appendChild(el('h2', '', t('dev_part_mesh')));
    const idrow = el('div', 'idrow');
    idrow.appendChild(copyable(el('span', 'ip', d.ip || '—'), d.ip));
    const sys = systemText(d);
    if (sys) idrow.appendChild(el('span', 'sys', sys));
    part.appendChild(idrow);
    const facts = el('div', 'facts');
    if (d.online){
      fact(facts, t('dev_latency'), fmtMs(d.rttMs) + (d.jitterMs ? ' ±' + d.jitterMs : ''), qualBars(d.quality));
      fact(facts, t('dev_loss'), (d.lossPct || 0) + '%');
    }
    const modeText = d.mode ? t('mode_' + d.mode) : '—';
    fact(facts, t('dev_mode'), modeText.startsWith('mode_') ? d.mode : modeText);
    if (d.nat) fact(facts, t('dev_nat'), natLabel(d.nat));
    fact(facts, t('dev_conns'), connText(d));
    if (d.online && d.direct && d.direct.length) fact(facts, t('dev_direct'), directText(d));
    fact(facts, t('dev_traffic'), '↑' + fmtBytes(d.bytesUp) + ' ↓' + fmtBytes(d.bytesDown));
    fact(facts, d.online ? t('dev_since') : t('dev_last'), d.online ? fmtAgo(d.connectedAt) : fmtClock(d.lastSeen));
    if (d.app) fact(facts, t('dev_app'), d.app);
    fact(facts, t('dev_sessions'), String(d.sessions || 0));
    part.appendChild(facts);
    renderDevConns(part, d);
    renderDevHold(part, d);
    const acts = el('div', 'dacts');
    if (d.alias){
      const reset = el('button', 'btn ghost small', t('dev_reset_name').replace('{n}', d.name));
      reset.addEventListener('click', () => renameDevice(d, ''));
      acts.appendChild(reset);
    }
    const forget = el('button', 'btn ghost small', t('dev_forget'));
    forget.addEventListener('click', async () => {
      const yes = await appConfirm(t('confirm_forget').replace('{n}', d.display), t('confirm_forget_yes'));
      if (!yes) return;
      const {ok, data} = await api('/api/mesh/forget', {net: MESH_NET, device: d.id});
      if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
      toast(t('forgotten'));
      loadMesh();
    });
    acts.appendChild(forget);
    acts.appendChild(freezeButton(d));
    part.appendChild(acts);
    renderDevExit(part, d);
    box.appendChild(part);
  } else if (MESH && MESH.effectiveRole === 'main'){
    const part = el('div', 'dpart');
    part.appendChild(el('h2', '', t('dev_part_mesh')));
    part.appendChild(el('p', 'note', t('dev_no_mesh')));
    box.appendChild(part);
  }

  // --- доступ: роль и «куда может ходить» ---
  emitPanel('deviceCard', box, d); // «Куда может ходить» — раздел «Доступ»

  // --- вход на сервер (клиент SSH) ---
  const part = el('div', 'dpart');
  const h = el('h2');
  h.appendChild(el('span', '', t('dev_part_client')));
  if (c) h.appendChild(stateBadge(c.state));
  part.appendChild(h);
  if (!c){
    part.appendChild(el('p', 'note', t('dev_no_client')));
    box.appendChild(part);
    return;
  }
  const facts = el('div', 'facts');
  fact(facts, t('client_name_label'), c.name);
  if (c.username) fact(facts, t('user_label'), c.username);
  fact(facts, t('dev_type'), t('device_' + c.deviceType));
  fact(facts, t('dev_created'), fmtDate(c.createdAt));
  fact(facts, t('dev_traffic'), '↓' + fmtBytes(c.rxBytes) + ' ↑' + fmtBytes(c.txBytes));
  fact(facts, t('dev_link'), onlineText(c));
  if ((c.groups && c.groups.length) || c.canCreate){
    fact(facts, t('acc_groups'), ((c.groups || []).join(', ') || '—') + (c.canCreate ? ' · ' + t('acc_creator_short') : ''));
  }
  part.appendChild(facts);
  const acts = el('div', 'dacts');
  const btn = (cls, key, fn) => {
    const b = el('button', 'btn small ' + cls, t(key));
    b.addEventListener('click', fn);
    acts.appendChild(b);
  };
  if (c.state === 'frozen') btn('ghost', 'unfreeze_client', () => clientAction('unfreeze', c.id));
  else btn('ghost', 'freeze_client', () => clientAction('freeze', c.id));
  btn('ghost', 'disconnect_client', () => clientAction('disconnect', c.id));
  btn('ghost', 'show_settings', () => showClientConfig(c.id));
  btn('ghost', 'acc_client_btn', () => openClientAccess(c.id));
  btn('danger', 'delete_client', () => deleteClient(c.id));
  part.appendChild(acts);
  box.appendChild(part);
}

// renderDevConns — связь с каждым сервером сети: на связи (с задержкой) или
// нет и почему (ТЗ 24, п. 4 и 13).
function renderDevConns(part, d){
  const conns = devConns(d), miss = d.missing || [];
  if (!miss.length && !conns.some(c => !c.online && c.stale)) return;
  const box = el('div', 'dconns');
  // На связи через другой сервер (ТЗ 31): никаких «X — не на связи» рядом с
  // зелёной точкой — только тихая пометка, к кому напрямую не подключено.
  if (d.online){
    const names = [...new Set(conns.filter(c => !c.online).map(c => c.server).concat(miss.map(m => m.server)))]
      .map(serverName).filter(n => n !== '—');
    if (names.length) box.appendChild(el('p', 'note', fill('dev_not_direct', {s: names.join(', ')})));
  }
  // С кем на связи — уже в «На связи через»; здесь — только без связи:
  // старая картина сервера и почему нет связи с остальными.
  if (!d.online) conns.filter(c => !c.online).forEach(c => {
    const r = el('div', 'dconn');
    r.appendChild(el('span', 'dot' + (c.online ? ' on' : '')));
    const txt = serverName(c.server) + ' — ' + (c.online ? t('sec_connected') + (c.rttMs ? ' · ' + fmtMs(c.rttMs) : '') : t('dev_conns_none')) +
      (c.stale ? ' · ' + fill('dev_conn_stale', {t: ageText(c.age || 0)}) : '');
    r.appendChild(el('span', '', txt));
    box.appendChild(r);
  });
  if (miss.length){
    box.appendChild(el('div', 'k', t(d.online ? 'dev_why_head_on' : 'dev_why_head')));
    miss.forEach(m => {
      const r = el('div', 'dconn');
      r.appendChild(el('span', 'dot'));
      r.appendChild(el('span', '', whyText(m)));
      box.appendChild(r);
    });
    if (d.online && !d.here && miss.some(m => m.server === 'main')) box.appendChild(el('p', 'note', t('why_here')));
  }
  part.appendChild(box);
}

async function clientAction(action, id){
  showMsg($('devicesMsg'), '', '');
  const {ok, data} = await api('/api/clients/' + action, {id});
  if (!ok){ showMsg($('devicesMsg'), errText(data.error, data), 'err'); return; }
  loadClients();
}


// Права одного клиента — отдельной карточкой: список устройств
// перерисовывается раз в 15 секунд и смахнул бы открытую форму.
let ACCESS_CLIENT = '';
function openClientAccess(id){
  const c = CLIENTS.find(v => v.id === id);
  if (!c) return;
  ACCESS_CLIENT = id;
  $('clientAccessTitle').textContent = t('acc_client_title') + ': ' + c.name;
  showMsg($('clientAccessMsg'), '', '');
  const box = $('clientAccessGroups');
  box.innerHTML = '';
  ACCESS.groups.forEach(g => {
    const l = document.createElement('label');
    l.className = 'g';
    const cb = document.createElement('input');
    cb.type = 'checkbox'; cb.value = g;
    cb.checked = (c.groups || []).includes(g);
    l.appendChild(cb);
    l.appendChild(document.createTextNode(groupLabel(g)));
    box.appendChild(l);
  });
  if (!ACCESS.groups.length) accNote(box, 'acc_no_groups');
  $('clientCanCreate').checked = !!c.canCreate;
  $('clientCreatorPass').value = '';
  $('clientAccessCard').hidden = false;
  openDevPane(false);
  $('clientAccessCard').scrollIntoView({behavior:'smooth', block:'start'});
}
$('closeClientAccessBtn').addEventListener('click', () => { $('clientAccessCard').hidden = true; devPaneCheck(); });
$('saveClientAccessBtn').addEventListener('click', async () => {
  const msg = $('clientAccessMsg');
  showMsg(msg, '', '');
  const groups = [...$('clientAccessGroups').querySelectorAll('input:checked')].map(cb => cb.value);
  let r = await api('/api/clients/groups', {id: ACCESS_CLIENT, groups});
  if (!r.ok){ showMsg(msg, errText(r.data.error, r.data), 'err'); return; }
  r = await api('/api/clients/creator', {id: ACCESS_CLIENT, canCreate: $('clientCanCreate').checked,
    password: $('clientCreatorPass').value});
  if (!r.ok){ showMsg(msg, errText(r.data.error, r.data), 'err'); return; }
  $('clientCreatorPass').value = '';
  showMsg(msg, t('acc_saved'), 'ok');
  loadClients();
});

async function deleteClient(id){
  if (!await appConfirm(t('confirm_delete_client'))) return;
  showMsg($('devicesMsg'), '', '');
  const {ok, data} = await api('/api/clients/delete', {id});
  if (!ok){ showMsg($('devicesMsg'), errText(data.error, data), 'err'); return; }
  if (DEV_SEL === 'c:' + id) DEV_SEL = '';
  if (ACCESS_CLIENT === id) $('clientAccessCard').hidden = true;
  if (CONFIG_FOR === id) closeClientConfig();
  devPaneCheck();
  loadClients();
  emitPanel('refresh');
}

$('addClientBtn').addEventListener('click', () => {
  showMsg($('addClientMsg'), '', '');
  $('newClientName').value = '';
  $('newClientType').value = 'windows';
  $('addClientForm').hidden = false;
  openDevPane(false);
  $('addClientForm').scrollIntoView({behavior: 'smooth', block: 'start'});
  $('newClientName').focus();
});
$('cancelClientBtn').addEventListener('click', () => { $('addClientForm').hidden = true; devPaneCheck(); });

$('createClientBtn').addEventListener('click', async () => {
  showMsg($('addClientMsg'), '', '');
  const name = $('newClientName').value.trim();
  const deviceType = $('newClientType').value;
  if (!name){ showMsg($('addClientMsg'), errText('bad_request'), 'err'); return; }
  $('createClientBtn').disabled = true;
  const {ok, data} = await api('/api/clients/create', {name, deviceType});
  $('createClientBtn').disabled = false;
  if (!ok){ showMsg($('addClientMsg'), errText(data.error, data), 'err'); return; }
  $('addClientForm').hidden = true;
  await loadClients();
  if (data.client) selectDev('c:' + data.client.id, true);
  // QR и ссылка приходят только в ответе на создание — второй раз их не
  // получить (п. 16), поэтому показываем то, что пришло, без запроса.
  if (!data.config){ showMsg($('devicesMsg'), errText(data.configError, data), 'err'); return; }
  CONFIG_FOR = data.client ? data.client.id : '';
  renderClientConfig(data.config, true);
});

/* ---------- настройки клиента: QR / буфер / файл ---------- */
let currentConfigFilename = 'ssh_tunnel.json';
let currentConfigJSON = '';
let currentConfigLink = '';

// showClientConfig — «Показать настройки» у существующего клиента: без QR и
// ссылки, их панель отдаёт только при создании.
async function showClientConfig(id){
  const {ok, data} = await api('/api/clients/config?id=' + encodeURIComponent(id));
  if (!ok){ showMsg($('devicesMsg'), errText(data.error, data), 'err'); return; }
  CONFIG_FOR = id;
  renderClientConfig(data.config, false);
}

function renderClientConfig(cfg, justCreated){
  showMsg($('clientConfigMsg'), '', '');
  $('clientConfigTitle').textContent = t(justCreated ? 'client_key_title' : 'client_settings_title');
  $('clientConfigNote').textContent = t(justCreated ? 'client_key_note' : 'client_settings_note');
  // QR со ссылкой — для любого типа устройства: телефон сканирует, на ПК
  // ссылку копируют кнопкой под кодом и вставляют в программу.
  currentConfigLink = cfg.link || '';
  $('clientConfigQRBox').hidden = !(cfg.qrPngBase64 && currentConfigLink);
  if (cfg.qrPngBase64){
    const src = 'data:image/png;base64,' + cfg.qrPngBase64;
    $('qrThumb').src = src;
    $('qrFullImg').src = src;
  }
  $('clientConfigHost').value = cfg.host;
  $('clientConfigPort').value = cfg.sshPort;
  $('clientConfigUser').value = cfg.user;
  $('clientConfigKey').value = cfg.privateKey;
  currentConfigFilename = 'ssh_tunnel-' + cfg.user + '.json';
  currentConfigJSON = cfg.json;
  $('clientConfigCard').hidden = false;
  openDevPane(false);
  $('clientConfigCard').scrollIntoView({behavior: 'smooth', block: 'start'});
}

$('qrThumb').addEventListener('click', () => { $('qrOverlay').hidden = false; });
$('qrClose').addEventListener('click', () => { $('qrOverlay').hidden = true; });
$('qrOverlay').addEventListener('click', e => {
  if (e.target === $('qrOverlay')) $('qrOverlay').hidden = true;
});
document.addEventListener('keydown', e => {
  if (e.key !== 'Escape') return;
  $('qrOverlay').hidden = true;
  if (!$('confirmOverlay').hidden) closeConfirm(false);
});

$('copyClientConfigBtn').addEventListener('click', async () => {
  if (await copyText(currentConfigJSON)) {
    showMsg($('clientConfigMsg'), t('copied'), 'ok');
  } else {
    showMsg($('clientConfigMsg'), t('copy_failed'), 'err');
  }
});

$('copyClientLinkBtn').addEventListener('click', async () => {
  if (await copyText(currentConfigLink)) {
    showMsg($('clientConfigMsg'), t('copied'), 'ok');
  } else {
    showMsg($('clientConfigMsg'), t('copy_failed'), 'err');
  }
});

$('downloadClientConfigBtn').addEventListener('click', () => {
  const blob = new Blob([currentConfigJSON], {type: 'application/json'});
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url; a.download = currentConfigFilename;
  document.body.appendChild(a); a.click(); document.body.removeChild(a);
  URL.revokeObjectURL(url);
});

$('closeClientConfigBtn').addEventListener('click', () => { closeClientConfig(); devPaneCheck(); });
// closeClientConfig — убрать со страницы приватный ключ, ссылку и QR.
function closeClientConfig(){
  CONFIG_FOR = '';
  $('clientConfigKey').value = '';
  currentConfigJSON = '';
  currentConfigLink = '';
  $('qrThumb').removeAttribute('src'); $('qrFullImg').removeAttribute('src');
  $('clientConfigCard').hidden = true;
}

$('meshSearch').addEventListener('input', () => renderDevList());
document.querySelectorAll('#meshFilter button').forEach(b => b.addEventListener('click', () => {
  MESH_FILTER = b.dataset.f;
  document.querySelectorAll('#meshFilter button').forEach(x => x.classList.toggle('on', x === b));
  renderDevList();
}));
/* ---------- профиль устройства в панели (ТЗ 16) ----------
   Одобрение новых устройств и заморозка (meshd 5+, у каждого сервера
   свои), цепочка выхода, которую панель советует устройству (оно может её
   поменять — показываем значение устройства и что оно другое), и записи
   журнала об этом. */
// holdBadge — значок «ждёт одобрения» / «заморожено» у устройства сети.
function holdBadge(d){
  if (!d || !(d.pending || d.frozen)) return null;
  return el('span', 'badge warn', t(d.frozen ? 'hold_frozen' : 'hold_pending'));
}

// renderDevHold — что с устройством и кнопки «Одобрить» / «Отклонить».
function renderDevHold(part, d){
  if (!d.pending && !d.frozen) return;
  part.appendChild(el('div', 'msg warn', t(d.frozen ? 'hold_frozen_note' : 'hold_pending_note')));
  if (d.pending){
    const acts = el('div', 'dacts');
    const yes = el('button', 'btn small', t('dev_approve'));
    yes.addEventListener('click', () => devHoldCall('/api/mesh/approve', {net: MESH_NET, device: d.id}, 'approved'));
    const no = el('button', 'btn ghost small', t('dev_reject'));
    no.addEventListener('click', async () => {
      if (!await appConfirm(t('confirm_forget').replace('{n}', d.display), t('dev_reject'))) return;
      devHoldCall('/api/mesh/forget', {net: MESH_NET, device: d.id}, 'forgotten');
    });
    acts.appendChild(yes);
    acts.appendChild(no);
    part.appendChild(acts);
  }
  part.appendChild(el('p', 'note', t('hold_scope_note')));
}

// freezeButton — «Заморозить в сети» / «Разморозить» для ряда кнопок карточки.
function freezeButton(d){
  const b = el('button', 'btn ghost small', t(d.frozen ? 'dev_unfreeze' : 'dev_freeze'));
  b.addEventListener('click', async () => {
    if (!d.frozen && !await appConfirm(fill('confirm_freeze', {n: d.display}), t('dev_freeze'))) return;
    devHoldCall('/api/mesh/freeze', {net: MESH_NET, device: d.id, on: !d.frozen}, d.frozen ? 'unfrozen_ok' : 'frozen_ok');
  });
  return b;
}

async function devHoldCall(path, body, okKey){
  const {ok, data} = await api(path, body);
  if (!ok){ toast(meshErr(data), 'err'); return; }
  toast(t(okKey));
  loadMesh();
  refreshJournal();
}

/* Цепочка выхода. Сервер называется ключом «адрес:порт» (id профилей у
   каждого устройства свои). EX_DRAFT — что выбрано в полях, пока их не
   отправили: опрос состояния перерисовывает карточку и не должен сбросить
   выбор. */
let EX_DRAFT = null; // {dev, entry, via:[...]}

function exName(d, key){
  if (!key) return t('ex_deleted');
  const p = (d.profiles || []).find(x => x.k === key);
  return p && p.n ? p.n + ' (' + key + ')' : key;
}
function exPath(d, entry, via){
  if (!via || !via.length) return fill('ex_direct', {s: exName(d, entry)});
  return [entry].concat(via).map(k => exName(d, k)).join(' → ');
}
function sameVia(a, b){ return (a || []).join('|') === (b || []).join('|'); }

function renderDevExit(part, d){
  const box = el('div', 'dexit');
  box.appendChild(el('h3', '', t('ex_title')));
  part.appendChild(box);
  const profs = d.profiles || [];
  if (!profs.length){ box.appendChild(el('p', 'note', t('ex_no_profiles'))); return; }
  if (EX_DRAFT && EX_DRAFT.dev !== d.id) EX_DRAFT = null;
  const es = d.exitSet;
  const active = profs.find(p => p.a) || profs[0];
  const entry = (EX_DRAFT && EX_DRAFT.entry) || (es && profs.some(p => p.k === es.entry) ? es.entry : active.k);
  const prof = profs.find(p => p.k === entry) || active;

  // Что на устройстве сейчас и откуда это.
  box.appendChild(el('div', 'ex-now', fill('ex_now', {path: exPath(d, prof.k, prof.v)})));
  if (prof.t) box.appendChild(el('div', 'note', prof.b
    ? fill('ex_by_panel', {s: prof.b, t: fmtClock(prof.t)})
    : fill('ex_by_device', {t: fmtClock(prof.t)})));
  if (es && es.entry === prof.k){
    if ((prof.t || 0) < es.t) box.appendChild(el('div', 'msg warn', fill('ex_pending', {t: fmtClock(es.t)})));
    else if (prof.t > es.t || !sameVia(es.via, prof.v)) box.appendChild(el('div', 'msg warn', fill('ex_changed', {path: exPath(d, es.entry, es.via)})));
  }

  // Поля: вход и до двух серверов после него.
  const draft = EX_DRAFT || {dev: d.id, entry: prof.k, via: (prof.v || []).filter(Boolean)};
  const form = el('div', 'ex-row');
  const mk = (label, value, opts, onChange) => {
    const f = el('label', 'ex-f');
    f.appendChild(el('span', 'k', label));
    const s = el('select');
    opts.forEach(([v, text]) => { const o = el('option', '', text); o.value = v; s.appendChild(o); });
    s.value = value;
    s.addEventListener('change', () => onChange(s.value));
    f.appendChild(s);
    form.appendChild(f);
  };
  const keep = () => { EX_DRAFT = draft; if (document.activeElement) document.activeElement.blur(); renderDetail(); };
  mk(t('ex_entry'), draft.entry, profs.map(p => [p.k, exName(d, p.k)]), v => {
    draft.entry = v;
    const p = profs.find(x => x.k === v);
    draft.via = ((p && p.v) || []).filter(Boolean);
    keep();
  });
  for (let i = 0; i < 2; i++){
    if (i > 0 && !draft.via[0]) break;
    const opts = [['', t('ex_none')]].concat(profs.filter(p => p.k !== draft.entry &&
      !draft.via.some((k, j) => j !== i && k === p.k)).map(p => [p.k, exName(d, p.k)]));
    mk(t('ex_hop'), draft.via[i] || '', opts, v => {
      if (v) draft.via[i] = v; else draft.via = draft.via.slice(0, i);
      keep();
    });
  }
  box.appendChild(form);
  const send = el('button', 'btn small', t('ex_send'));
  send.addEventListener('click', async () => {
    send.disabled = true;
    const {ok, data} = await api('/api/mesh/exit', {net: MESH_NET, device: d.id, entry: draft.entry, via: draft.via.filter(Boolean)});
    send.disabled = false;
    if (!ok){ toast(meshErr(data), 'err'); return; }
    EX_DRAFT = null;
    toast(t('ex_sent'));
    loadMesh();
    refreshJournal();
  });
  const acts = el('div', 'dacts');
  acts.appendChild(send);
  box.appendChild(acts);
  box.appendChild(el('p', 'note', t('ex_hint')));
}

/* ---------- имя устройства, переход к устройству ---------- */
// focusDeviceNow — открыть устройство в «Устройствах» (со схемы, из «Сетей»:
// они зовут focusDevice из core.js).
function focusDeviceNow(id){
  MESH_FILTER = 'all';
  $('meshSearch').value = '';
  document.querySelectorAll('#meshFilter button').forEach(b => b.classList.toggle('on', b.dataset.f === 'all'));
  const it = devItems().find(i => i.mesh && i.mesh.id === id);
  if (!it) return;
  if (TAB !== 'Devices') showTab('Devices');
  renderDevList();
  selectDev(it.key, true);
  const row = document.querySelector('.drow[data-key="' + CSS.escape(it.key) + '"]');
  if (!row || !row.getClientRects().length) return;
  row.scrollIntoView({behavior: 'smooth', block: 'nearest'});
  row.classList.remove('flash');
  void row.offsetWidth;
  row.classList.add('flash');
}

// startRename — имя прямо в шапке карточки: Enter или уход с поля —
// сохранить, Esc — отменить.
function startRename(nm, d){
  MESH_EDIT = d.id;
  nm.innerHTML = '';
  const input = el('input');
  input.type = 'text';
  input.maxLength = 64;
  input.value = d.alias || d.display;
  nm.appendChild(input);
  nm.appendChild(el('div', 'h', d.host + '.mesh'));
  input.focus();
  input.select();
  let done = false;
  const finish = save => {
    if (done) return;
    done = true;
    MESH_EDIT = null;
    const v = input.value.trim();
    if (save && v && v !== d.display) renameDevice(d, v);
    else renderDetail();
  };
  input.addEventListener('keydown', e => {
    if (e.key === 'Enter') finish(true);
    if (e.key === 'Escape') finish(false);
  });
  input.addEventListener('blur', () => finish(true));
}

async function renameDevice(d, alias){
  const {ok, data} = await api('/api/mesh/rename', {net: MESH_NET, device: d.id, alias});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); loadMesh(); return; }
  toast(t('renamed'));
  loadMesh();
}

/* ---------- события каркаса ---------- */
// Группы нужны форме прав клиента — грузим при каждом открытии раздела.
onPanel('show:Devices', () => loadAccess());
onPanel('clients', () => { renderDevList(); renderDetail(); });
onPanel('access', () => renderDetail());
onPanel('detail', () => renderDetail());
onPanel('focusDevice', id => focusDeviceNow(id));
onPanel('mesh', () => {
  renderDevList();
  renderDetail();
});
