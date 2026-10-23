import { api, attempt, stateLabels } from '../api.js';
import { TerminalPane } from './terminal.js';
import { FileManager } from './files.js';
import { InstanceForm } from './instance-form.js';

// Instance detail page. Add a tab: one entry in `tabs` + one v-if branch.
const tabs = [
  { key: 'terminal', label: '终端' },
  { key: 'files', label: '文件' },
  { key: 'settings', label: '设置' },
];

export const InstanceDetail = {
  components: { TerminalPane, FileManager, InstanceForm },
  props: { id: String, tab: String },
  setup(props) {
    const { ref, onMounted, onUnmounted } = Vue;
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
    return { inst, tabs, act, remove, onStatus, running, deleteFiles, stateLabels, load };
  },
  template: `
    <div class="page detail" v-if="inst">
      <header class="page-head">
        <div class="row">
          <a href="#/instances" class="muted">实例</a><span class="muted">/</span>
          <h1>{{ inst.name }}</h1>
          <span class="badge" :class="inst.state">{{ stateLabels[inst.state] }}</span>
          <span v-if="inst.pid" class="muted small">PID {{ inst.pid }}</span>
        </div>
        <div class="row">
          <button v-if="!running()" class="primary" @click="act('start')">启动</button>
          <button v-if="running()" @click="act('stop')">停止</button>
          <button @click="act('restart')">重启</button>
          <button v-if="running()" class="danger" @click="act('kill')">强制结束</button>
        </div>
      </header>
      <p v-if="inst.lastError" class="error">{{ inst.lastError }}</p>
      <nav class="tabs">
        <a v-for="t in tabs" :key="t.key" :href="'#/instances/' + id + '/' + t.key" :class="{ active: tab === t.key }">{{ t.label }}</a>
      </nav>
      <TerminalPane v-if="tab === 'terminal'" :id="id" @status="onStatus" />
      <FileManager v-else-if="tab === 'files'" :id="id" />
      <div v-else-if="tab === 'settings'" class="card">
        <InstanceForm :initial="inst" :key="inst.id" @saved="load" />
        <hr>
        <div class="row">
          <label class="check"><input type="checkbox" v-model="deleteFiles"> 同时删除实例目录（仅限面板默认目录）</label>
          <button class="danger" :disabled="running()" @click="remove">删除实例</button>
        </div>
      </div>
    </div>
  `,
};
