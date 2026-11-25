// Книга панели («Настройки» → «Книга») и таблица «? → глава» (BOOK_HELP).
/* ---------- Книга: Markdown → безопасный HTML ---------- */
/* Текст глав — свои файлы, но и он проходит через экранирование: сырой HTML
   из Markdown не попадает на страницу, ссылки — только http(s) и главы Книги.
   Блок между метками проверяется отдельным тестом на node (см. отчёт ТЗ 21). */
/* BOOK-RENDER-BEGIN */
const bkEsc = s => String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
  .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
function bkEmph(s){
  return s.replace(/\*\*([^*]+?)\*\*/g, '<b>$1</b>')
    .replace(/(^|[^\w])__([^_]+?)__(?!\w)/g, '$1<b>$2</b>')
    .replace(/\*([^*\s](?:[^*]*[^*\s])?)\*/g, '<i>$1</i>')
    .replace(/(^|[^\w])_([^_\s](?:[^_]*[^_\s])?)_(?!\w)/g, '$1<i>$2</i>');
}
function bkInline(src){
  const keep = [];
  const hold = html => '\u0000' + (keep.push(html) - 1) + '\u0000';
  let s = bkEsc(String(src).replace(/\u0000/g, ''));
  s = s.replace(/`([^`]+)`/g, (_, c) => hold('<code>' + c + '</code>'));
  s = s.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (_, txt, url) => {
    if (/^https?:\/\//i.test(url))
      return hold('<a href="' + url + '" target="_blank" rel="noopener noreferrer">' + bkEmph(txt) + '</a>');
    const ch = /^#?(\d\d-[a-z0-9-]+?)(?:\.md)?$/i.exec(url);
    if (ch) return hold('<a href="#" data-bk="' + ch[1] + '">' + bkEmph(txt) + '</a>');
    return txt;                                      // чужая ссылка — просто текст
  });
  s = bkEmph(s);
  for (let k = 0; k < 3; k++) s = s.replace(/\u0000(\d+)\u0000/g, (_, n) => keep[+n]);
  return s;
}
function bkMd(md){ return bkBlocks(String(md).replace(/\r\n/g, '\n').split('\n'), 0); }
function bkBlocks(lines, base){
  const n = lines.length, out = [];
  const ITEM = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;
  const FENCE = /^\s*(```|~~~)/;
  const HEAD = /^(#{1,4})\s+(.*?)\s*#*\s*$/;
  const HR = /^\s*([-*_])(\s*\1){2,}\s*$/;
  const SEP = l => l.includes('|') && l.includes('-') && /^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$/.test(l);
  const cells = l => {
    let s = l.trim();
    if (s[0] === '|') s = s.slice(1);
    const res = [];
    let cur = '';
    for (let k = 0; k < s.length; k++){
      if (s[k] === '\\' && s[k + 1] === '|'){ cur += '|'; k++; }
      else if (s[k] === '|'){ res.push(cur.trim()); cur = ''; }
      else cur += s[k];
    }
    if (cur.trim() || !s.endsWith('|')) res.push(cur.trim());
    return res;
  };
  const at = i => ' data-l="' + (base + i + 1) + '"';
  const starts = i => FENCE.test(lines[i]) || HEAD.test(lines[i]) || HR.test(lines[i]) ||
    ITEM.test(lines[i]) || /^\s*>/.test(lines[i]) || (lines[i].includes('|') && i + 1 < n && SEP(lines[i + 1]));
  function list(i, indent){
    const first = ITEM.exec(lines[i]);
    const tag = /^\d/.test(first[2]) ? 'ol' : 'ul';
    let html = '<' + tag + '>';
    while (i < n){
      const m = ITEM.exec(lines[i]);
      if (!m || m[1].length !== indent || (/^\d/.test(m[2]) ? 'ol' : 'ul') !== tag) break;
      let text = m[3], sub = '';
      const ln = i;
      i++;
      for (;;){
        if (i >= n) break;
        if (!lines[i].trim()){
          let j = i;
          while (j < n && !lines[j].trim()) j++;
          const mm = j < n && ITEM.exec(lines[j]);
          if (mm && mm[1].length >= indent){ i = j; continue; }
          break;
        }
        const mm = ITEM.exec(lines[i]);
        if (mm){
          if (mm[1].length > indent){ const r = list(i, mm[1].length); sub += r.html; i = r.next; continue; }
          break;
        }
        if (/^\s+/.test(lines[i]) && lines[i].search(/\S/) > indent){ text += ' ' + lines[i].trim(); i++; continue; }
        break;
      }
      html += '<li' + at(ln) + '>' + bkInline(text) + sub + '</li>';
    }
    return {html: html + '</' + tag + '>', next: i};
  }
  let i = 0;
  while (i < n){
    const line = lines[i];
    if (!line.trim()){ i++; continue; }
    let m;
    if ((m = FENCE.exec(line))){
      const buf = [], start = i;
      i++;
      while (i < n && !lines[i].trim().startsWith(m[1])){ buf.push(lines[i]); i++; }
      i++;
      out.push('<pre' + at(start) + '><code>' + bkEsc(buf.join('\n')) + '</code></pre>');
      continue;
    }
    if ((m = HEAD.exec(line))){
      out.push('<h' + m[1].length + at(i) + '>' + bkInline(m[2]) + '</h' + m[1].length + '>');
      i++;
      continue;
    }
    if (HR.test(line)){ out.push('<hr>'); i++; continue; }
    if (line.includes('|') && i + 1 < n && SEP(lines[i + 1])){
      const head = cells(line), start = i;
      i += 2;
      let h = '<div class="bk-tw"><table' + at(start) + '><thead><tr>' +
        head.map(c => '<th>' + bkInline(c) + '</th>').join('') + '</tr></thead><tbody>';
      while (i < n && lines[i].trim() && lines[i].includes('|')){
        h += '<tr' + at(i) + '>' + cells(lines[i]).map(c => '<td>' + bkInline(c) + '</td>').join('') + '</tr>';
        i++;
      }
      out.push(h + '</tbody></table></div>');
      continue;
    }
    if (/^\s*>/.test(line)){
      const buf = [], start = i;
      while (i < n && /^\s*>/.test(lines[i])){ buf.push(lines[i].replace(/^\s*>\s?/, '')); i++; }
      out.push('<blockquote' + at(start) + '>' + bkBlocks(buf, base + start) + '</blockquote>');
      continue;
    }
    if (ITEM.test(line)){
      const r = list(i, ITEM.exec(line)[1].length);
      out.push(r.html);
      i = r.next;
      continue;
    }
    const buf = [line.trim()], start = i;
    i++;
    while (i < n && lines[i].trim() && !starts(i)){ buf.push(lines[i].trim()); i++; }
    out.push('<p' + at(start) + '>' + bkInline(buf.join(' ')) + '</p>');
  }
  return out.join('');
}
/* BOOK-RENDER-END */

