import { api } from '../api.js';
import { Logo } from '../icons.js';

export const LoginView = {
  components: { Logo },
  emits: ['login'],
  setup(_, { emit }) {
    const { ref } = Vue;
    const username = ref('admin');
    const password = ref('');
    const error = ref('');
    const busy = ref(false);
    const shake = ref(false);
    async function submit() {
      busy.value = true;
      error.value = '';
      shake.value = false;
      try {
        const r = await api('POST', '/api/auth/login', { username: username.value, password: password.value });
        emit('login', r); // the account: username, role, grants
      } catch (e) {
        error.value = e.message;
        requestAnimationFrame(() => (shake.value = true));
      } finally {
        busy.value = false;
      }
    }
    return { username, password, error, busy, shake, submit };
  },
  template: `
    <div class="login">
      <form class="card login-card" :class="{ shake }" @submit.prevent="submit" @animationend="shake = false">
        <div class="brand"><Logo :size="34" /><span>GPanel</span></div>
        <label>用户名<input v-model="username" autocomplete="username" required></label>
        <label>密码<input v-model="password" type="password" autocomplete="current-password" required autofocus></label>
        <p v-if="error" class="error small row"><Icon name="alert" />{{ error }}</p>
        <button class="primary" :disabled="busy">
          <Icon v-if="busy" name="refresh" class="spin" />{{ busy ? '登录中…' : '登录' }}
        </button>
        <p class="muted small">首次启动的初始密码会打印在面板日志中。</p>
      </form>
    </div>
  `,
};
