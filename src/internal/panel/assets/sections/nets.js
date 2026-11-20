// Раздел «Сети»: сети устройств, серверы сети и прогноз прямых соединений —
// одной страницей (ТЗ 28). Раскладка — ТЗ 30 (nets.html, nets.css): слева
// серверы сети и сети, справа прогноз и сам сервер в сети.

// Старые ссылки #nets/servers и #nets/links (раньше — вкладки) ведут к
// своему блоку. Каркас переписывает адрес на #nets, поэтому хвост берём
// раньше него: при загрузке и при смене адреса (перехват — первым).
let NETS_JUMP = netsJumpOf(location.hash);
function netsJumpOf(h){
  const m = /^#nets\/(nets|servers|links)$/.exec(h || '');
  return m ? m[1] : '';
}
// Каркас к этому времени уже открыл раздел и переписал адрес — хвост берём
// из самого события.
addEventListener('hashchange', e => {
  const to = netsJumpOf(new URL(e.newURL).hash);
  if (to){ NETS_JUMP = to; netsJump(); }
});
function netsJump(){
  const to = NETS_JUMP;
  if (!to || $('tabMesh').hidden) return;
  const target = {nets: $('netsPage'), servers: $('meshServersCard'), links: $('meshForecastCard')}[to];
  // Карточка ещё не показана (картина сети не пришла) — прыгнем, когда придёт.
  if (!target || target.hidden) return;
  NETS_JUMP = '';
  setTimeout(() => target.scrollIntoView({block: 'start'}), 0);
}
/* ---------- проверка NAT и прогноз прямых соединений ---------- */
// natForecast — сможет ли пара устройств соединиться напрямую.
function natForecast(a, b){
  if (!a.udp || !b.udp) return {lvl: 'no', cls: 'err', why: t('fcw_udp')};
  if (a.ipv6 && b.ipv6) return {lvl: 'yes', cls: 'ok', why: t('fcw_ipv6')};
  const ea = a.mapping === 'independent', eb = b.mapping === 'independent';
  if (ea && eb) return {lvl: 'likely', cls: 'ok', why: t('fcw_both_easy')};
  if (ea || eb){
    const hard = ea ? b : a;
    return {lvl: 'maybe', cls: 'warn', why: hard.portDelta ? t('fcw_one_hard_seq') : t('fcw_one_hard')};
  }
  return {lvl: 'unlikely', cls: 'err', why: t('fcw_both_hard')};
}
function renderForecast(devices){
  const box = $('meshForecast');
  box.innerHTML = '';
  const tested = devices.filter(d => d.nat);
  // Сначала — сами устройства: какой у кого NAT и внешний адрес.
  tested.forEach(d => {
    const row = el('div', 'fc-row');
    const p = el('span', 'p');
    p.appendChild(el('b', '', d.display));
    p.appendChild(el('div', 'why', natLabel(d.nat) + (d.nat.publicIp ? ' · ' + d.nat.publicIp : '')));
    row.appendChild(p);
    box.appendChild(row);
  });
  const pairs = [];
  for (let i = 0; i < tested.length; i++){
    for (let j = i + 1; j < tested.length; j++){
      pairs.push([tested[i], tested[j], natForecast(tested[i].nat, tested[j].nat)]);
    }
  }
  const order = {yes: 0, likely: 1, maybe: 2, unlikely: 3, no: 4};
  pairs.sort((x, y) => order[x[2].lvl] - order[y[2].lvl]);
  if (pairs.length){
    box.appendChild(el('h2', '', t('fc_pairs'))).style.marginTop = '14px';
  }
  pairs.slice(0, 40).forEach(([a, b, f]) => {
    const row = el('div', 'fc-row');
    const p = el('span', 'p');
    p.appendChild(el('span', '', a.display + '  ↔  ' + b.display));
    p.appendChild(el('div', 'why', f.why));
    row.appendChild(p);
    row.appendChild(el('span', 'badge ' + f.cls, t('fc_' + f.lvl)));
    box.appendChild(row);
  });
  if (tested.length < 2){
    const e = el('div', 'empty', t('fc_empty'));
    e.style.padding = '14px 6px';
    box.appendChild(e);
  }
}

