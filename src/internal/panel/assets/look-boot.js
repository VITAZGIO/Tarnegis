// Тема и фон — до первой отрисовки, чтобы страница не мигала другой темой
// (выбор этого браузера; с сервера он приходит позже, look.js).
try {
  const l = JSON.parse(localStorage.getItem('panelLook') || '{}');
  document.documentElement.dataset.theme = l.theme || '';
  document.documentElement.dataset.bg = l.bg || '';
} catch (_){}
