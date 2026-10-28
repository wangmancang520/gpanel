// Thin fetch wrapper: JSON in/out, throws Error(message) on failure.
export async function api(method, url, body, { text = false } = {}) {
  const init = { method, headers: {}, credentials: 'same-origin' };
  if (body instanceof FormData) {
    init.body = body;
  } else if (typeof body === 'string') {
    init.body = body;
    init.headers['Content-Type'] = 'text/plain; charset=utf-8';
  } else if (body !== undefined) {
    init.body = JSON.stringify(body);
    init.headers['Content-Type'] = 'application/json';
  }
  const res = await fetch(url, init);
  if (res.status === 401 && !url.startsWith('/api/auth/')) {
    window.dispatchEvent(new Event('gpanel:unauthorized'));
  }
  if (!res.ok) {
    let msg = res.statusText;
    try {
      const body = await res.json();
      msg = body.error || msg;
      if (body.mustChangePassword && session.user) session.user.mustChangePassword = true;
    } catch {}
    throw new Error(msg);
  }
  if (text) return res.text();
  const ct = res.headers.get('Content-Type') || '';
  return ct.includes('json') ? res.json() : res.text();
}

export const q = (params) => new URLSearchParams(params).toString();

// The logged-in account ({ username, role, mustChangePassword, instances })
// and what it may do. The server enforces all of this; the UI only hides
// what would be refused anyway.
export const session = Vue.reactive({ user: null });
export const roleLabels = { superadmin: '超级管理员', admin: '管理员', user: '普通用户' };
export const isAdmin = () => ['admin', 'superadmin'].includes(session.user?.role);
export const isSuper = () => session.user?.role === 'superadmin';
export const grant = (id) =>
  isAdmin() ? { console: true, files: true } : { console: false, files: false, ...(session.user?.instances?.[id] || {}) };
export async function refreshSession() {
  try { session.user = await api('GET', '/api/auth/me'); } catch { session.user = null; }
  return session.user;
}

// Simple global toast store shared by all views.
export const toasts = Vue.reactive([]);
export function toast(msg, kind = 'info') {
  const t = { id: Math.random(), msg, kind };
  toasts.push(t);
  setTimeout(() => toasts.splice(toasts.indexOf(t), 1), 3500);
}

// Run an async action and report errors as a toast.
export async function attempt(fn, okMsg) {
  try {
    const r = await fn();
    if (okMsg) toast(okMsg, 'ok');
    return r;
  } catch (e) {
    toast(e.message, 'error');
  }
}

export const stateLabels = {
  stopped: '已停止', starting: '启动中', running: '运行中', stopping: '停止中', crashed: '已崩溃',
};

export function formatSize(n) {
  if (n < 1024) return n + ' B';
  const units = ['KB', 'MB', 'GB', 'TB'];
  let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
  return n.toFixed(1) + ' ' + units[i];
}