/* ---------- серверы сети (ТЗ 22) ---------- */
// Все серверы равны: этот первым, потом остальные. «Добавить сервер» даёт
// одноразовый код, «Войти в сеть» — пока этот сервер ни с кем не связан.
function srvNet(){ return (MESH && MESH.serverNet) || {servers: []}; }
function renderMeshServers(){
  const sn = srvNet();
  const others = (sn.servers || []).filter(s => !s.self && !s.waiting);
  $('srvJoinBtn').hidden = !!sn.legacy || others.length > 0;
  $('srvAddBtn').hidden = !!sn.legacy;
  $('srvRemoved').hidden = !sn.removed || !!sn.net;
  $('srvRemoved').textContent = sn.removed ? t('srv_removed_note').replace('{n}', sn.removed) : '';
  const bad = (sn.servers || []).filter(s => s.hostBad && !s.self);
  const notes = [];
  if (sn.sshHostInside){
    const me = (sn.servers || []).find(s => s.self);
    notes.push(fill('srv_sshhost_inside', {h: sn.sshHostInside, x: (me && me.host) || '—'}));
  }
  bad.forEach(s => notes.push(fill('srv_hostbad_note', {s: s.name || s.host, h: s.host})));
  $('srvHostNote').hidden = !notes.length;
  $('srvHostNote').textContent = notes.join('\n\n');
  const box = $('meshServers');
  const sig = LANG + '|' + JSON.stringify(sn.servers) + '|' + (sn.net || '') + '|' +
    ((MESH && MESH.servers) || []).map(s => s.id + (s.stale ? ':' + Math.floor((s.age || 0) / 60) : '')).join(',');
  if (box.dataset.sig === sig || SRV_EDIT) return;
  box.dataset.sig = sig;
  box.innerHTML = '';
  (sn.servers || []).forEach(s => {
    const row = el('div', 'srv-row');
    row.appendChild(el('span', 'dot' + (s.online ? ' on' : '')));
    const nm = el('div', 'nm');
    const n = el('div', 'n', s.name || s.host);
    if (s.self) n.appendChild(el('span', 'badge dim', t('srv_main'))).style.marginLeft = '8px';
    if (s.waiting) n.appendChild(el('span', 'badge warn', t('srv_waiting_move'))).style.marginLeft = '8px';
    if (s.hostBad) n.appendChild(el('span', 'badge warn', t('srv_hostbad'))).style.marginLeft = '8px';
    const pencil = el('button', 'icon-btn');
    pencil.type = 'button';
    pencil.title = t('srv_rename');
    pencil.innerHTML = '<svg viewBox="0 0 24 24"><path d="M4 20h4L19 9l-4-4L4 16v4Z"/><path d="m13.5 6.5 4 4"/></svg>';
    pencil.addEventListener('click', () => startSrvRename(s, n));
    n.appendChild(pencil);
    nm.appendChild(n);
    const sub = [(s.host || '') + (s.port && s.port !== 22 ? ':' + s.port : '')];
    if (!s.self && !s.waiting){
      sub.push(s.online ? (s.lastSync ? t('srv_synced').replace('{t}', fmtAgo(s.lastSync)) : '') : (s.error || t('srv_offline')));
      const ms = srvById(s.id);
      if (ms && ms.stale) sub.push(fill('srv_stale', {t: ageText(ms.age || 0)}));
      if (ms && ms.noView && s.online) sub.push(t('srv_noview'));
      if (s.online && s.rttMs) sub.push(fmtMs(s.rttMs));
    }
    nm.appendChild(el('div', 'h', sub.filter(Boolean).join(' · ')));
    row.appendChild(nm);
    if ((sn.net && (!s.self || others.length)) || s.waiting){
      const del = el('button', 'btn ghost small', t(s.self ? 'srv_leave' : 'srv_remove'));
      del.addEventListener('click', async () => {
        const yes = await appConfirm(t(s.self ? 'srv_leave_confirm' : 'srv_remove_confirm').replace('{n}', s.name || s.host), t(s.self ? 'srv_leave' : 'srv_remove'));
        if (!yes) return;
        const {ok, data} = await api('/api/mesh/servers/remove', {id: s.id});
        if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
        loadMesh();
      });
      row.appendChild(del);
    }
    box.appendChild(row);
  });
}
// Переименование сервера сети (ТЗ 24): имя общее — побеждает последнее.
let SRV_EDIT = null;
function startSrvRename(s, holder){
  SRV_EDIT = s.id;
  holder.innerHTML = '';
  const inp = el('input', 'net-name');
  inp.type = 'text';
  inp.maxLength = 48;
  inp.value = s.name || '';
  inp.placeholder = t('srv_name_ph');
  holder.appendChild(inp);
  inp.focus();
  inp.select();
  let done = false;
  const finish = async save => {
    if (done) return;
    done = true;
    const v = inp.value.trim();
    if (save && v && v !== s.name && await appConfirm(fill('srv_rename_confirm', {a: s.name || s.host, b: v}), t('srv_rename_yes'))){
      const {ok, data} = await api('/api/mesh/servers/rename', {id: s.self ? 'main' : s.id, name: v});
      if (!ok) toast(data.error || t('err_internal'), 'err'); else toast(t('srv_renamed'));
    }
    SRV_EDIT = null;
    $('meshServers').dataset.sig = '';
    loadMesh();
  };
  inp.addEventListener('keydown', e => {
    if (e.key === 'Enter'){ e.preventDefault(); finish(true); }
    if (e.key === 'Escape'){ e.preventDefault(); finish(false); }
  });
  inp.addEventListener('blur', () => finish(true));
}

