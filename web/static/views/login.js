import { api } from '../api.js';

export const LoginView = {
  emits: ['login'],
  setup(_, { emit }) {
    const { ref } = Vue;
    const username = ref('admin');
    const password = ref('');
    const error = ref('');
    const busy = ref(false);
    async function submit() {
      busy.value = true;
      error.value = '';
      try {
        const r = await api('POST', '/api/auth/login', { username: username.value, password: password.value });
        emit('login', r.username);
      } catch (e) {
        error.value = e.message;
      } finally {
        busy.value = false;
      }
    }
    return { username, password, error, busy, submit };
  },
  template: `
    <div class="login">
      <form class="card login-card" @submit.prevent="submit">
        <h1>GPanel</h1>
        <label>用户名<input v-model="username" autocomplete="username" required></label>
        <label>密码<input v-model="password" type="password" autocomplete="current-password" required autofocus></label>
        <p v-if="error" class="error">{{ error }}</p>
        <button class="primary" :disabled="busy">登录</button>
        <p class="muted small">首次启动的初始密码会打印在面板日志中。</p>
      </form>
    </div>
  `,
};
