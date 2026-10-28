import { api, attempt, q, formatSize } from '../api.js';

// Log files of an instance (logs/, crash-reports/, rotated .log.gz):
// pick a file, filter by level or text, read from the end and page back.
// Served by internal/serverlogs; the live console only keeps recent output.

const PAGE = 500;
const levels = [
  { key: '', label: '全部' },
  { key: 'warn', label: '警告及错误' },
  { key: 'error', label: '仅错误' },
];
const levelOf = (t) => (/\/(ERROR|FATAL)\]|\b(ERROR|FATAL|SEVERE)\b|Exception\b|^\s+at [\w.$]+\(/i.test(t) ? 'err'
  : /\/WARN\]|\bWARN(ING)?\b/i.test(t) ? 'warn' : '');
const fmtTime = (t) => new Date(t).toLocaleString('zh-CN', { hour12: false });

export const LogViewer = {
  props: { id: String },
  setup(props) {
    const { ref, computed, watch, nextTick, onMounted } = Vue;
    const files = ref(null);
    const file = ref('');
    const level = ref('');
    const query = ref('');
    const lines = ref([]);
    const info = ref(null); // { total, matched, more }
    const loading = ref(false);
    const box = ref(null);

    const base = computed(() => `/api/instances/${props.id}/logs`);
    const current = computed(() => files.value?.find((f) => f.path === file.value));

    async function loadFiles() {
      files.value = (await attempt(() => api('GET', base.value))) || [];
      if (!file.value && files.value.length) {
        file.value = (files.value.find((f) => f.path === 'logs/latest.log') || files.value[0]).path;
      }
    }

    // Fresh view of the selected file (newest lines), or earlier lines when `older`.
    async function load(older = false) {
      if (!file.value) return;
      loading.value = true;
      const params = { file: file.value, limit: PAGE, level: level.value, q: query.value.trim() };
      if (older && lines.value.length) params.before = lines.value[0].n;
      const keepFrom = older && box.value ? box.value.scrollHeight - box.value.scrollTop : 0;
      const page = await attempt(() => api('GET', `${base.value}/view?${q(params)}`));
      loading.value = false;
      if (!page) return;
      lines.value = older ? [...page.lines, ...lines.value] : page.lines;
      info.value = { total: page.total, matched: page.matched, more: page.more };
      await nextTick();
      if (!box.value) return;
      box.value.scrollTop = older ? box.value.scrollHeight - keepFrom : box.value.scrollHeight;
    }

    let timer;
    watch(query, () => { clearTimeout(timer); timer = setTimeout(() => load(), 300); });
    watch([file, level], () => load());
    onMounted(async () => { await loadFiles(); load(); });

    const refresh = async () => { await loadFiles(); load(); };
    const downloadUrl = computed(() => `${base.value}/download?${q({ file: file.value })}`);
    const filtered = computed(() => level.value || query.value.trim());
    // Highlight the search term (escaped; the line itself is rendered as text).
    const parts = (text) => {
      const term = query.value.trim();
      if (!term) return [{ t: text }];
      const out = [];
      const lower = text.toLowerCase(), needle = term.toLowerCase();
      let i = 0;
      for (let j; (j = lower.indexOf(needle, i)) >= 0; i = j + needle.length) {
        if (j > i) out.push({ t: text.slice(i, j) });
        out.push({ t: text.slice(j, j + needle.length), hit: true });
      }
      if (i < text.length) out.push({ t: text.slice(i) });
      return out;
    };

    return {
      files, file, level, levels, query, lines, info, loading, box, current, downloadUrl, filtered,
      load, refresh, parts, levelOf, formatSize, fmtTime,
    };
  },
  template: `
    <div class="card log-viewer">
      <div class="log-toolbar">
        <select v-model="file" :disabled="!files || !files.length" class="mono">
          <option v-if="files && !files.length" value="">没有日志文件</option>
          <option v-for="f in files" :key="f.path" :value="f.path">{{ f.path }}（{{ formatSize(f.size) }}，{{ fmtTime(f.modTime) }}）</option>
        </select>
        <div class="seg">
          <button v-for="l in levels" :key="l.key" :class="{ pressed: level === l.key }" @click="level = l.key">{{ l.label }}</button>
        </div>
        <input v-model="query" placeholder="搜索日志…" class="log-search" spellcheck="false">
        <button @click="refresh" title="刷新"><Icon name="refresh" :class="{ spin: loading }" /></button>
        <a v-if="file" class="button" :href="downloadUrl" title="下载原始文件"><Icon name="download" /></a>
      </div>
      <div ref="box" class="log-lines mono">
        <div v-if="info && info.more" class="log-more">
          <button class="link" :disabled="loading" @click="load(true)"><Icon name="arrow" class="up" />加载更早的日志</button>
        </div>
        <div v-if="files && !files.length" class="empty"><Icon name="file" :size="32" /><p>这个实例还没有 logs/ 或 crash-reports/ 日志。</p></div>
        <div v-else-if="info && !lines.length" class="empty"><p>没有匹配的行。</p></div>
        <div v-for="l in lines" :key="l.n" class="log-line" :class="levelOf(l.text)">
          <span class="ln">{{ l.n }}</span><span class="lt"><template v-for="(p, i) in parts(l.text)" :key="i"><mark v-if="p.hit">{{ p.t }}</mark><template v-else>{{ p.t }}</template></template></span>
        </div>
      </div>
      <div class="log-status muted small" v-if="info">
        共 {{ info.total }} 行<template v-if="filtered">，匹配 {{ info.matched }} 行</template>，显示 {{ lines.length }} 行
      </div>
    </div>
  `,
};