/* ---------- Книга: страница ---------- */
/* Какие «?» ведут в какую главу. Единственное место такой привязки: главы
   пишутся отдельно, и если главы с этим id в сборке нет, ссылки просто не
   будет (bookChapterFor). */
const BOOK_HELP = {
  // «?» у названия раздела в шапке (ТЗ 30, core.js PAGE_HEAD)
  home_sub:'01-about', devices_sub:'03-devices', mesh_sub:'04-networks', access_sub:'05-access', plugins_sub:'11-plugins',
  hint_tile_devices:'03-devices',
  hint_map:'04-networks',
  hint_acc:'05-access', hint_acc_rules:'05-access', hint_acc_client_groups:'05-access', hint_scope_servers:'03-devices', hint_nd:'05-access',
  hint_acc_creator:'05-access', hint_sm:'05-access', hint_ck:'05-access',
  hint_tile_servers:'06-servers', hint_servers:'06-servers', hint_mesh_name:'06-servers',
  hint_srv_dn:'06-servers', hint_srv_code:'06-servers',
  hint_self:'06-servers',
  hint_fc:'07-no-server',
  hint_egress:'09-server-egress',
  hint_node_host:'11-plugins', hint_node_quota:'11-plugins',
  hint_node_ad:'11-plugins', hint_node_site:'11-plugins', hint_plugin_adblock:'11-plugins',
  hint_plugin_machines:'11-plugins', hint_plugin_sitecheck:'11-plugins',
  hint_plugin_backup:'12-backups',
  hint_jr:'13-journal',
  hint_service:'15-update'
};
const BK = {lang:'', chapters:[], cur:'', want:'', loading:null};
const BK_KEY = 'panelBook';
function bkState(){ try { return JSON.parse(localStorage.getItem(BK_KEY) || '{}') || {}; } catch (e) { return {}; } }
function bkSave(patch){
  try { localStorage.setItem(BK_KEY, JSON.stringify(Object.assign(bkState(), patch))); } catch (e) {}
}
const bkNarrow = () => matchMedia('(max-width:760px)').matches;
function bkGetY(){
  return bkNarrow() ? Math.max(0, Math.round(-$('bkText').getBoundingClientRect().top)) : $('bkText').scrollTop;
}
function bkSetY(y){
  if (bkNarrow()) scrollTo(0, $('bkText').getBoundingClientRect().top + scrollY + y); else $('bkText').scrollTop = y;
}
let bkSaveTimer = null;
function bkOnScroll(){
  if (!BK.cur || $('tabSettings').hidden) return;
  clearTimeout(bkSaveTimer);
  bkSaveTimer = setTimeout(() => bkSave({ch: BK.cur, y: bkGetY()}), 250);
}
$('bkText').addEventListener('scroll', bkOnScroll, {passive: true});
addEventListener('scroll', bkOnScroll, {passive: true});

