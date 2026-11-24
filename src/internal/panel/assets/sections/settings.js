// Раздел «Настройки»: сеть устройств на этом сервере, выход сервера, панель,
// вид (тема и фон), Книга (book.js).
/* ---------- настройки: автозапуск ---------- */
async function loadAutostart(){
  const {ok, data} = await api('/api/autostart');
  if (!ok) return;
  $('autostartRow').hidden = !data.supported;
  $('autostartNote').hidden = !data.supported;
  $('autostartUnsupported').hidden = !!data.supported;
  $('autostartCheck').checked = !!data.enabled;
}

/* ---------- выход сервера через другой сервер (ТЗ 10) ---------- */
let EGRESS_POLL = null, EGRESS_TOKEN = '';
function stopEgressPoll(){ if (EGRESS_POLL){ clearInterval(EGRESS_POLL); EGRESS_POLL = null; } }

async function loadEgress(){
  const {status, ok, data} = await api('/api/egress');
  if (status === 404){ $('egressCard').hidden = true; stopEgressPoll(); return; }
  if (!ok) return;
  $('egressCard').hidden = false;
  renderEgress(data);
  const live = data.phase === 'installing' || data.phase === 'applying' || data.phase === 'rollback';
  if (live && !EGRESS_POLL) EGRESS_POLL = setInterval(loadEgress, 1500);
  else if (!live) stopEgressPoll();
}

function renderEgress(d){
  const off = d.phase === 'off';
  $('egressOffForm').hidden = !off;
  $('egressOnView').hidden = off;
  if (off){
    $('egressMode').value = d.mode || 'block';
    showMsg($('egressMsg'), d.conflict || '', 'err');
    return;
  }
  $('egressPhase').textContent = t('egress_phase_' + d.phase) || d.phase;
  $('egressTargetName').textContent = d.target ? (d.target.name || d.target.host) + ' (' + d.target.host + ')' : '—';
  $('egressExitRow').hidden = d.phase !== 'on' || !d.exitIp;
  $('egressExitIp').textContent = d.exitIp || '—';
  $('egressRollbackRow').hidden = !d.rolledBackAt;
  if (d.rolledBackAt) $('egressRolledBack').textContent = fmtAgo(d.rolledBackAt);
  $('egressModeOn').value = d.mode || 'block';
  const busy = d.phase === 'installing' || d.phase === 'applying';
  $('egressModeOnRow').hidden = busy;
  $('egressApplyingBox').hidden = d.phase !== 'applying';
  if (d.phase === 'applying'){
    $('egressApplyingNote').textContent = t('egress_applying_note').replace('{s}', Math.max(0, d.remaining || 0));
    if (d.token) EGRESS_TOKEN = d.token;
  }
  const job = d.job;
  $('egressJobLog').hidden = !job || !(job.lines && job.lines.length);
  if (job && job.lines) $('egressJobLog').textContent = job.lines.join('\n');
  showMsg($('egressMsg2'), job && job.error ? job.error : '', 'err');
  $('egressOffBtn').disabled = !!(job && job.running);
}

$('egressApplyBtn').addEventListener('click', async () => {
  const btn = $('egressApplyBtn');
  btn.disabled = true;
  const {ok, data} = await api('/api/egress/apply', {link: $('egressLink').value, mode: $('egressMode').value});
  btn.disabled = false;
  if (!ok || data.error){ showMsg($('egressMsg'), data.error || t('err_internal'), 'err'); return; }
  $('egressLink').value = '';
  showMsg($('egressMsg'), '', '');
  loadEgress();
});
$('egressConfirmBtn').addEventListener('click', async () => {
  const btn = $('egressConfirmBtn');
  btn.disabled = true;
  const {ok, data} = await api('/api/egress/confirm', {token: EGRESS_TOKEN});
  btn.disabled = false;
  if (!ok || data.error){ showMsg($('egressMsg2'), data.error || t('err_internal'), 'err'); return; }
  loadEgress();
});
$('egressOffBtn').addEventListener('click', async () => {
  const yes = await appConfirm(t('egress_off_confirm'), t('egress_off'));
  if (!yes) return;
  const {ok, data} = await api('/api/egress/off', {});
  if (!ok || data.error){ showMsg($('egressMsg2'), data.error || t('err_internal'), 'err'); return; }
  loadEgress();
});
$('egressModeOn').addEventListener('change', async () => {
  const {ok, data} = await api('/api/egress/mode', {mode: $('egressModeOn').value});
  if (!ok || data.error) showMsg($('egressMsg2'), data.error || t('err_internal'), 'err');
  loadEgress();
});
/* ---------- проверка обновлений ---------- */
/* Ходит на GitHub сервер панели, а не браузер: панель может стоять там, куда
   у браузера администратора доступа нет, да и CORS чужого домена со страницы
   не пройдёт. Скачивать и подменять себя панель не станет — файл занят
   работающей службой (docs/UPDATE_SPEC.md). */
