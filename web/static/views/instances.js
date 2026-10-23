import { api, attempt, stateLabels } from '../api.js';
import { InstanceForm } from './instance-form.js';

export const InstanceList = {
  components: { InstanceForm },
  setup() {
    const { ref, onMounted, onUnmounted } = Vue;
    const items = ref([]);
    const creating = ref(false);
    const load = async () => { items.value = await attempt(() => api('GET', '/api/instances')) || items.value; };
    let timer;
    onMounted(() => { load(); timer = setInterval(load, 3000); });
    onUnmounted(() => clearInterval(timer));

    const act = (inst, action) => attempt(async () => {
      await api('POST', `/api/instances/${inst.id}/${action}`);
      await load();
    });
    const created = (cfg) => { creating.value = false; location.hash = `#/instances/${cfg.id}`; };
    return { items, creating, act, created, stateLabels };
  },
  template: `
    <div class="page">
      <header class="page-head">
        <h1>实例</h1>
        <button class="primary" @click="creating = true">新建实例</button>
      </header>
      <p v-if="!items.length" class="empty">还没有实例。点击「新建实例」开始。</p>
      <div class="cards">
        <div v-for="it in items" :key="it.id" class="card instance-card">
          <a :href="'#/instances/' + it.id" class="instance-name">{{ it.name }}</a>
          <span class="badge" :class="it.state">{{ stateLabels[it.state] }}</span>
          <code class="muted ellipsis">{{ it.command }}</code>
          <div class="row">
            <button v-if="it.state === 'stopped' || it.state === 'crashed'" @click="act(it, 'start')">启动</button>
            <button v-else @click="act(it, 'stop')">停止</button>
            <a class="button" :href="'#/instances/' + it.id">控制台</a>
          </div>
        </div>
      </div>
      <div v-if="creating" class="modal-backdrop" @click.self="creating = false">
        <div class="card modal wide">
          <h2>新建实例</h2>
          <InstanceForm @saved="created" @cancel="creating = false" />
        </div>
      </div>
    </div>
  `,
};
