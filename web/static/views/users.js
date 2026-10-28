import { api, attempt, toast, session, isSuper, roleLabels } from '../api.js';
import { confirmAction } from '../dialogs.js';

// Panel accounts (not game whitelists). The server decides who may change
// whom; `manageable` on each row reflects that for the current viewer.

const fmtTime = (t) => (t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '从未登录');

// Per-instance grants for a plain user: { [instanceId]: { console, files } }.
const GrantEditor = {
  props: { modelValue: Object, instances: Array },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    const get = (id) => props.modelValue[id] || { console: false, files: false };
    const set = (id, key, on) => {
      const next = { ...props.modelValue, [id]: { ...get(id), [key]: on } };
      if (!next[id].console && !next[id].files) delete next[id];
      emit('update:modelValue', next);
    };
    return { get, set };
  },
  template: `
    <div class="grant-list">
      <p v-if="!instances.length" class="muted small">还没有实例。</p>
      <div v-for="it in instances" :key="it.id" class="grant-row">
        <span class="ellipsis"><Icon name="server" />{{ it.name }}</span>
        <label class="check"><input type="checkbox" :checked="get(it.id).console" @change="set(it.id, 'console', $event.target.checked)"> 控制台与启停</label>
        <label class="check"><input type="checkbox" :checked="get(it.id).files" @change="set(it.id, 'files', $event.target.checked)"> 文件管理</label>
      </div>
    </div>
  `,
};