$('srvAddBtn').addEventListener('click', async () => {
  const btn = $('srvAddBtn');
  btn.disabled = true;
  const {ok, data} = await api('/api/mesh/servers/invite', {});
  btn.disabled = false;
  if (!ok){ showMsg($('srvMsg'), data.error || t('err_internal'), 'err'); return; }
  showMsg($('srvMsg'), '', '');
  SRV_CODE = data.code;
  $('srvCode').textContent = data.code;
  $('srvCodeExp').textContent = t('srv_code_exp').replace('{t}', fmtClock(data.expires));
  $('srvJoinBox').hidden = true;
  $('srvCodeBox').hidden = false;
  loadMesh();
});
$('srvCodeCopy').addEventListener('click', async () => {
  toast(await copyText(SRV_CODE) ? t('copied') : t('copy_failed'), 'ok');
});
$('srvCodeClose').addEventListener('click', () => {
  $('srvCodeBox').hidden = true;
  SRV_CODE = '';
  $('srvCode').textContent = '';
});
$('srvJoinBtn').addEventListener('click', () => {
  $('srvCodeBox').hidden = true;
  $('srvJoinBox').hidden = false;
  $('srvJoinConfirm').hidden = true;
  $('srvJoinCode').focus();
});
$('srvJoinCancel').addEventListener('click', () => {
  $('srvJoinBox').hidden = true;
  $('srvJoinCode').value = '';
  showMsg($('srvMsg'), '', '');
});
$('srvJoinCode').addEventListener('input', () => { $('srvJoinConfirm').hidden = true; });
$('srvJoinCheck').addEventListener('click', async () => {
  const btn = $('srvJoinCheck');
  btn.disabled = true;
  const {ok, data} = await api('/api/mesh/servers/preview', {code: $('srvJoinCode').value});
  btn.disabled = false;
  if (!ok){ showMsg($('srvMsg'), data.error || t('err_internal'), 'err'); return; }
  showMsg($('srvMsg'), '', '');
  const list = $('srvJoinList');
  list.innerHTML = '';
  const li = k => list.appendChild(el('li', '', k));
  li(t('srv_join_servers').replace('{s}', (data.servers || []).join(', ')));
  li(t('srv_join_rules').replace('{n}', data.rules).replace('{m}', data.netRules));
  if (data.holds) li(t('srv_join_holds').replace('{n}', data.holds));
  li(t('srv_join_add').replace('{n}', data.addAccounts));
  li(t('srv_join_give').replace('{n}', data.giveAccounts));
  $('srvJoinConfirm').hidden = false;
});
$('srvJoinGo').addEventListener('click', async () => {
  const btn = $('srvJoinGo');
  btn.disabled = true;
  const {ok, data} = await api('/api/mesh/servers/join', {code: $('srvJoinCode').value});
  btn.disabled = false;
  if (!ok){ showMsg($('srvMsg'), data.error || t('err_internal'), 'err'); return; }
  $('srvJoinBox').hidden = true;
  $('srvJoinCode').value = '';
  toast(t('srv_join_started'));
  loadMesh();
});

/* ---------- сам сервер как узел сети ---------- */
let SELF_DIRTY = false;
function renderSelfCard(m, role){
  const card = $('meshSelfCard');
  card.hidden = role !== 'main' && role !== 'secondary';
  if (card.hidden) return;
  let host = '', ip = '';
  if (role === 'main' && m.self){ host = m.self.host; ip = m.self.ip; }
  if (role === 'secondary' && m.link){ host = m.link.meshHost || ''; ip = m.link.meshIp || ''; }
  $('selfAddr').textContent = host ? host + '.mesh · ' + ip : '—';
  $('selfExample').textContent = host ? t('self_example').replace('{h}', host + '.mesh') : '';
  if (!SELF_DIRTY){
    $('selfEnabled').checked = !!m.serverAccess;
    $('selfPorts').value = m.serverPorts || '';
  }
  $('selfPorts').disabled = !$('selfEnabled').checked;
}
['selfEnabled', 'selfPorts'].forEach(id => $(id).addEventListener('input', () => {
  SELF_DIRTY = true;
  $('selfPorts').disabled = !$('selfEnabled').checked;
}));
$('selfSaveBtn').addEventListener('click', async () => {
  const btn = $('selfSaveBtn');
  btn.disabled = true;
  const {ok, data} = await api('/api/mesh/serveraccess', {enabled: $('selfEnabled').checked, ports: $('selfPorts').value});
  btn.disabled = false;
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  SELF_DIRTY = false;
  toast(t('saved'));
  loadMesh();
});

