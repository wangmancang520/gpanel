import { api, attempt, stateLabels, isAdmin, grant } from '../api.js';
import { InstanceForm } from './instance-form.js';

export const InstanceList = {
  components: { InstanceForm },
  setup() {
    const { ref, onMounted, onUnmounted } = Vue;
    const items = ref([]);
    const loaded = ref(false);
    const creating = ref(false);
    const load = async () => {
      items.value = await attempt(() => api('GET', '/api/instances')) || items.value;
      loaded.value = true;
    };
    let timer;
    onMounted(() => { load(); timer = setInterval(load, 3000); });
    onUnmounted(() => clearInterval(timer));

    const act = (inst, action) => attempt(async () => {
      await api('POST', `/api/instances/${inst.id}/${action}`);
      await load();
    });
    const created = (cfg) => { creating.value = false; location.hash = `#/instances/${cfg.id}`; };
    return { items, loaded, creating, act, created, stateLabels, isAdmin, grant };
  },
  template: `
    <div class="page">
      <header class="page-head">
        <h1>实例</h1>
        <button v-if="isAdmin()" class="primary" @click="creating = true"><Icon name="plus" />新建实例</button>
      </header>
      <div v-if="loaded && !items.length" class="card empty rise">
        <Icon name="server" :size="36" /><p v-if="isAdmin()">还没有实例。点击「新建实例」开始。</p><p v-else>还没有分配给你的实例，请联系管理员。</p>
      </div>
      <div class="cards">
        <div v-for="(it, i) in items" :key="it.id" class="card instance-card rise" :class="it.state" :style="{ '--i': i }">
          <a :href="'#/instances/' + it.id" class="instance-name"><Icon name="server" />{{ it.name }}</a>
          <span class="badge" :class="it.state">{{ stateLabels[it.state] }}</span>
          <code class="muted ellipsis">{{ it.command }}</code>
          <div class="row">
            <template v-if="grant(it.id).console">
              <button v-if="it.state === 'stopped' || it.state === 'crashed'" @click="act(it, 'start')"><Icon name="play" />启动</button>
              <button v-else @click="act(it, 'stop')" :disabled="it.state === 'stopping'"><Icon name="stop" />停止</button>
              <a class="button" :href="'#/instances/' + it.id"><Icon name="terminal" />控制台</a>
            </template>
            <a v-else class="button" :href="'#/instances/' + it.id + '/files'"><Icon name="folder" />文件</a>
          </div>
        </div>
      </div>
      <Transition name="modal">
        <div v-if="creating" class="modal-backdrop" @click.self="creating = false" @keydown.esc="creating = false">
          <div class="card modal wide">
            <h2>新建实例</h2>
            <InstanceForm @saved="created" @cancel="creating = false" />
          </div>
        </div>
      </Transition>
    </div>
  `,
};
