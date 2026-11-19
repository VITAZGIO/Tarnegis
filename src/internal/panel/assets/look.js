// look.js — тема и фон панели (ТЗ 25, «Настройки» → «Вид») и фирменный фон.
// Темы — общий theme.css программы, знак — её logo.svg (шаблон #vgLogo).
// Фоны «Код» и «Автор» повторяют программу (uiassets/index.html,
// renderBackdrop): там они внутри страницы окна, а не отдельным файлом,
// поэтому код перенесён сюда, а не подключён.
const LOOK = {theme: '', bg: ''};
try { Object.assign(LOOK, JSON.parse(localStorage.getItem('panelLook') || '{}')); } catch (_){}

// vgPaths — фигуры знака VG без собственного цвета (красит тот, кто вставил).
function vgPaths(){
  const svg = $('vgLogo').content.querySelector('svg');
  return [...svg.querySelectorAll('path')].map(p => '<path d="' + p.getAttribute('d') + '"/>').join('');
}

function applyLook(save){
  document.documentElement.dataset.theme = LOOK.theme;
  document.documentElement.dataset.bg = LOOK.bg;
  document.querySelectorAll('#themeSeg button').forEach(b => b.classList.toggle('on', b.dataset.v === LOOK.theme));
  document.querySelectorAll('#bgSeg button').forEach(b => b.classList.toggle('on', b.dataset.v === LOOK.bg));
  try { localStorage.setItem('panelLook', JSON.stringify(LOOK)); } catch (_){}
  renderBackdrop();
  if (save) saveLook();
}

async function saveLook(){
  const {ok, data} = await api('/api/settings/look', {theme: LOOK.theme, bg: LOOK.bg});
  showMsg($('lookMsg'), ok ? '' : errOf(data), 'err');
}

// loadLook — выбор с сервера (другой браузер, тот же логин). Пусто на
// сервере — остаётся выбор этого браузера.
async function loadLook(){
  const {ok, data} = await api('/api/settings/look');
  if (!ok || (!data.theme && !data.bg)) return;
  if (data.theme === LOOK.theme && data.bg === LOOK.bg) return;
  LOOK.theme = data.theme || '';
  LOOK.bg = data.bg || '';
  applyLook(false);
}

['themeSeg', 'bgSeg'].forEach(id => document.querySelectorAll('#' + id + ' button').forEach(b => {
  b.addEventListener('click', () => {
    LOOK[id === 'themeSeg' ? 'theme' : 'bg'] = b.dataset.v;
    applyLook(true);
  });
}));

/* ---------- фон: «Код» и «Автор» ---------- */
let mxTimer = null, mxResize = null;
function stopMatrix(){
  clearInterval(mxTimer); mxTimer = null;
  if (mxResize) removeEventListener('resize', mxResize);
  mxResize = null;
}
// Падающие символы. Кадр раз в 80 мс, и только пока окно видно: фон не
// должен грузить процессор. При «уменьшить движение» — один неподвижный кадр.
function startMatrix(c){
  const ctx = c.getContext('2d');
  const fs = 14;
  const glyphs = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789';
  let cols = [], w = 0, h = 0;
  const size = () => {
    w = c.width = c.clientWidth; h = c.height = c.clientHeight;
    cols = Array.from({length: Math.ceil(w / fs)}, () => Math.random() * h / fs | 0);
  };
  size();
  mxResize = size;
  addEventListener('resize', size);
  const color = getComputedStyle(document.documentElement).getPropertyValue('--ok').trim() || '#2fbf71';
  const step = () => {
    if (document.hidden) return;
    ctx.globalCompositeOperation = 'destination-out';
    ctx.fillStyle = 'rgba(0,0,0,.09)';
    ctx.fillRect(0, 0, w, h);
    ctx.globalCompositeOperation = 'source-over';
    ctx.fillStyle = color;
    ctx.font = fs + 'px monospace';
    cols.forEach((y, i) => {
      ctx.fillText(glyphs[Math.random() * glyphs.length | 0], i * fs, y * fs);
      cols[i] = (y * fs > h && Math.random() > .975) ? 0 : y + 1;
    });
  };
  if (REDUCED_MOTION_LOOK){
    for (let i = 0; i < 60; i++) step();
    return;
  }
  mxTimer = setInterval(step, 80);
}
const REDUCED_MOTION_LOOK = !!(window.matchMedia && matchMedia('(prefers-reduced-motion: reduce)').matches);