$('updateCheckBtn').addEventListener('click', async () => {
  const btn = $('updateCheckBtn');
  btn.disabled = true;
  showMsg($('updateMsg'), t('update_checking'), 'ok');
  $('updateHow').hidden = true;
  const {ok, data} = await api('/api/update');
  btn.disabled = false;
  if (!ok || data.error){
    showMsg($('updateMsg'),
      data.code === 'update_rate_limited' ? t('update_rate_limited') : t('update_failed'), 'err');
    return;
  }
  const r = data.result;
  if (r.newer){
    showMsg($('updateMsg'), t('update_available').replace('{v}', r.latest), 'ok');
    $('updateLink').href = r.url;
    UPDATE_TO = r.latest;
    $('updateInstallBtn').textContent = t('update_install').replace('{v}', r.latest);
    $('updateInstallBtn').hidden = false;
    $('updateHow').hidden = false;
    loadUpdateAll(false);
  } else if (r.ahead){
    showMsg($('updateMsg'), t('update_ahead').replace('{v}', r.latest), 'ok');
  } else if (r.unknown){
    showMsg($('updateMsg'), t('update_unknown').replace('{v}', r.latest), 'ok');
    $('updateLink').href = r.url;
    // Сборка не из релиза — подменять её релизом без спроса не предлагаем.
    $('updateInstallBtn').hidden = true;
    $('updateHow').hidden = false;
  } else {
    showMsg($('updateMsg'), t('update_latest'), 'ok');
  }
});

/* «Обновить до …»: панель сама скачивает, сверяет и ставит новую версию,
   потом перезапускается (selfupdate.go). Страница следит за шагами, а
   после перезапуска ждёт, пока панель ответит новой версией, и
   перезагружается — вход не слетает, сессия переезжает в новую копию. */
let UPDATE_TO = '';
$('updateInstallBtn').addEventListener('click', async () => {
  const yes = await appConfirm(t('update_confirm').replace('{v}', UPDATE_TO), t('update_confirm_yes'));
  if (!yes) return;
  const btn = $('updateInstallBtn');
  btn.disabled = true;
  $('updateCheckBtn').disabled = true;
  $('updateBar').hidden = false;
  $('updateStage').textContent = t('upd_stage_download');
  const fail = msg => {
    $('updateBar').hidden = true;
    $('updateStage').textContent = '';
    btn.disabled = false;
    $('updateCheckBtn').disabled = false;
    showMsg($('updateMsg'), t('update_install_failed').replace('{e}', msg), 'err');
  };
  const {ok, data} = await api('/api/update/install', {});
  if (!ok || data.error){ fail(data.error || t('err_internal')); return; }

  const sleep = ms => new Promise(r => setTimeout(r, ms));
  let stage = '';
  // Шаги до перезапуска.
  for (let i = 0; i < 400 && stage !== 'restart'; i++){
    await sleep(1000);
    const r = await api('/api/update/progress');
    if (r.status === 0) break; // связи нет — панель уже перезапускается
    const p = r.data;
    if (p.error){ fail(p.error); return; }
    stage = p.stage || stage;
    if (stage) $('updateStage').textContent = t('upd_stage_' + stage);
    if (!p.running && !p.error && stage !== 'restart') break;
  }
  $('updateStage').textContent = t('upd_stage_restart');
  // Перезапуск: панель пропадает на несколько секунд и возвращается новой.
  const deadline = Date.now() + 90000;
  while (Date.now() < deadline){
    await sleep(1500);
    try {
      const st = await api('/api/status');
      if (st.ok && st.data.version && st.data.version.replace(/^v/, '') === UPDATE_TO){
        toast(t('update_done').replace('{v}', UPDATE_TO));
        setTimeout(() => location.reload(), 800);
        return;
      }
    } catch(e){ /* панель ещё перезапускается */ }
  }
  fail(t('update_no_return'));
});

