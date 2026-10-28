import { api, toasts, attempt, session, isAdmin, refreshSession, roleLabels } from './api.js';
import { Icon, Logo } from './icons.js';
import { DialogHost } from './dialogs.js';
import { LoginView } from './views/login.js';
import { Dashboard } from './views/dashboard.js';
import { InstanceList } from './views/instances.js';
import { InstanceDetail } from './views/instance.js';
import { RuntimesView } from './views/runtimes.js';
import { SecurityView, PasswordCard } from './views/security.js';
import { UsersView } from './views/users.js';

const { createApp, ref, computed, onMounted } = Vue;

// Hash router: #/dashboard, #/instances, #/instances/<id>/<tab>, #/runtimes,
// #/users, #/security. To add a page: add a nav entry and a branch in `view`;
// `admin` pages are hidden from plain users (the server refuses them anyway).
const nav = [
  { path: 'dashboard', label: '概览', icon: 'gauge' },
  { path: 'instances', label: '实例', icon: 'server' },
  { path: 'runtimes', label: '运行时', icon: 'layers', admin: true },
  { path: 'users', label: '用户', icon: 'users', admin: true },
  { path: 'security', label: '安全', icon: 'shield' },
];

const App = {
  components: { LoginView, Dashboard, InstanceList, InstanceDetail, RuntimesView, SecurityView, UsersView, PasswordCard, Logo, DialogHost },
  setup() {
    const ready = ref(false);
    const hash = ref(location.hash);
    window.addEventListener('hashchange', () => (hash.value = location.hash));
    window.addEventListener('gpanel:unauthorized', () => (session.user = null));

    const parts = computed(() => hash.value.replace(/^#\/?/, '').split('/').filter(Boolean));
    const section = computed(() => parts.value[0] || 'dashboard');
    const visibleNav = computed(() => nav.filter((n) => !n.admin || isAdmin()));
    const allowed = computed(() => visibleNav.value.some((n) => n.path === section.value));

    // Grants and roles can change while the page is open; keep them fresh.
    onMounted(async () => {
      await refreshSession();
      ready.value = true;
      setInterval(() => session.user && refreshSession(), 20000);
    });

    const logout = () => attempt(async () => {
      await api('POST', '/api/auth/logout');
      session.user = null;
    });

    // Re-key the main view per page so route changes animate; tab switches
    // inside an instance keep the same key (the tab pane animates itself).
    const viewKey = computed(() => section.value + '/' + (parts.value[1] || ''));
    const toastIcon = { ok: 'check', error: 'alert', info: 'info' };

    return { session, ready, visibleNav, allowed, parts, section, viewKey, toasts, toastIcon, logout, roleLabels };
  },
  template: `
    <template v-if="ready">
      <Transition name="fade" mode="out-in">
      <LoginView v-if="!session.user" @login="u => session.user = u" />
      <div v-else class="layout">
        <aside class="sidebar">
          <div class="brand"><Logo /><span>GPanel</span></div>
          <nav>
            <a v-for="n in visibleNav" :key="n.path" :href="'#/' + n.path" :class="{ active: section === n.path }" :title="n.label" :aria-current="section === n.path ? 'page' : null">
              <Icon :name="n.icon" :size="18" />{{ n.label }}
            </a>
          </nav>
          <div class="sidebar-foot">
            <span class="avatar">{{ session.user.username.slice(0, 1) }}</span>
            <span class="user ellipsis" :title="roleLabels[session.user.role]">{{ session.user.username }}<small class="muted">{{ roleLabels[session.user.role] }}</small></span>
            <a class="icon-link" href="#/security" title="修改密码" aria-label="修改密码"><Icon name="key" :size="18" /></a>
            <button class="link" @click="logout" title="退出" aria-label="退出"><Icon name="logout" :size="18" /></button>
          </div>
        </aside>
        <main class="main" v-if="!session.user.mustChangePassword">
          <Transition name="page" mode="out-in">
            <div v-if="!allowed" :key="viewKey" class="page"><div class="card empty"><Icon name="lock" :size="36" /><p>没有权限访问这个页面。</p></div></div>
            <Dashboard v-else-if="section === 'dashboard'" :key="viewKey" />
            <InstanceDetail v-else-if="section === 'instances' && parts[1]" :key="viewKey" :id="parts[1]" :tab="parts[2] || 'terminal'" />
            <InstanceList v-else-if="section === 'instances'" :key="viewKey" />
            <RuntimesView v-else-if="section === 'runtimes'" :key="viewKey" />
            <UsersView v-else-if="section === 'users'" :key="viewKey" />
            <SecurityView v-else-if="section === 'security'" :key="viewKey" :user="session.user.username" />
          </Transition>
        </main>
      </div>
      </Transition>
    </template>
    <Transition name="modal">
      <div v-if="session.user && session.user.mustChangePassword" class="modal-backdrop">
        <div class="modal wide force-password">
          <p class="notice"><Icon name="key" />这个账号的密码是别人设置的，请先设置你自己的密码再继续使用。</p>
          <PasswordCard :user="session.user.username" @changed="session.user.mustChangePassword = false" />
          <div class="row end"><button class="link" @click="logout"><Icon name="logout" />退出登录</button></div>
        </div>
      </div>
    </Transition>
    <DialogHost />
    <TransitionGroup name="toast" tag="div" class="toasts" aria-live="polite">
      <div v-for="t in toasts" :key="t.id" class="toast" :class="t.kind">
        <Icon :name="toastIcon[t.kind] || 'info'" />{{ t.msg }}
      </div>
    </TransitionGroup>
  `,
};

createApp(App)
  .component('Icon', Icon)
  .directive('focus', { mounted: (el) => el.focus() })
  .mount('#app');
