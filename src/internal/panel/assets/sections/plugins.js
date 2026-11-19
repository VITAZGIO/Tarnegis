// Раздел «Плагины»: хост плагинов сервера, папки, буфер, копии, блокировка
// рекламы, проверка сайта.
/* ---------- плагины сервера (хост ssh_tunnel_node) ---------- */
let NODE = null, NODE_TIMER = null;

// Сети из meshd по порядку: безымянная — «Сеть N». gone — данных сети на
// сервере есть, а самой сети в meshd уже нет (только когда список сетей
// известен: главный сервер ответил).
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

async function loadNode(){
  clearTimeout(NODE_TIMER);
  if (TAB !== 'Plugins') return;
  const {ok, status, data} = await api('/api/node/state');
  if (ok) { NODE = data; renderNode(); }
  else if (status === 404) { NODE = null; $('nodeHostCard').hidden = true; }
  scheduleNode();
}
// Опрос — только пока вкладка «Плагины» открыта и окно видно.
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

function renderNodeJob(job){
  const recent = job && (job.running || job.error ||
    (job.finished && Date.now() - new Date(job.finished) < 60000));
  $('nodeJobCard').hidden = !recent;
  if (!recent) return;
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

function renderNode(){
  if (!NODE) return;
  $('nodeHostCard').hidden = false;
  const installed = NODE.unit && NODE.unit.installed;
  const working = !!(NODE.job && NODE.job.running);
  $('nodeSvcState').textContent = !installed ? t('svc_missing') :
    (NODE.unit.active ? t('svc_active') : t('svc_stopped'));
  $('nodeInstallBtn').hidden = installed;
  $('nodeReinstallBtn').hidden = !installed;
  $('nodeRemoveBtn').hidden = !installed;
  $('nodeDeleteDataRow').hidden = !installed;
  $('nodeInstallBtn').disabled = $('nodeReinstallBtn').disabled = working;
  renderNodeJob(NODE.job);

  const meshdRow = $('nodeMeshdRow');
  const err = $('nodeErr');
  const cfgCard = $('nodeConfigCard');
  const evCard = $('nodeEventsCard');
  const netsHead = $('nodeNetsHead');

  if (!installed || !NODE.state){
    meshdRow.hidden = true;
    cfgCard.hidden = true;
    $('nodeAdCard').hidden = true;
    $('nodeSiteCard').hidden = true;
    evCard.hidden = true;
    netsHead.hidden = true;
    $('nodeWarnCard').hidden = true;
    $('nodeNetsNone').hidden = true;
    $('nodeNets').innerHTML = '';
    err.hidden = !(installed && NODE.error);
    if (!err.hidden) err.textContent = NODE.error;
    return;
  }
  err.hidden = true;
  const st = NODE.state;
  meshdRow.hidden = false;
  $('nodeMeshdState').textContent = t('node_meshd_' + (st.meshd || 'unknown'));
  renderNodeWarnings(NODE.warnings);
  cfgCard.hidden = false;
  if (!cfgCard.contains(document.activeElement)){
    $('pluginMirror').checked = !!st.config.mirror;
    $('pluginFiles').checked = !!st.config.files;
    $('pluginFilesWrite').checked = !!st.config.filesWrite;
    $('pluginClipboard').checked = !!st.config.clipboard;
    $('pluginBackup').checked = !!st.config.backup;
    $('pluginMachines').checked = !!st.config.machines;
    $('pluginAdblock').checked = !!st.config.adblock;
    $('pluginSitecheck').checked = !!st.config.sitecheck;
    $('nodeMachDays').value = st.config.machinesDays || 30;
    $('nodeQuota').value = st.config.quotaMB || '';
  }
  renderNodeAd(st);
  $('nodeSiteCard').hidden = !st.config.sitecheck;
  $('nodeUsed').textContent = fmtBytes(st.usedBytes) + ' / ' + fmtBytes((st.config.quotaMB || 0) * 1048576);

  evCard.hidden = !(st.events && st.events.length);
  if (!evCard.hidden){
    const box = $('nodeEvents');
    box.innerHTML = '';
    st.events.slice().reverse().forEach(e => {
      const line = el('div', 'l' + (e.error ? ' err' : ''),
        new Date(e.time * 1000).toLocaleString(LANG === 'ru' ? 'ru-RU' : 'en-US') + '  ' +
        (e.net ? '[' + netName(e.net) + '] ' : '') + e.text);
      box.appendChild(line);
    });
  }

  const nets = st.networks || [];
  netsHead.hidden = nets.length === 0;
  $('nodeNetsNone').hidden = nets.length !== 0;
  // Не перерисовывать, пока в карточке сети что-то вводят (квота, имя папки).
  const box = $('nodeNets');
  if (box.contains(document.activeElement) && document.activeElement.tagName === 'INPUT') return;
  box.innerHTML = '';
  nets.forEach(n => box.appendChild(renderNodeNet(n)));
}

function nodeRow(main, sub){
  const row = el('div', 'nrow');
  const g = el('div', 'grow');
  g.appendChild(el('div', '', main));
  if (sub) g.appendChild(el('div', 'sub', sub));
  row.appendChild(g);
  return row;
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

function renderNodeNet(n){
  const gone = netGone(n.id);
  const card = el('div', 'card' + (gone ? ' gone' : ''));
  card.appendChild(el('h1', '', netName(n.id)));

  if (gone){
    card.appendChild(el('div', 'msg warn', t('node_net_gone')));
    card.appendChild(el('p', 'note', t('node_net_gone_hint')));
    const del = el('button', 'btn danger small', t('node_net_delete'));
    del.addEventListener('click', async () => {
      const yes = await appConfirm(t('confirm_net_delete').replace('{n}', netName(n.id)), t('node_net_delete'));
      if (!yes) return;
      const {ok, data} = await api('/api/node/net/delete', {net: n.id});
      if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
      loadNode();
    });
    card.appendChild(del);
  }

  // Последнее обращение устройств — по плагинам.
  const acts = el('div', 'acts');
  const la = n.lastActive || {};
  ['mirror', 'files', 'clipboard', 'backup', 'machines', 'adblock', 'sitecheck'].forEach(p => {
    acts.appendChild(el('span', '', t('plugin_' + p) + ': ' + (la[p] ? fmtAgo(la[p]) : t('node_never'))));
  });
  card.appendChild(el('div', 'nsec', t('node_last')));
  card.appendChild(acts);

  card.appendChild(el('div', 'nsec', t('node_folders_title')));
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
  if (!gone){
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
    addBtn.addEventListener('click', async () => {
      const name = nameInput.value.trim();
      if (!name) { nameInput.focus(); return; }
      const {ok, data} = await api('/api/node/folder/add',
        {net: n.id, name, quotaMB: Number(quotaInput.value) || 2048});
      if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
      nameInput.value = '';
      nameInput.blur();
      loadNode();
    });
    form.appendChild(nameInput);
    form.appendChild(quotaInput);
    form.appendChild(addBtn);
    card.appendChild(form);
  }
  if (!gone) renderNodeAccess(n, card);

  if (n.filesBytes){
    card.appendChild(el('div', 'nsec', t('node_files_title')));
    card.appendChild(nodeRow(fmtBytes(n.filesBytes)));
  }

  if (n.clipboard && (n.clipboard.items || n.clipboard.devices)){
    card.appendChild(el('div', 'nsec', t('node_clipboard_title')));
    const row = nodeRow(n.clipboard.items + ' ' + t('clip_items') + ' · ' + fmtBytes(n.clipboard.bytes),
      n.clipboard.devices + ' ' + t('clip_devices'));
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

  if (n.machines && n.machines.reporters && n.machines.reporters.length){
    card.appendChild(el('div', 'nsec', t('node_mach_title') + ' · ' + fmtBytes(n.machines.bytes) +
      (n.machines.oldest ? ' · ' + t('node_mach_since') + ' ' + new Date(n.machines.oldest * 1000).toLocaleDateString(LANG === 'ru' ? 'ru-RU' : 'en-US') : '')));
    n.machines.reporters.forEach(r => {
      card.appendChild(nodeRow((r.machines || []).join(', ') || '—',
        t('node_mach_from') + ' ' + (r.name || r.id.slice(0, 8)) + ' · ' + (r.shared ? t('node_mach_shared') : t('node_mach_private')) +
        (r.last ? ' · ' + fmtAgo(r.last) : '')));
    });
    const clr = el('button', 'btn ghost small', t('node_mach_clear'));
    clr.addEventListener('click', async () => {
      const yes = await appConfirm(t('confirm_mach_clear').replace('{n}', netName(n.id)), t('node_mach_clear'));
      if (!yes) return;
      const {ok, data} = await api('/api/node/machines/clear', {net: n.id});
      if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
      loadNode();
    });
    card.appendChild(clr);
  }

  if (n.backups && n.backups.length){
    card.appendChild(el('div', 'nsec', t('node_backups_title') + ' · ' + fmtBytes(n.backupBytes) + ' ' + t('backup_stored')));
    n.backups.forEach(b => {
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
  return card;
}

// ---- Общий список блокировки (ТЗ 15) ----
const adLines = v => v.split('\n').map(x => x.trim()).filter(Boolean);
function renderNodeAd(st){
  const card = $('nodeAdCard'), ad = st.adblock;
  card.hidden = !st.config.adblock || !ad;
  if (card.hidden) return;
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

$('nodeInstallBtn').addEventListener('click', async () => {
  const {ok, data} = await api('/api/node/install', {});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  loadNode();
});
$('nodeReinstallBtn').addEventListener('click', async () => {
  const {ok, data} = await api('/api/node/install', {});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  loadNode();
});
$('nodeRemoveBtn').addEventListener('click', async () => {
  const deleteData = $('nodeDeleteData').checked;
  const yes = await appConfirm(t(deleteData ? 'confirm_node_remove_data' : 'confirm_node_remove'), t('node_remove'));
  if (!yes) return;
  const {ok, data} = await api('/api/node/remove', {deleteData});
  if (!ok){ toast(data.error || t('err_internal'), 'err'); return; }
  loadNode();
});
$('nodeConfigSaveBtn').addEventListener('click', async () => {
  const body = {
    mirror: $('pluginMirror').checked,
    files: $('pluginFiles').checked,
    filesWrite: $('pluginFilesWrite').checked,
    clipboard: $('pluginClipboard').checked,
    backup: $('pluginBackup').checked,
    machines: $('pluginMachines').checked,
    adblock: $('pluginAdblock').checked,
    sitecheck: $('pluginSitecheck').checked,
    machinesDays: Number($('nodeMachDays').value) || undefined,
    quotaMB: Number($('nodeQuota').value) || undefined,
  };
  const {ok, data} = await api('/api/node/config', body);
  $('nodeConfigMsg').innerHTML = '';
  if (!ok){ $('nodeConfigMsg').appendChild(el('div', 'msg err', data.error || t('err_internal'))); return; }
  loadNode();
});

/* ---------- события каркаса ---------- */
onPanel('show:Plugins', () => loadNode());