/* ---------- «Обновить на всех серверах» (ТЗ 31) ----------
   Кнопка видна, когда сервер в сети серверов. Каждый сервер обновляет себя
   сам (панель, потом meshd) тем же путём, что кнопка выше; этот — последним.
   Сервер без связи обновится, когда вернётся. */
let UA_TIMER = 0;
const UA_FINAL = ['done', 'error', 'offline', 'old', 'manual', 'idle'];
function uaStage(s){
  const key = 'ua_' + s.stage;
  return I18N[LANG][key] ? t(key) : s.stage;
}
function renderUpdateAll(st){
  const list = $('updateAllList');
  list.innerHTML = '';
  st.servers.forEach(s => {
    const row = el('div', 'ua-row');
    row.appendChild(el('span', '', s.name + (s.self ? ' · ' + t('ua_this') : '')));
    row.appendChild(el('span', 'ua-v', fill('ua_versions', {p: s.panel || '—', m: s.meshdRel || s.meshd || '—'})));
    const cls = s.stage === 'done' ? ' ok' : s.stage === 'error' ? ' err' : '';
    row.appendChild(el('span', 'ua-s' + cls, uaStage(s) + (s.error ? ': ' + s.error : '')));
    list.appendChild(row);
  });
  $('updateAllNote').textContent = fill('ua_target', {v: st.target || UPDATE_TO});
}
// loadUpdateAll — версии серверов; follow — следить за ходом, пока не
// закончится (этот сервер перезапускается — панель пропадает, ждём её).
async function loadUpdateAll(follow){
  clearTimeout(UA_TIMER);
  const {ok, status, data} = await api('/api/update/all');
  if (!ok){
    if (follow && status === 0){ UA_TIMER = setTimeout(() => loadUpdateAll(true), 3000); return; }
    $('updateAllBtn').hidden = true;
    return;
  }
  const many = data.inNet && data.servers.length > 1;
  $('updateAllBtn').hidden = !many;
  $('updateAllBox').hidden = !many;
  if (!many) return;
  renderUpdateAll(data);
  const running = data.target && data.servers.some(s => !UA_FINAL.includes(s.stage));
  $('updateAllBtn').disabled = !!running;
  if (running) UA_TIMER = setTimeout(() => loadUpdateAll(true), 2000);
  else if (follow && data.target && data.servers.every(s => s.stage !== 'error')) toast(fill('ua_finished', {v: data.target}));
}
$('updateAllBtn').addEventListener('click', async () => {
  const yes = await appConfirm(fill('ua_confirm', {v: UPDATE_TO}), t('update_confirm_yes'));
  if (!yes) return;
  const {ok, data} = await api('/api/update/all', {});
  if (!ok || data.error){ showMsg($('updateMsg'), t('update_install_failed').replace('{e}', data.error || t('err_internal')), 'err'); return; }
  loadUpdateAll(true);
});

$('autostartCheck').addEventListener('change', async e => {
  showMsg($('autostartMsg'), '', '');
  const want = e.target.checked;
  e.target.disabled = true;
  const {ok, data} = await api('/api/autostart', {enabled: want});
  e.target.disabled = false;
  if (!ok){
    e.target.checked = !want;
    showMsg($('autostartMsg'), errText(data.error, data), 'err');
    return;
  }
  $('autostartCheck').checked = !!data.enabled;
});