/* ---------- раздел «Сети»: имя, удаление ---------- */
const NET_TTL = 7 * 86400; // пустая сеть удаляется сама через 7 дней (meshd)
let NET_EDIT = null;       // id сети, которую сейчас переименовывают

function renderNets(){
  const box = $('meshNets');
  renderOldHints();
  // Идёт ввод имени — не смахиваем поле очередным опросом.
  if (NET_EDIT && box.querySelector('input.net-name')) return;
  box.innerHTML = '';
  const nets = mainNets();
  if (nets.length) meshDevices();
  nets.forEach((n, i) => {
    const devs = n.devices || [];
    const card = el('div', 'card net-card');
    const top = el('div', 'net-top');
    const h = el('h1', 'net-title');
    const name = el('span', 'net-name' + (n.name ? '' : ' unnamed'), netLabel(n, i));
    name.title = t('net_rename');
    name.addEventListener('click', () => startNetRename(n, i, h));
    h.appendChild(name);
    const pencil = el('button', 'icon-btn');
    pencil.type = 'button';
    pencil.title = t('net_rename');
    pencil.innerHTML = '<svg viewBox="0 0 24 24"><path d="M4 20h4L19 9l-4-4L4 16v4Z"/><path d="m13.5 6.5 4 4"/></svg>';
    pencil.addEventListener('click', () => startNetRename(n, i, h));
    h.appendChild(pencil);
    top.appendChild(h);
    if (n.id === MESH_NET && nets.length > 1) top.appendChild(el('span', 'badge ok', t('net_shown')));
    if (!devs.length) top.appendChild(el('span', 'badge warn', t('net_empty')));
    card.appendChild(top);

    const kv = (k, v) => {
      const r = el('div', 'kv');
      r.appendChild(el('span', 'k', k));
      r.appendChild(el('span', 'v', v));
      card.appendChild(r);
    };
    kv(t('net_devices'), devs.length ? devs.filter(d => d.online).length + ' ' + t('net_of') + ' ' + devs.length + ' ' + t('net_online_short') : '0');
    if (n.created) kv(t('net_created'), fmtDay(n.created));
    if (!devs.length && n.emptySince) kv(t('net_autoremove'), fmtDay(n.emptySince + NET_TTL));
    kv(t('net_id'), n.id.length > 16 ? n.id.slice(0, 16) + '…' : n.id);
    if (!devs.length) card.appendChild(el('p', 'note net-note', t(n.emptySince ? 'net_empty_note' : 'net_empty_note_old')));
    renderNetMembers(card, n);
    renderNetApproval(card, n);

    const acts = el('div', 'dacts');
    if (nets.length > 1 && n.id !== MESH_NET){
      const b = el('button', 'btn ghost small', t('net_show'));
      b.addEventListener('click', () => pickNet(n.id));
      acts.appendChild(b);
    }
    const del = el('button', 'btn danger small', t('net_delete'));
    del.addEventListener('click', () => deleteNet(n, i));
    acts.appendChild(del);
    card.appendChild(acts);
    box.appendChild(card);
  });
}

// renderNetMembers — устройства сети, серверы, через которые она работает,
// и как добавить в неё устройство (ТЗ 24, п. 7).
function renderNetMembers(card, n){
  const devs = (n.devices || []).slice().sort((a, b) => (b.online - a.online) || String(a.display).localeCompare(b.display));
  const sec = el('div', 'net-sec devs');
  sec.appendChild(el('div', 'k', t('net_devs_head')));
  if (!devs.length) sec.appendChild(el('p', 'note', t('net_no_devs')));
  devs.forEach(d => {
    const r = el('div', 'net-dev');
    r.appendChild(el('span', 'dot' + (d.online ? ' on' : '')));
    const nm = el('span', 'nm', d.display);
    r.appendChild(nm);
    r.appendChild(el('span', 'h', d.host + '.mesh'));
    r.appendChild(el('span', 'v', connText(d)));
    r.addEventListener('click', () => focusDevice(d.id));
    sec.appendChild(r);
  });
  card.appendChild(sec);
  const ids = new Set();
  devs.forEach(d => devConns(d).forEach(c => ids.add(c.server)));
  if (n.devices && n.devices.some(d => d.here) || !ids.size) ids.add('main');
  const srv = el('div', 'net-sec');
  srv.appendChild(el('div', 'k', t('net_srvs_head')));
  srv.appendChild(el('div', 'v', [...ids].map(id => {
    const s = srvById(id);
    return serverName(id) + (s && !s.online ? ' (' + t('srv_offline') + ')' : '');
  }).join(', ')));
  card.appendChild(srv);
  const how = el('details', 'net-add');
  how.appendChild(el('summary', '', t('net_add_head')));
  const ol = el('ol');
  ['net_add_1', 'net_add_2', 'net_add_3'].forEach(k => ol.appendChild(el('li', '', t(k))));
  how.appendChild(ol);
  how.appendChild(el('p', 'note', t('net_add_warn')));
  card.appendChild(how);
}