export const UsersView = {
  components: { GrantEditor },
  setup() {
    const { ref, reactive, computed, onMounted } = Vue;
    const users = ref([]);
    const instances = ref([]);
    const loaded = ref(false);
    const editing = ref(null); // { mode: 'create' | 'edit', username, role, password, instances }
    const revealed = ref(null); // { username, password } shown once
    const busy = ref(false);

    const load = async () => {
      const [u, i] = await Promise.all([
        api('GET', '/api/users').catch((e) => { toast(e.message, 'error'); return null; }),
        api('GET', '/api/instances').catch(() => []),
      ]);
      if (u) users.value = u;
      instances.value = i;
      loaded.value = true;
    };
    onMounted(load);

    const instanceName = (id) => instances.value.find((i) => i.id === id)?.name || id;
    const grantSummary = (u) => {
      if (u.role !== 'user') return '全部实例';
      const ids = Object.keys(u.instances);
      if (!ids.length) return '无';
      return ids.map(instanceName).join('、');
    };
    const roleChoices = computed(() => (isSuper() ? ['user', 'admin'] : ['user']));

    const openCreate = () => {
      editing.value = reactive({ mode: 'create', username: '', role: 'user', password: '', instances: {} });
    };
    const openEdit = (u) => {
      editing.value = reactive({ mode: 'edit', username: u.username, role: u.role, instances: { ...u.instances } });
    };
    const save = async () => {
      const e = editing.value;
      busy.value = true;
      const body = { role: e.role, instances: e.role === 'user' ? e.instances : {} };
      const r = await attempt(() => e.mode === 'create'
        ? api('POST', '/api/users', { ...body, username: e.username.trim(), password: e.password })
        : api('PUT', `/api/users/${encodeURIComponent(e.username)}`, body));
      busy.value = false;
      if (!r) return;
      editing.value = null;
      if (e.mode === 'create') {
        if (r.password) revealed.value = { username: r.user.username, password: r.password, created: true };
        else toast(`已创建 ${r.user.username}，首次登录需修改密码`, 'ok');
      } else toast('已保存', 'ok');
      load();
    };

    const toggleDisabled = async (u) => {
      if (!u.disabled && !(await confirmAction(`停用 ${u.username}？`, '停用后该账号立即退出登录，且无法再登录，直到重新启用。', { okText: '停用', danger: true }))) return;
      await attempt(() => api('PUT', `/api/users/${encodeURIComponent(u.username)}`, { disabled: !u.disabled }), u.disabled ? '已启用' : '已停用');
      load();
    };
    const resetPassword = async (u) => {
      if (!(await confirmAction(`重置 ${u.username} 的密码？`, '将生成一个随机密码，该账号的登录会话会被退出，下次登录后需要修改密码。', { okText: '重置' }))) return;
      const r = await attempt(() => api('POST', `/api/users/${encodeURIComponent(u.username)}/reset-password`));
      if (r) revealed.value = { username: u.username, password: r.password };
      load();
    };
    const remove = async (u) => {
      if (!(await confirmAction(`删除 ${u.username}？`, '只删除面板账号，不影响任何实例和文件。此操作无法撤销。', { okText: '删除', danger: true }))) return;
      await attempt(() => api('DELETE', `/api/users/${encodeURIComponent(u.username)}`), '已删除');
      load();
    };
    const copy = (text) => navigator.clipboard?.writeText(text).then(() => toast('已复制', 'ok'));

    return {
      users, instances, loaded, editing, revealed, busy, session, roleLabels, roleChoices, isSuper,
      grantSummary, fmtTime, openCreate, openEdit, save, toggleDisabled, resetPassword, remove, copy,
    };
  },
  template: `
    <div class="page">
      <header class="page-head">
        <h1>用户</h1>
        <button class="primary" @click="openCreate"><Icon name="plus" />新建用户</button>
      </header>
      <p class="notice small"><Icon name="info" />这里管理的是可以登录本面板的账号，不是游戏服务器的白名单。
        普通用户只能看到并操作分配给他的实例；<strong>「文件管理」权限可以修改服务器文件和模组，等同于信任对方不会作恶</strong>，请只授予可信的人。</p>
      <div class="card rise table-wrap">
        <table class="table">
          <thead><tr><th>用户名</th><th>角色</th><th>状态</th><th>可访问的实例</th><th>最后登录</th><th></th></tr></thead>
          <tbody>
            <template v-if="!loaded">
              <tr v-for="i in 3" :key="'sk' + i" class="sk-row">
                <td><span class="skeleton sk-line" style="width: 80px"></span></td><td><span class="skeleton sk-pill"></span></td>
                <td><span class="skeleton sk-pill"></span></td><td><span class="skeleton sk-line" style="width: 96px"></span></td>
                <td><span class="skeleton sk-line" style="width: 72px"></span></td><td></td>
              </tr>
            </template>
            <tr v-for="u in users" :key="u.username" :class="{ dim: u.disabled }">
              <td class="nowrap"><span class="avatar sm">{{ u.username.slice(0, 1) }}</span>{{ u.username }}
                <span v-if="u.username === session.user.username" class="muted small">（你）</span></td>
              <td><span class="badge" :class="'role-' + u.role">{{ roleLabels[u.role] }}</span></td>
              <td class="nowrap">
                <span v-if="u.disabled" class="badge crashed">已停用</span>
                <span v-else-if="u.mustChangePassword" class="badge starting">待改密码</span>
                <span v-else class="badge running">正常</span>
              </td>
              <td class="ellipsis grant-cell" :title="grantSummary(u)">{{ grantSummary(u) }}</td>
              <td class="muted small nowrap">{{ fmtTime(u.lastLogin) }}</td>
              <td class="actions nowrap">
                <template v-if="u.manageable">
                  <a href="" @click.prevent="openEdit(u)" title="角色与权限" aria-label="角色与权限"><Icon name="sliders" :size="16" /></a>
                  <a href="" @click.prevent="resetPassword(u)" title="重置密码" aria-label="重置密码"><Icon name="key" :size="16" /></a>
                  <a href="" @click.prevent="toggleDisabled(u)" :title="u.disabled ? '启用' : '停用'" :aria-label="u.disabled ? '启用' : '停用'"><Icon :name="u.disabled ? 'play' : 'stop'" :size="16" /></a>
                  <a href="" class="danger" @click.prevent="remove(u)" title="删除" aria-label="删除"><Icon name="trash" :size="16" /></a>
                </template>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <Transition name="modal">
        <div v-if="editing" class="modal-backdrop" @click.self="editing = null" @keydown.esc="editing = null">
          <form class="card modal wide" @submit.prevent="save">
            <h2>{{ editing.mode === 'create' ? '新建用户' : '编辑 ' + editing.username }}</h2>
            <label v-if="editing.mode === 'create'">用户名
              <input v-model="editing.username" v-focus required pattern="[A-Za-z0-9_.\\-]{2,32}" placeholder="2–32 位字母、数字或 _ . -" autocomplete="off">
            </label>
            <label>角色
              <select v-model="editing.role" :disabled="roleChoices.length < 2">
                <option v-for="r in roleChoices" :key="r" :value="r">{{ roleLabels[r] }}</option>
              </select>
              <small class="muted" v-if="editing.role === 'admin'">管理员可以访问全部实例、新建实例、管理 Java 运行时，并管理普通用户。</small>
              <small class="muted" v-else-if="!isSuper()">只有超级管理员可以任命管理员。</small>
            </label>
            <label v-if="editing.mode === 'create'">初始密码
              <input v-model="editing.password" type="text" minlength="8" placeholder="留空则自动生成" autocomplete="new-password" class="mono">
              <small class="muted">首次登录后必须修改密码。</small>
            </label>
            <div v-if="editing.role === 'user'">
              <h3 class="small">可访问的实例</h3>
              <GrantEditor v-model="editing.instances" :instances="instances" />
              <small class="muted">实例的启动命令、Java 等设置始终只有管理员可以修改。</small>
            </div>
            <div class="row end">
              <button type="button" @click="editing = null">取消</button>
              <button class="primary" :disabled="busy"><Icon name="check" />{{ editing.mode === 'create' ? '创建' : '保存' }}</button>
            </div>
          </form>
        </div>
      </Transition>

      <Transition name="modal">
        <div v-if="revealed" class="modal-backdrop">
          <div class="card modal dialog">
            <h2>{{ revealed.created ? '已创建 ' : '' }}{{ revealed.username }} 的密码</h2>
            <p class="muted">只显示这一次，请复制后交给对方。对方首次登录后必须修改密码。</p>
            <div class="row"><code class="secret">{{ revealed.password }}</code>
              <button type="button" @click="copy(revealed.password)"><Icon name="copy" />复制</button></div>
            <div class="row end"><button class="primary" @click="revealed = null">完成</button></div>
          </div>
        </div>
      </Transition>
    </div>
  `,
};
