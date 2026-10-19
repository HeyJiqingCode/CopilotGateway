// Shared helpers for the login page and the model catalog.
const CGW = (() => {
  const LANG_KEY = 'cgw_lang';
  let memoryLang = null;

  // Language is remembered per browser; it still switches when storage is blocked.
  function getLang() {
    let saved = memoryLang;
    try { saved = localStorage.getItem(LANG_KEY) || saved; } catch { /* storage blocked */ }
    if (saved === 'zh' || saved === 'en') return saved;
    return navigator.language.startsWith('zh') ? 'zh' : 'en';
  }

  function setLang(lang) {
    memoryLang = lang;
    try { localStorage.setItem(LANG_KEY, lang); } catch { /* storage blocked */ }
    document.documentElement.lang = lang === 'zh' ? 'zh-CN' : 'en';
  }

  function esc(value) {
    return String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  }

  function icon(name, cls = '') {
    return `<svg class="icon ${cls}" aria-hidden="true"><use href="/web/assets/icons.svg#${name}"></use></svg>`;
  }

  // Toasts sit in the top-right corner, never in the layout, and close after 3 s
  // unless hovered or focused. Identical messages are not stacked.
  function toast(message, type = 'success') {
    let host = document.querySelector('.toasts');
    if (!host) {
      host = document.createElement('div');
      host.className = 'toasts';
      host.setAttribute('role', 'status');
      host.setAttribute('aria-live', 'polite');
      document.body.appendChild(host);
    }
    for (const el of host.children) {
      if (el.dataset.key === type + message) return;
    }
    const el = document.createElement('div');
    el.className = `toast toast-${type}`;
    el.dataset.key = type + message;
    el.innerHTML = `<span class="toast-icon">${icon(type === 'error' ? 'circle-alert' : 'circle-check')}</span><span class="toast-text">${esc(message)}</span>`;
    host.appendChild(el);
    while (host.children.length > 4) host.firstElementChild.remove();

    let timer;
    const start = () => { timer = setTimeout(() => el.remove(), 3000); };
    const stop = () => clearTimeout(timer);
    el.addEventListener('mouseenter', stop);
    el.addEventListener('mouseleave', start);
    el.addEventListener('focusin', stop);
    el.addEventListener('focusout', start);
    start();
  }

  return { getLang, setLang, esc, icon, toast };
})();
