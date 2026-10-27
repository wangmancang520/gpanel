import { api, toasts, attempt } from './api.js';
import { Icon, Logo } from './icons.js';
import { DialogHost } from './dialogs.js';
import { LoginView } from './views/login.js';
import { Dashboard } from './views/dashboard.js';
import { InstanceList } from './views/instances.js';
import { InstanceDetail } from './views/instance.js';
import { RuntimesView } from './views/runtimes.js';
import { SecurityView } from './views/security.js';

const { createApp, ref, computed, onMounted } = Vue;

// Hash router: #/dashboard, #/instances, #/instances/<id>/<tab>, #/runtimes, #/security.
// To add a page: add a nav entry and a branch in `view`.
const nav = [
  { path: 'dashboard', label: '概览', icon: 'gauge' },
  { path: 'instances', label: '实例', icon: 'server' },
  { path: 'runtimes', label: '运行时', icon: 'layers' },
  { path: 'security', label: '安全', icon: 'shield' },
];

const App = {
  components: { LoginView, Dashboard, InstanceList, InstanceDetail, RuntimesView, SecurityView, Logo, DialogHost },
  setup() {
    const user = ref(null);
    const ready = ref(false);
    const hash = ref(location.hash);
    window.addEventListener('hashchange', () => (hash.value = location.hash));
    window.addEventListener('gpanel:unauthorized', () => (user.value = null));

    const parts = computed(() => hash.value.replace(/^#\/?/, '').split('/').filter(Boolean));
    const section = computed(() => parts.value[0] || 'dashboard');

    onMounted(async () => {
      try { user.value = (await api('GET', '/api/auth/me')).username; } catch {}
      ready.value = true;
    });

    const logout = () => attempt(async () => {
      await api('POST', '/api/auth/logout');
      user.value = null;
    });

    // Re-key the main view per page so route changes animate; tab switches
    // inside an instance keep the same key (the tab pane animates itself).
    const viewKey = computed(() => section.value + '/' + (parts.value[1] || ''));
    const toastIcon = { ok: 'check', error: 'alert', info: 'info' };

    return { user, ready, nav, parts, section, viewKey, toasts, toastIcon, logout };
  },
  template: `
    <template v-if="ready">
      <Transition name="fade" mode="out-in">
      <LoginView v-if="!user" @login="u => user = u" />
      <div v-else class="layout">
        <aside class="sidebar">
          <div class="brand"><Logo /><span>GPanel</span></div>
          <nav>
            <a v-for="n in nav" :key="n.path" :href="'#/' + n.path" :class="{ active: section === n.path }" :title="n.label" :aria-current="section === n.path ? 'page' : null">
              <Icon :name="n.icon" :size="18" />{{ n.label }}
            </a>
          </nav>
          <div class="sidebar-foot">
            <span class="avatar">{{ user.slice(0, 1) }}</span>
            <span class="user ellipsis">{{ user }}</span>
            <a class="icon-link" href="#/security" title="修改密码" aria-label="修改密码"><Icon name="key" :size="18" /></a>
            <button class="link" @click="logout" title="退出" aria-label="退出"><Icon name="logout" :size="18" /></button>
          </div>
        </aside>
        <main class="main">
          <Transition name="page" mode="out-in">
            <Dashboard v-if="section === 'dashboard'" :key="viewKey" />
            <InstanceDetail v-else-if="section === 'instances' && parts[1]" :key="viewKey" :id="parts[1]" :tab="parts[2] || 'terminal'" />
            <InstanceList v-else-if="section === 'instances'" :key="viewKey" />
            <RuntimesView v-else-if="section === 'runtimes'" :key="viewKey" />
            <SecurityView v-else-if="section === 'security'" :key="viewKey" :user="user" />
          </Transition>
        </main>
      </div>
      </Transition>
    </template>
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