// bkEnsure загружает главы текущего языка (один раз на язык).
function bkEnsure(){
  if (BK.lang === LANG && BK.chapters.length) return Promise.resolve(true);
  if (BK.loading && BK.loading.lang === LANG) return BK.loading.p;
  const lang = LANG;
  const p = api('/api/book?lang=' + lang).then(r => {
    if (r.ok && Array.isArray(r.data.chapters)){ BK.lang = lang; BK.chapters = r.data.chapters; }
    return BK.lang === LANG && BK.chapters.length > 0;
  }).catch(() => false).then(v => { if (BK.loading && BK.loading.p === p) BK.loading = null; return v; });
  BK.loading = {lang, p};
  return p;
}
// bookChapterFor — id главы для подсказки или '' (главы нет — ссылки нет).
function bookChapterFor(hintKey){
  const id = BOOK_HELP[hintKey];
  return id && BK.lang === LANG && BK.chapters.some(c => c.id === id) ? id : '';
}

function bkRenderToc(){
  const toc = $('bkToc');
  toc.innerHTML = '';
  if (!BK.chapters.length){
    const d = document.createElement('div');
    d.className = 'bk-empty';
    d.textContent = t('book_empty');
    toc.appendChild(d);
    return;
  }
  for (const c of BK.chapters){
    const b = document.createElement('button');
    b.type = 'button';
    b.dataset.id = c.id;
    b.textContent = c.title;
    b.classList.toggle('on', c.id === BK.cur);
    b.onclick = () => bkOpen(c.id);
    toc.appendChild(b);
  }
}

// bkMark подсвечивает найденное только в тексте (узлы), не в разметке.
function bkMark(root, q){
  const ql = q.toLowerCase();
  const w = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const nodes = [];
  while (w.nextNode()) nodes.push(w.currentNode);
  for (const nd of nodes){
    const low = nd.nodeValue.toLowerCase();
    let i = low.indexOf(ql);
    if (i < 0) continue;
    const frag = document.createDocumentFragment();
    let from = 0;
    while (i >= 0){
      frag.appendChild(document.createTextNode(nd.nodeValue.slice(from, i)));
      const m = document.createElement('mark');
      m.textContent = nd.nodeValue.slice(i, i + q.length);
      frag.appendChild(m);
      from = i + q.length;
      i = low.indexOf(ql, from);
    }
    frag.appendChild(document.createTextNode(nd.nodeValue.slice(from)));
    nd.parentNode.replaceChild(frag, nd);
  }
}

// bkOpen открывает главу; opt.q + opt.line — место, найденное поиском;
// opt.y — вернуть прокрутку (после обновления страницы).
function bkOpen(id, opt){
  opt = opt || {};
  const c = BK.chapters.find(x => x.id === id);
  if (!c) return false;
  BK.cur = id;
  const box = $('bkText');
  box.innerHTML = bkMd(c.md);
  if (opt.q) bkMark(box, opt.q);
  $('bk').classList.add('open');
  $('bkToc').querySelectorAll('button').forEach(b => b.classList.toggle('on', b.dataset.id === id));
  requestAnimationFrame(() => {
    if (opt.line){
      let target = null;
      box.querySelectorAll('[data-l]').forEach(el => { if (+el.dataset.l <= opt.line) target = el; });
      const mk = (target && target.querySelector('mark')) || box.querySelector('mark');
      (mk || target || box).scrollIntoView({block: 'center'});
    } else if (opt.y){
      bkSetY(opt.y);
    } else if (bkNarrow()){
      scrollTo(0, Math.max(0, $('bookCard').getBoundingClientRect().top + scrollY - 60));
    } else {
      box.scrollTop = 0;
    }
  });
  bkSave({ch: id, y: opt.y || 0});
  return true;
}
$('bkText').addEventListener('click', e => {
  const a = e.target.closest('a[data-bk]');
  if (!a) return;
  e.preventDefault();
  bkOpen(a.dataset.bk);
});
$('bkBack').onclick = () => {
  $('bk').classList.remove('open');
  bkSave({ch: '', y: 0});
  BK.cur = '';
  bkRenderToc();
  scrollTo(0, Math.max(0, $('bookCard').getBoundingClientRect().top + scrollY - 60));
};

