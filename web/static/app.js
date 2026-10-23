import { api, toasts, attempt } from './api.js';
import { LoginView } from './views/login.js';
import { InstanceList } from './views/instances.js';
import { InstanceDetail } from './views/instance.js';
import { RuntimesView } from './views/runtimes.js';
import { PasswordDialog } from './views/password.js';

const { createApp, ref, computed, onMounted } = Vue;

// Hash router: #/instances, #/instances/<id>/<tab>, #/runtimes.
// To add a page: add a nav entry and a branch in `view`.
const nav = [
  { path: 'instances', label: '实例' },
  { path: 'runtimes', label: '运行时' },
];

const App = {
  components: { LoginView, InstanceList, InstanceDetail, RuntimesView, PasswordDialog },
  setup() {
    const user = ref(null);
    const ready = ref(false);
    const hash = ref(location.hash);
    const showPassword = ref(false);
    window.addEventListener('hashchange', () => (hash.value = location.hash));
    window.addEventListener('gpanel:unauthorized', () => (user.value = null));

    const parts = computed(() => hash.value.replace(/^#\/?/, '').split('/').filter(Boolean));
    const section = computed(() => parts.value[0] || 'instances');

    onMounted(async () => {
      try { user.value = (await api('GET', '/api/auth/me')).username; } catch {}
      ready.value = true;
    });

    const logout = () => attempt(async () => {
      await api('POST', '/api/auth/logout');
      user.value = null;
    });

    return { user, ready, nav, parts, section, toasts, logout, showPassword };
  },
  template: `
    <template v-if="ready">
      <LoginView v-if="!user" @login="u => user = u" />
      <div v-else class="layout">
        <aside class="sidebar">
          <div class="brand">GPanel</div>
          <nav>
            <a v-for="n in nav" :key="n.path" :href="'#/' + n.path" :class="{ active: section === n.path }">{{ n.label }}</a>
          </nav>
          <div class="sidebar-foot">
            <span class="muted">{{ user }}</span>
            <button class="link" @click="showPassword = true">修改密码</button>
            <button class="link" @click="logout">退出</button>
          </div>
        </aside>
        <main class="main">
          <InstanceDetail v-if="section === 'instances' && parts[1]" :key="parts[1]" :id="parts[1]" :tab="parts[2] || 'terminal'" />
          <InstanceList v-else-if="section === 'instances'" />
          <RuntimesView v-else-if="section === 'runtimes'" />
        </main>
      </div>
      <PasswordDialog v-if="showPassword" @close="showPassword = false" />
    </template>
    <div class="toasts">
      <div v-for="t in toasts" :key="t.id" class="toast" :class="t.kind">{{ t.msg }}</div>
    </div>
  `,
};

createApp(App).mount('#app');
