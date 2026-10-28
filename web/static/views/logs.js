import { api, q, formatSize } from '../api.js';

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
    const error = ref(''); // why the list or the file couldn't be read
    const box = ref(null);

    const base = computed(() => `/api/instances/${props.id}/logs`);
    const current = computed(() => files.value?.find((f) => f.path === file.value));

    async function loadFiles() {
      try {
        files.value = await api('GET', base.value);
        error.value = '';
      } catch (e) {
        error.value = e.message;
        files.value = files.value || [];
        return;
      }
      if (!file.value && files.value.length) {
        file.value = (files.value.find((f) => f.path === 'logs/latest.log') || files.value[0]).path;
      }
    }

    // Fresh view of the selected file (newest lines), or earlier lines when
    // `older`. A refresh of the same view scrolls smoothly to the new end, so
    // it's visible that new lines arrived; anything else jumps there.
    let seq = 0;
    async function load(older = false, { fresh = false } = {}) {
      if (!file.value) return;
      const my = ++seq;
      if (fresh) { lines.value = []; info.value = null; } // another file: placeholders, not the old file's lines
      loading.value = true;
      const params = { file: file.value, limit: PAGE, level: level.value, q: query.value.trim() };
      if (older && lines.value.length) params.before = lines.value[0].n;
      const el = box.value;
      const keepFrom = older && el ? el.scrollHeight - el.scrollTop : 0;
      const atEnd = el && el.scrollHeight - el.scrollTop - el.clientHeight < 40;
      const smooth = !older && !fresh && atEnd && lines.value.length > 0;
      let page;
      try {
        page = await api('GET', `${base.value}/view?${q(params)}`);
        error.value = '';
      } catch (e) {
        if (my === seq) { error.value = e.message; loading.value = false; }
        return;
      }
      if (my !== seq) return; // a newer request (typing, switching files) won
      loading.value = false;
      lines.value = older ? [...page.lines, ...lines.value] : page.lines;
      info.value = { total: page.total, matched: page.matched, more: page.more };
      await nextTick();
      if (!box.value) return;
      if (older) box.value.scrollTop = box.value.scrollHeight - keepFrom;
      else box.value.scrollTo({ top: box.value.scrollHeight, behavior: smooth ? 'smooth' : 'auto' });
    }

    let timer;
    watch(query, () => { clearTimeout(timer); timer = setTimeout(() => load(), 300); });
    watch(file, () => load(false, { fresh: true }));
    watch(level, () => load());
    onMounted(loadFiles); // picking the first file triggers the load

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
      files, file, level, levels, query, lines, info, loading, error, box, current, downloadUrl, filtered,
      load, refresh, parts, levelOf, formatSize, fmtTime,
    };
  },
  template: `
    <div class="card log-viewer">
      <div class="log-toolbar">
        <select v-model="file" :disabled="!files || !files.length" class="mono">
          <option v-if="!files" value="">正在读取日志列表…</option>
          <option v-else-if="!files.length" value="">没有日志文件</option>
          <option v-for="f in files" :key="f.path" :value="f.path">{{ f.path }}（{{ formatSize(f.size) }}，{{ fmtTime(f.modTime) }}）</option>
        </select>
        <div class="seg">
          <button v-for="l in levels" :key="l.key" :class="{ pressed: level === l.key }" @click="level = l.key">{{ l.label }}</button>
        </div>
        <input v-model="query" placeholder="搜索日志…" class="log-search" spellcheck="false">
        <button @click="refresh" title="刷新"><Icon name="refresh" :class="{ spin: loading }" /></button>
        <a v-if="file" class="button" :href="downloadUrl" title="下载原始文件"><Icon name="download" /></a>
      </div>
      <div class="log-body">
      <div v-if="loading && info" class="loading-bar"></div>
      <div ref="box" class="log-lines mono" :aria-busy="loading">
        <div v-if="info && info.more" class="log-more">
          <button class="link" :disabled="loading" @click="load(true)"><Icon name="arrow" class="up" />加载更早的日志</button>
        </div>
        <div v-if="error && !lines.length" class="empty log-state">
          <Icon name="alert" :size="32" /><p>日志读取失败：{{ error }}</p>
          <button @click="refresh"><Icon name="refresh" />重试</button>
        </div>
        <div v-else-if="files && !files.length" class="empty log-state">
          <Icon name="file" :size="32" />
          <p class="log-state-title">这个实例还没有日志文件</p>
          <p class="small">游戏服务器启动后通常会把日志写到 <code>logs/</code>（例如 Minecraft 的 <code>logs/latest.log</code>），崩溃报告在 <code>crash-reports/</code>。<br>
            没有启动过的实例、或不写日志文件的服务器，这里会是空的；实时输出请看「终端」。</p>
          <div class="row center"><a class="button" :href="'#/instances/' + id + '/terminal'"><Icon name="terminal" />打开终端</a><button @click="refresh"><Icon name="refresh" />重新检查</button></div>
        </div>
        <div v-else-if="!info" class="log-skeleton" aria-label="正在读取日志">
          <div v-for="i in 14" :key="i" class="log-line"><span class="ln"></span><span class="sk-dark" :style="{ width: (30 + (i * 37) % 60) + '%' }"></span></div>
        </div>
        <div v-else-if="!lines.length" class="empty log-state"><Icon name="search" :size="28" /><p>没有匹配的行。</p></div>
        <div v-for="l in lines" :key="l.n" class="log-line" :class="levelOf(l.text)">
          <span class="ln">{{ l.n }}</span><span class="lt"><template v-for="(p, i) in parts(l.text)" :key="i"><mark v-if="p.hit">{{ p.t }}</mark><template v-else>{{ p.t }}</template></template></span>
        </div>
      </div>
      </div>
      <div class="log-status muted small">
        <template v-if="info">共 {{ info.total }} 行<template v-if="filtered">，匹配 {{ info.matched }} 行</template>，显示 {{ lines.length }} 行</template>
        <template v-else-if="loading"><Icon name="refresh" :size="12" class="spin" /> 正在读取 {{ file }}…</template>
        <template v-else-if="!files">正在读取日志列表…</template>
        <template v-else-if="!files.length">没有日志文件</template>
      </div>
    </div>
  `,
};