// authorPattern — сетка знаков VG, повёрнутая против часовой (как в программе).
let PATTERN_N = 0;
function authorPattern(){
  const id = 'vgp' + (++PATTERN_N);
  return '<svg aria-hidden="true"><defs><pattern id="' + id + '" width="108" height="96" patternUnits="userSpaceOnUse"' +
    ' patternTransform="rotate(-35)"><g transform="translate(20 22) scale(0.108)">' + vgPaths() +
    '</g></pattern></defs><rect width="100%" height="100%" fill="url(#' + id + ')"/></svg>';
}

function renderBackdrop(){
  const box = $('backdrop');
  stopMatrix();
  box.innerHTML = '';
  if (LOOK.bg === 'code'){
    const c = document.createElement('canvas');
    box.appendChild(c);
    startMatrix(c);
  } else if (LOOK.bg === 'author'){
    box.innerHTML = authorPattern();
  }
}

/* ---------- фирменный фон (заготовка для заходa 10) ---------- */
// brandBlock — авторский узор программы и знак VG по центру: бирюзовый, с
// той же фактурой, что у иконки программы (feTurbulence 1.6 / 4 октавы /
// зерно 5 и та же матрица — nativeui/icon-source.svg). Без кнопок. Займёт
// пустую правую половину «Устройств» (ТЗ 27).
function brandBlock(){
  const box = el('div', 'brand-bg');
  box.setAttribute('aria-hidden', 'true');
  box.innerHTML = authorPattern() +
    '<svg class="brand-vg" viewBox="0 0 626 520"><defs>' +
    '<filter id="vgGrain' + PATTERN_N + '" x="0" y="0" width="100%" height="100%">' +
    '<feTurbulence type="fractalNoise" baseFrequency="1.6" numOctaves="4" seed="5"/>' +
    '<feColorMatrix type="matrix" values="0 0 0 0 0.02  0 0 0 0 0.07  0 0 0 0 0.1  0 0 0 -1.5 1.15"/>' +
    '<feComposite in2="SourceGraphic" operator="in"/></filter></defs>' +
    '<g fill="#2de2ff">' + vgPaths() + '</g>' +
    '<g filter="url(#vgGrain' + PATTERN_N + ')">' + vgPaths() + '</g></svg>';
  return box;
}

// Пустая проверочная страница фирменного фона: адрес #brandtest (ссылка в
// плашке режима показа). Esc или щелчок — закрыть.
function brandTest(){
  const old = $('brandTest');
  if (location.hash !== '#brandtest'){ if (old) old.remove(); return; }
  if (old) return;
  const page = el('div', 'brand-test');
  page.id = 'brandTest';
  page.appendChild(brandBlock());
  page.appendChild(el('div', 'brand-test-note', t('brand_test_note')));
  page.addEventListener('click', closeBrandTest);
  document.body.appendChild(page);
}
function closeBrandTest(){
  const p = $('brandTest');
  if (p) p.remove();
  if (location.hash === '#brandtest') history.replaceState(null, '', '#' + TAB_SLUG[TAB]);
}
addEventListener('hashchange', brandTest);
addEventListener('keydown', e => { if (e.key === 'Escape' && $('brandTest')) closeBrandTest(); });

/* ---------- события каркаса ---------- */
onPanel('start', loadLook);
applyLook(false);
brandTest();
