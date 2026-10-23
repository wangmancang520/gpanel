import { api, attempt } from '../api.js';

export const PasswordDialog = {
  emits: ['close'],
  setup(_, { emit }) {
    const { ref } = Vue;
    const oldPassword = ref('');
    const newPassword = ref('');
    const submit = () => attempt(async () => {
      await api('POST', '/api/auth/password', { oldPassword: oldPassword.value, newPassword: newPassword.value });
      emit('close');
    }, '密码已修改');
    return { oldPassword, newPassword, submit };
  },
  template: `
    <div class="modal-backdrop" @click.self="$emit('close')">
      <form class="card modal" @submit.prevent="submit">
        <h2>修改密码</h2>
        <label>原密码<input v-model="oldPassword" type="password" required></label>
        <label>新密码（至少 8 位）<input v-model="newPassword" type="password" minlength="8" required></label>
        <div class="row end">
          <button type="button" @click="$emit('close')">取消</button>
          <button class="primary">保存</button>
        </div>
      </form>
    </div>
  `,
};
