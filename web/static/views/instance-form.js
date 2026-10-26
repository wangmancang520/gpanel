import { api, attempt } from '../api.js';

// Create/edit form for an instance. New fields go here and in instance.Config.
export const InstanceForm = {
  props: { initial: Object },
  emits: ['saved', 'cancel'],
  setup(props, { emit }) {
    const { reactive, ref, computed, onMounted } = Vue;
    const src = props.initial || {};
    const form = reactive({
      name: src.name || '',
      workDir: src.workDir || '',
      command: src.command || '{{java}} -Xms1G -Xmx2G -jar server.jar nogui',
      stopCommand: src.stopCommand ?? 'stop',
      stopTimeout: src.stopTimeout || 30,
      runtime: src.runtime || '',
      autoStart: !!src.autoStart,
      autoRestart: !!src.autoRestart,
      envText: Object.entries(src.env || {}).map(([k, v]) => `${k}=${v}`).join('\n'),
    });
    const runtimes = ref([]);
    onMounted(async () => { runtimes.value = await attempt(() => api('GET', '/api/runtimes')) || []; });
    // Option text stays short: native dropdowns can't wrap, and a long home
    // path pushed the list off screen. The full path is shown under the select.
    const runtimeLabel = (r) => `${r.name} · ${r.managed ? '面板安装' : '系统'}`;
    const selectedRuntime = computed(() => runtimes.value.find((r) => r.id === form.runtime));

    const save = () => attempt(async () => {
      const env = {};
      for (const line of form.envText.split('\n')) {
        const i = line.indexOf('=');
        if (i > 0) env[line.slice(0, i).trim()] = line.slice(i + 1);
      }
      const body = {
        name: form.name, workDir: form.workDir, command: form.command,
        stopCommand: form.stopCommand, stopTimeout: Number(form.stopTimeout),
        runtime: form.runtime, autoStart: form.autoStart, autoRestart: form.autoRestart, env,
      };
      const saved = src.id
        ? await api('PUT', `/api/instances/${src.id}`, body)
        : await api('POST', '/api/instances', body);
      emit('saved', saved);
    }, '已保存');

    return { form, runtimes, runtimeLabel, selectedRuntime, defaultDir: src.dir, save, isNew: !src.id };
  },
  template: `
    <form class="form" @submit.prevent="save">
      <label>名称<input v-model="form.name" required></label>
      <label>启动命令
        <input v-model="form.command" class="mono" required>
        <small class="muted"><code v-pre>{{java}}</code> 会替换为所选 Java 的路径；不经过 shell，需要脚本时写 <code>bash start.sh</code></small>
      </label>
      <label>运行时
        <select v-model="form.runtime">
          <option value="">系统默认（PATH 中的 java）</option>
          <option v-for="r in runtimes" :key="r.id" :value="r.id">{{ runtimeLabel(r) }}</option>
        </select>
        <small v-if="selectedRuntime" class="muted path">{{ selectedRuntime.home }}</small>
        <small v-else-if="form.runtime" class="error">所选运行时已不存在，请重新选择</small>
      </label>
      <div class="grid2">
        <label>停止命令<input v-model="form.stopCommand" class="mono" placeholder="留空则发送 SIGTERM"></label>
        <label>停止超时（秒）<input v-model="form.stopTimeout" type="number" min="1"></label>
      </div>
      <label>工作目录<input v-model="form.workDir" class="mono" placeholder="留空则使用面板数据目录">
        <small v-if="!form.workDir && defaultDir" class="muted path">当前：{{ defaultDir }}</small>
      </label>
      <label>环境变量（每行 KEY=VALUE）<textarea v-model="form.envText" class="mono" rows="3"></textarea></label>
      <div class="row">
        <label class="check"><input type="checkbox" v-model="form.autoStart"> 面板启动时自动启动</label>
        <label class="check"><input type="checkbox" v-model="form.autoRestart"> 崩溃后自动重启</label>
      </div>
      <div class="row end">
        <button type="button" v-if="isNew" @click="$emit('cancel')">取消</button>
        <button class="primary">{{ isNew ? '创建' : '保存' }}</button>
      </div>
    </form>
  `,
};