// Журнал установки meshd / ретранслятора UDP / «Перейти» (ТЗ 34) — внутри
// карточки, чья кнопка его начала, под её кнопками: отдельная карточка
// появлялась между другими и перекладывала их по столбцам. Увиденный на
// этой странице журнал остаётся до перезагрузки или «×»; после
// перезагрузки — как раньше: идёт, с ошибкой или закончился меньше минуты назад.
let JOB_SEEN = '', JOB_HIDDEN = '';
function jobHost(role){
  if (role === 'main') return $('meshServiceCard');
  if (role === 'secondary') return $('meshSecView');
  return $('meshRoleCard');
}
$('meshJobClose').addEventListener('click', () => {
  JOB_HIDDEN = $('meshJobLog').dataset.started || '';
  $('meshJob').hidden = true;
});
function renderJob(job, role){
  const box = $('meshJob'), host = jobHost(role);
  if (box.parentNode !== host) host.appendChild(box);
  const recent = job && job.started !== JOB_HIDDEN && (job.running || job.error || job.started === JOB_SEEN ||
    (job.finished && Date.now() - new Date(job.finished) < 60000));
  box.hidden = !recent;
  if (!recent) return;
  JOB_SEEN = job.started;
  const title = $('meshJobTitle');
  title.innerHTML = '';
  if (job.running){
    title.appendChild(el('span', 'spin'));
    title.appendChild(document.createTextNode(t(job.kind === 'udprelay' ? 'job_udprelay' : job.kind === 'join' ? 'srv_job_join' : 'job_install')));
  } else {
    title.textContent = job.error ? t('job_failed') + ': ' + job.error : t('job_done');
  }
  const log = $('meshJobLog');
  const lines = job.lines || [];
  if (Number(log.dataset.n || 0) !== lines.length || log.dataset.started !== job.started){
    const atBottom = log.scrollTop + log.clientHeight >= log.scrollHeight - 20;
    const from = log.dataset.started === job.started ? Number(log.dataset.n || 0) : 0;
    if (from === 0) log.innerHTML = '';
    lines.slice(from).forEach(l => log.appendChild(el('div', 'l', l)));
    log.dataset.n = lines.length;
    log.dataset.started = job.started;
    if (atBottom || from === 0) log.scrollTop = log.scrollHeight;
  }
}

/* ---------- сеть устройств на этом сервере ---------- */
function renderDnCard(m){
  const on = m.effectiveRole === 'main';
  $('meshRoleCard').hidden = m.effectiveRole === 'secondary';
  $('srvDnOff').hidden = on;
  $('srvDnOn').hidden = !on;
  if (!on) return;
  const st = $('srvDnState');
  st.innerHTML = '';
  st.appendChild(el('span', 'badge ' + (m.meshd && m.meshd.active ? 'ok' : 'warn'), svcText(m.meshd) + (m.version ? ' · meshd ' + meshdLabel(m) : '')));
  $('srvDnName').textContent = m.name || m.sshHost || '—';
}
$('meshMakeMainBtn').addEventListener('click', async () => {
  const btn = $('meshMakeMainBtn');
  btn.disabled = true;
  const {ok, data} = await api('/api/mesh/role', {role: 'main', name: $('meshName').value.trim()});
  btn.disabled = false;
  if (!ok){ showMsg($('meshRoleMsg'), data.error || t('err_internal'), 'err'); return; }
  showMsg($('meshRoleMsg'), '', '');
  loadMesh();
});

/* ---------- сервер, подключённый старым способом ---------- */
function renderMeshSecondary(){
  const m = MESH, l = m.link || {};
  $('secMainHost').textContent = (m.mainHost || '') + (m.mainPort && m.mainPort !== 22 ? ':' + m.mainPort : '');
  const st = $('secState');
  st.innerHTML = '';
  st.appendChild(el('span', 'badge ' + (l.connected ? 'ok' : 'warn'), l.connected ? t('sec_connected') : t('sec_connecting')));
  $('secErr').hidden = !l.error;
  $('secErr').textContent = l.error || '';
}
// «Перейти»: своя служба сети (адрес в сети прежний) и вход в сеть серверов
// того сервера, к которому был проброс, — по уже разрешённому там ключу.
$('secToMainBtn').addEventListener('click', async () => {
  const yes = await appConfirm(t('srv_legacy_confirm'), t('srv_legacy_go'));
  if (!yes) return;
  const {ok, data} = await api('/api/mesh/servers/migrate', {});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  loadMesh();
});
$('udpRelayBtn').addEventListener('click', async () => {
  const {ok, data} = await api('/api/udprelay/install', {});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  loadMesh();
});
$('meshReinstallBtn').addEventListener('click', async () => {
  const {ok, status, data} = await api('/api/mesh/reinstall', {});
  // status 0 — связь оборвалась (панель открыта через сеть устройств, а
  // meshd перезапускается): работа на сервере идёт дальше, опрос покажет.
  if (!ok && status !== 0){ toast(data.error || t('err_internal'), 'err'); return; }
  // Стоит meshd того же релиза, что и панель, — ставить нечего (ТЗ 25).
  if (data.latest) toast(fill('meshd_latest', {v: MESH ? meshdLabel(MESH) : ''}));
  loadMesh();
});

