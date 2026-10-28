import { api, attempt, stateLabels, isAdmin, grant } from '../api.js';
import { TerminalPane } from './terminal.js';
import { FileManager } from './files.js';
import { InstanceForm } from './instance-form.js';

// Instance detail page. Add a tab: one entry in `tabs` + one v-if branch;
// `can` says who sees it (the server enforces the same rules).
const tabs = [
  { key: 'terminal', label: '终端', icon: 'terminal', can: (g) => g.console },
  { key: 'files', label: '文件', icon: 'folder', can: (g) => g.files },
  { key: 'settings', label: '设置', icon: 'sliders', can: () => isAdmin() },
];

export const InstanceDetail = {
  components: { TerminalPane, FileManager, InstanceForm },
  props: { id: String, tab: String },
  setup(props) {
    const { ref, computed, watchEffect, onMounted, onUnmounted } = Vue;
    const inst = ref(null);
    const deleteFiles = ref(false);
    const load = async () => {
      try { inst.value = await api('GET', `/api/instances/${props.id}`); }
      catch (e) { if (!inst.value) location.hash = '#/instances'; }
    };
    let timer;
    onMounted(() => { load(); timer = setInterval(load, 5000); });
    onUnmounted(() => clearInterval(timer));

    const act = (action) => attempt(async () => {
      inst.value = await api('POST', `/api/instances/${props.id}/${action}`);
    });
    const remove = () => {
      if (!confirm(`确定删除实例「${inst.value.name}」？`)) return;
      attempt(async () => {
        await api('DELETE', `/api/instances/${props.id}?files=${deleteFiles.value ? 1 : 0}`);
        location.hash = '#/instances';
      }, '已删除');
    };
    const onStatus = (s) => { inst.value = s; };
    const running = () => inst.value && ['running', 'starting', 'stopping'].includes(inst.value.state);
    const g = computed(() => grant(props.id));
    const visibleTabs = computed(() => tabs.filter((t) => t.can(g.value)));
    // A tab the account can't use (e.g. the default terminal for files-only
    // users) jumps to the first one it can.
    watchEffect(() => {
      const keys = visibleTabs.value.map((t) => t.key);
      if (keys.length && !keys.includes(props.tab)) location.replace(`#/instances/${props.id}/${keys[0]}`);
    });
    return { inst, tabs: visibleTabs, g, isAdmin, act, remove, onStatus, running, deleteFiles, stateLabels, load };
  },
  template: `
    <div class="page detail" v-if="inst">
      <header class="page-head">
        <div class="row">
          <a href="#/instances" class="crumb-link">实例</a><span class="muted">/</span>
          <h1>{{ inst.name }}</h1>
          <span class="badge" :class="inst.state">{{ stateLabels[inst.state] }}</span>
          <span v-if="inst.pid" class="muted small">PID {{ inst.pid }}</span>
        </div>
        <div class="row" v-if="g.console">
          <button v-if="!running()" class="primary" @click="act('start')"><Icon name="play" />启动</button>
          <button v-if="running()" @click="act('stop')" :disabled="inst.state === 'stopping'"><Icon name="stop" />停止</button>
          <button @click="act('restart')"><Icon name="restart" />重启</button>
          <button v-if="running()" class="danger" @click="act('kill')"><Icon name="power" />强制结束</button>
        </div>
      </header>
      <p v-if="inst.lastError" class="error row"><Icon name="alert" />{{ inst.lastError }}</p>
      <nav class="tabs">
        <a v-for="t in tabs" :key="t.key" :href="'#/instances/' + id + '/' + t.key" :class="{ active: tab === t.key }">
          <Icon :name="t.icon" />{{ t.label }}
        </a>
      </nav>
      <Transition name="page" mode="out-in">
      <TerminalPane v-if="tab === 'terminal' && g.console" :id="id" @status="onStatus" />
      <FileManager v-else-if="tab === 'files' && g.files" :id="id" />
      <div v-else-if="tab === 'settings' && isAdmin()" class="card">
        <InstanceForm :initial="inst" :key="inst.id" @saved="load" />
        <hr>
        <div class="row">
          <label class="check"><input type="checkbox" v-model="deleteFiles"> 同时删除实例目录（仅限面板默认目录）</label>
          <button class="danger" :disabled="running()" @click="remove"><Icon name="trash" />删除实例</button>
        </div>
      </div>
      </Transition>
    </div>
  `,
};