// Поиск по всем главам: глава + строка. Клик — глава на этом месте.
function bkSearch(){
  const q = $('bkQ').value.trim(), f = $('bkFound');
  if (q.length < 2){ f.hidden = true; f.innerHTML = ''; return; }
  const ql = q.toLowerCase();
  f.innerHTML = '';
  let total = 0;
  outer: for (const c of BK.chapters){
    const lines = c.md.split('\n');
    let perCh = 0;
    for (let i = 0; i < lines.length; i++){
      const plain = lines[i].replace(/^\s*(#{1,4}|[-*+]|\d+[.)]|>)\s+/, '')
        .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1').replace(/[*`_]/g, '');
      const at = plain.toLowerCase().indexOf(ql);
      if (at < 0) continue;
      const b = document.createElement('button');
      b.type = 'button';
      const ttl = document.createElement('b');
      ttl.textContent = c.title;
      const sn = document.createElement('span');
      sn.className = 'sn';
      const from = Math.max(0, at - 40);
      sn.appendChild(document.createTextNode((from ? '…' : '') + plain.slice(from, at)));
      const mk = document.createElement('mark');
      mk.textContent = plain.slice(at, at + q.length);
      sn.appendChild(mk);
      sn.appendChild(document.createTextNode(plain.slice(at + q.length, at + q.length + 80)));
      b.appendChild(ttl);
      b.appendChild(sn);
      b.onclick = () => bkOpen(c.id, {q, line: i + 1});
      f.appendChild(b);
      total++;
      if (total >= 60) break outer;
      if (++perCh >= 4) break;
    }
  }
  if (!total){
    const d = document.createElement('div');
    d.className = 'empty';
    d.textContent = t('book_nores');
    f.appendChild(d);
  }
  f.hidden = false;
}
let bkQTimer = null;
$('bkQ').addEventListener('input', () => { clearTimeout(bkQTimer); bkQTimer = setTimeout(bkSearch, 150); });
$('bkQ').addEventListener('keydown', e => { if (e.key === 'Escape'){ $('bkQ').value = ''; bkSearch(); } });

// bkShow вызывается, когда открывают «Настройки» или меняют язык: грузит
// главы и открывает ту, что запомнена (или ту, куда послали из «?»).
async function bkShow(force){
  if (force){ BK.chapters = []; BK.lang = ''; }
  const ok = await bkEnsure();
  if (BK.lang !== LANG && ok) return;
  bkRenderToc();
  if (!ok){ $('bkText').textContent = ''; $('bk').classList.remove('open'); return; }
  const st = bkState();
  const want = BK.want;
  BK.want = '';
  const first = !BK.shown;               // первый показ после загрузки страницы
  BK.shown = true;
  const keep = BK.chapters.some(c => c.id === BK.cur) ? BK.cur : '';
  const id = want || keep || st.ch;
  if (id && BK.chapters.some(c => c.id === id)){
    bkOpen(id, {y: !want && first && id === st.ch ? st.y : 0});
  } else if (!bkNarrow()){
    bkOpen(BK.chapters[0].id);           // на широком экране справа не пусто
  } else {
    $('bk').classList.remove('open');
  }
  bkSearch();
}
// bkGoto — из «?»: открыть «Настройки» → «Книга» на главе id.
function bkGoto(id){
  BK.want = id;
  showTab('Settings');
  bkShow().then(() => { if (!bkNarrow()) $('bookCard').scrollIntoView({block: 'start'}); });
}

/* ---------- события каркаса ---------- */
onPanel('lang', () => {
  if (BK.chapters.length && BK.lang !== LANG) bkShow(true);
  else if (BK.chapters.length) bkRenderToc();
});
onPanel('sub:Settings', sub => { if (sub === 'book') bkShow(); });
onPanel('show:Settings', () => { if (SUB.Settings === 'book') bkShow(); });