async function meshOff(){
  const yes = await appConfirm(t('confirm_mesh_off'), t('confirm_off_yes'));
  if (!yes) return;
  const {ok, data} = await api('/api/mesh/role', {role: 'off'});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); }
  loadMesh();
}
$('meshOffBtn').addEventListener('click', meshOff);
$('secOffBtn').addEventListener('click', meshOff);
/* ---------- настройки: ёмкость сервера ---------- */
// Та же формула, что MaxStartupsFor в capacity.go: 4 соединения на
// устройство, полный отказ — вдвое выше, не ниже умолчаний sshd.
function maxStartupsFor(n){
  const begin = Math.max(10, n * 4), full = Math.max(100, begin * 2);
  return begin + ':30:' + full;
}
function capPreview(){
  const n = Math.max(1, Math.min(Number($('capNum').max) || 250, Number($('capNum').value) || 1));
  $('capWanted').textContent = 'MaxStartups ' + maxStartupsFor(n);
  return n;
}
async function loadCapacity(){
  const {ok, data} = await api('/api/settings/capacity');
  if (!ok) return;
  ['capRange', 'capNum'].forEach(id => { $(id).min = data.min; $(id).max = data.max; $(id).value = data.maxDevices; });
  $('capEffective').textContent = data.effective ? 'MaxStartups ' + data.effective : '—';
  capPreview();
}
$('capRange').addEventListener('input', e => { $('capNum').value = e.target.value; capPreview(); });
$('capNum').addEventListener('input', e => { $('capRange').value = e.target.value; capPreview(); });
$('capSaveBtn').addEventListener('click', async () => {
  const n = capPreview();
  const btn = $('capSaveBtn');
  btn.disabled = true;
  showMsg($('capMsg'), '', '');
  const {ok, data} = await api('/api/settings/capacity', {maxDevices: n});
  btn.disabled = false;
  if (!ok){ showMsg($('capMsg'), data.error || t('err_internal'), 'err'); return; }
  $('capEffective').textContent = data.effective ? 'MaxStartups ' + data.effective : '—';
  toast(t('cap_saved').replace('{n}', data.maxDevices));
});

// renderMeshService — служба сети и ретранслятор UDP (карточка «Служба сети»).
function renderMeshService(m){
  $('meshSvcState').innerHTML = '';
  const b = el('span', 'badge ' + (m.meshd.active ? 'ok' : 'warn'), svcText(m.meshd));
  $('meshSvcState').appendChild(b);
  $('meshSvcVersion').textContent = m.version ? meshdLabel(m) : '—';
  $('meshSvcSince').textContent = m.startedAt ? fmtClock(m.startedAt) : '—';
  $('meshReinstallBtn').disabled = !!(m.job && m.job.running);
  // Ретранслятор UDP: без него у клиентов с «Пробрасывать UDP» не работают
  // звонки, игры и QUIC (видео YouTube подолгу стоит).
  const relay = m.udprelay || {};
  $('udpRelayState').innerHTML = '';
  $('udpRelayState').appendChild(el('span', 'badge ' + (relay.active ? 'ok' : 'warn'),
    relay.installed ? svcText(relay) : t('udprelay_none')));
  $('udpRelayBtn').textContent = t(relay.installed ? 'udprelay_update' : 'udprelay_install');
  $('udpRelayBtn').disabled = !!(m.job && m.job.running);
}