function startNetRename(n, i, h){
  NET_EDIT = n.id;
  h.innerHTML = '';
  const inp = el('input', 'net-name');
  inp.type = 'text';
  inp.maxLength = 64;
  inp.value = n.name || '';
  inp.placeholder = t('net_n').replace('{n}', i + 1) + ' — ' + t('net_name_ph');
  h.appendChild(inp);
  inp.focus();
  inp.select();
  let done = false;
  const finish = save => {
    if (done) return;
    done = true;
    NET_EDIT = null;
    const v = inp.value.trim();
    if (save && v !== (n.name || '')) saveNetName(n, v); else renderNets();
  };
  inp.addEventListener('keydown', e => {
    if (e.key === 'Enter'){ e.preventDefault(); finish(true); }
    if (e.key === 'Escape'){ e.preventDefault(); finish(false); }
  });
  inp.addEventListener('blur', () => finish(true));
}

async function saveNetName(n, name){
  const {ok, data} = await api('/api/mesh/net/rename', {net: n.id, name});
  if (!ok){ toast(meshErr(data), 'err'); renderNets(); return; }
  toast(name ? t('net_renamed') : t('net_name_cleared'));
  loadMesh();
  refreshJournal();
}

async function deleteNet(n, i){
  const devs = n.devices || [];
  const label = netLabel(n, i);
  const msg = devs.length
    ? fill('net_del_confirm', {name: label, n: devs.length, on: devs.filter(d => d.online).length})
    : fill('net_del_confirm_empty', {name: label});
  if (!await appConfirm(msg, t('net_delete'))) return;
  const {ok, data} = await api('/api/mesh/net/delete', {net: n.id});
  if (!ok){ toast(meshErr(data), 'err'); return; }
  toast(t('net_deleted'));
  if (MESH_NET === n.id) MESH_NET = null;
  loadMesh();
  refreshJournal();
}

// renderNetApproval — переключатель одобрения у сети в «Сетях».
function renderNetApproval(card, n){
  const devs = n.devices || [];
  const waiting = devs.filter(d => d.pending).length;
  if (waiting){
    const r = el('div', 'kv');
    r.appendChild(el('span', 'k', t('net_pending')));
    r.appendChild(el('span', 'v', String(waiting)));
    card.appendChild(r);
  }
  const lab = el('label', 'chk');
  lab.style.marginTop = '10px';
  const cb = el('input');
  cb.type = 'checkbox';
  cb.checked = !!n.approval;
  lab.appendChild(cb);
  const txt = el('span');
  txt.appendChild(el('div', '', t('net_approval')));
  txt.appendChild(el('div', 'note', t('net_approval_note')));
  lab.appendChild(txt);
  cb.addEventListener('change', async () => {
    const want = cb.checked;
    const {ok, data} = await api('/api/mesh/net/approval', {net: n.id, on: want});
    if (!ok){ cb.checked = !want; toast(meshErr(data), 'err'); return; }
    toast(t(want ? 'net_approval_on' : 'net_approval_off'));
    loadMesh();
    refreshJournal();
  });
  card.appendChild(lab);
}

/* ---------- события каркаса ---------- */

onPanel('mesh', (m, role) => {
  const isMain = role === 'main';
  $('meshForecastCard').hidden = !isMain;
  $('netsNone').hidden = isMain;
  $('netsNoneText').textContent = isMain ? '' : t(role === 'secondary' ? 'nets_secondary' : 'nets_off');
  renderSelfCard(m, role);
  renderNets();
  if (isMain) renderForecast(meshDevices());
  $('meshServersCard').hidden = false;
  renderMeshServers();
  netsJump();
});
onPanel('show:Mesh', () => netsJump());