/* ---------- учётная запись панели: пароль, выход ---------- */
$('showPassFormBtn').addEventListener('click', () => {
  $('passForm').hidden = false;
  $('showPassFormBtn').hidden = true;
  $('curPass2').focus();
});
$('cancelPassBtn').addEventListener('click', () => {
  $('passForm').hidden = true;
  $('showPassFormBtn').hidden = false;
  showMsg($('accountMsg'), '', '');
});
$('accountChangeBtn').addEventListener('click', () => doChangePassword(
  'curPass2','newPass21','newPass22', $('accountMsg'), $('accountChangeBtn'),
  () => {
    $('passForm').hidden = true;
    $('showPassFormBtn').hidden = false;
    showMsg($('accountMsg'), t('password_changed'), 'ok');
  }));

$('logoutBtn').addEventListener('click', async () => {
  await api('/api/logout', {});
  loadSession();
});

/* ---------- устройства сети (ТЗ 40) ----------
   Версия каждого устройства сети и «Обновить»: устройство обновляется само
   (файл — из релиза на GitHub), сеть передаёт только номер версии. */
let ND_TIMER = 0;
async function loadNetDevs(){
  clearTimeout(ND_TIMER);
  const {ok, data} = await api('/api/update/devices');
  if (!ok){ $('netDevCard').hidden = true; return; }
  $('netDevCard').hidden = false;
  $('netDevNote').textContent = data.old ? t('nu_old') : data.target ? fill('nu_note', {v: data.target}) : t('nu_note_none');
  const list = $('netDevList');
  list.innerHTML = '';
  const devs = data.devices || [];
  if (!devs.length) list.appendChild(el('p', 'note', t('nu_empty')));
  devs.forEach(d => {
    const row = el('div', 'nd-row');
    const nm = el('span', 'nd-n');
    const ic = osIcon(d.platform);
    if (ic) nm.appendChild(ic);
    nm.appendChild(document.createTextNode(d.name + (d.netName ? ' · ' + d.netName : '')));
    row.appendChild(nm);
    row.appendChild(el('span', 'nd-v', (d.version || '—') + (d.build ? ' · ' + d.build.toUpperCase() : '')));
    let st = t('nu_' + d.state);
    if (d.state === 'offline' && d.lastSeen) st += ' · ' + fill('nu_seen', {t: fmtClock(d.lastSeen)});
    if (d.updSt) st += ' · ' + d.updSt;
    row.appendChild(el('span', 'nd-s ' + d.state, st));
    const cell = el('span', 'nd-b');
    if (d.canUpdate){
      const b = el('button', 'btn ghost small', t('nu_update'));
      b.addEventListener('click', async () => {
        b.disabled = true;
        const r = await api('/api/update/device', {net: d.net, device: d.id});
        if (!r.ok){ b.disabled = false; toast(r.data.error || t('err_internal'), 'err'); return; }
        toast(t('nu_sent_one'));
        ND_TIMER = setTimeout(loadNetDevs, 3000);
      });
      cell.appendChild(b);
    }
    row.appendChild(cell);
    list.appendChild(row);
  });
  $('netDevAllBtn').hidden = !devs.some(d => d.canUpdate);
  // Пока кто-то обновляется — смотреть чаще.
  if (devs.some(d => d.updSt && d.state !== 'ok')) ND_TIMER = setTimeout(loadNetDevs, 5000);
}
$('netDevAllBtn').addEventListener('click', async () => {
  const {ok, data} = await api('/api/update/devices', {});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  toast(fill('nu_sent', {n: data.sent}));
  ND_TIMER = setTimeout(loadNetDevs, 3000);
});

/* ---------- события каркаса ---------- */
onPanel('show:Settings', () => { loadAutostart(); loadCapacity(); loadEgress(); loadNetDevs(); });
onPanel('status', st => { $('setVersion').textContent = st.version; });
onPanel('mesh', (m, role) => {
  const isMain = role === 'main';
  renderJob(m.job, role);
  $('meshServiceCard').hidden = !isMain;
  $('meshSecView').hidden = role !== 'secondary';
  if (isMain) renderMeshService(m);
  if (role === 'secondary') renderMeshSecondary();
  renderDnCard(m);
});
