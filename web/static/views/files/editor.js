import { api, attempt, q, toast } from '../../api.js';
import { confirmAction } from '../../dialogs.js';
import { LANGUAGES } from './filetypes.js';

// CodeMirror 5 is ~500 KB, so it is loaded on first use, not with the page.
const CM = 'vendor/codemirror/';
const SCRIPTS = [
  'codemirror.js',
  'addon/mode/simple.js', 'addon/mode/overlay.js',
  'addon/dialog/dialog.js', 'addon/search/searchcursor.js', 'addon/search/search.js', 'addon/search/jump-to-line.js',
  'addon/edit/matchbrackets.js', 'addon/edit/closebrackets.js', 'addon/selection/active-line.js', 'addon/comment/comment.js',
  // htmlmixed needs xml, javascript and css loaded first.
  'mode/xml/xml.js', 'mode/javascript/javascript.js', 'mode/css/css.js', 'mode/htmlmixed/htmlmixed.js',
  'mode/properties/properties.js', 'mode/yaml/yaml.js', 'mode/shell/shell.js', 'mode/toml/toml.js',
  'mode/python/python.js', 'mode/clike/clike.js', 'mode/lua/lua.js', 'mode/sql/sql.js', 'mode/markdown/markdown.js',
  'mode/go/go.js', 'mode/dockerfile/dockerfile.js', 'mode/nginx/nginx.js', 'mode/diff/diff.js',
];
let loading;

function loadCodeMirror() {
  if (window.CodeMirror) return Promise.resolve();
  if (loading) return loading;
  for (const css of ['codemirror.css', 'addon/dialog/dialog.css']) {
    const l = document.createElement('link');
    l.rel = 'stylesheet';
    l.href = CM + css;
    document.head.appendChild(l);
  }
  const load = (src) => new Promise((resolve, reject) => {
    const s = document.createElement('script');
    s.src = CM + src;
    s.onload = resolve;
    s.onerror = () => reject(new Error('编辑器加载失败：' + src));
    document.head.appendChild(s);
  });
  loading = SCRIPTS.reduce((p, src) => p.then(() => load(src)), Promise.resolve()).then(defineLogMode);
  loading.catch(() => { loading = null; });
  return loading;
}

// Game-server logs: timestamps, levels, stack traces.
function defineLogMode() {
  CodeMirror.defineSimpleMode('gplog', {
    start: [
      { regex: /\[?\d{4}-\d{2}-\d{2}[ T]?\d{0,2}:?\d{0,2}:?\d{0,2}[^\]\s]*\]?|\[?\d{2}:\d{2}:\d{2}(?:[.,]\d+)?\]?/, token: 'comment' },
      { regex: /\b(?:ERROR|SEVERE|FATAL|FAILED)\b|\[[^\]]*\/(?:ERROR|FATAL)\]/i, token: 'error' },
      { regex: /\b(?:WARN|WARNING)\b|\[[^\]]*\/WARN\]/i, token: 'keyword' },
      { regex: /\b(?:INFO|DEBUG|TRACE)\b|\[[^\]]*\/(?:INFO|DEBUG)\]/, token: 'def' },
      { regex: /^\s+at .*$|^\S+(?:Exception|Error)\b.*$/, token: 'error' },
      { regex: /"(?:[^\\"]|\\.)*"/, token: 'string' },
      { regex: /\b\d+(?:\.\d+)*\b/, token: 'number' },
    ],
  });
}

const sameMode = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const prefs = () => { try { return JSON.parse(localStorage.getItem('gpanel.editor') || '{}'); } catch { return {}; } };
const savePrefs = (p) => { try { localStorage.setItem('gpanel.editor', JSON.stringify(p)); } catch {} };

export const CodeEditor = {
  props: { base: String, path: String, mode: [String, Object] },
  emits: ['close', 'saved'],
  setup(props, { emit }) {
    const { ref, shallowRef, onMounted, onUnmounted } = Vue;
    const host = ref(null);
    const cm = shallowRef(null);
    const state = ref('loading'); // loading | binary | ready | error
    const error = ref('');
    const dirty = ref(false);
    const saving = ref(false);
    const cursor = ref({ line: 1, ch: 1 });
    const wrap = ref(prefs().wrap ?? false);
    const langIndex = ref(Math.max(0, LANGUAGES.findIndex(([, m]) => sameMode(m, props.mode ?? null))));
    let text = '';
    let cleanGen = 0;

    function mount() {
      state.value = 'ready';
      Vue.nextTick(() => {
        const editor = CodeMirror(host.value, {
          value: text, mode: LANGUAGES[langIndex.value][1], theme: 'gpanel',
          lineNumbers: true, matchBrackets: true, autoCloseBrackets: true, styleActiveLine: true,
          lineWrapping: wrap.value, indentUnit: 2, tabSize: 4, indentWithTabs: false,
          extraKeys: {
            'Ctrl-S': () => save(), 'Cmd-S': () => save(),
            'Ctrl-/': 'toggleComment', 'Cmd-/': 'toggleComment',
            Tab: (c) => (c.somethingSelected() ? c.indentSelection('add') : c.replaceSelection(' '.repeat(c.getOption('indentUnit')))),
            'Shift-Tab': (c) => c.indentSelection('subtract'),
          },
        });
        cleanGen = editor.changeGeneration();
        editor.on('change', () => { dirty.value = !editor.isClean(cleanGen); });
        editor.on('cursorActivity', () => {
          const c = editor.getCursor();
          cursor.value = { line: c.line + 1, ch: c.ch + 1 };
        });
        editor.focus();
        cm.value = editor;
      });
    }

    onMounted(async () => {
      try {
        const [content] = await Promise.all([
          api('GET', `${props.base}/content?${q({ path: props.path })}`, undefined, { text: true }),
          loadCodeMirror(),
        ]);
        text = content;
        // NUL bytes almost never occur in text files.
        if (text.slice(0, 8000).includes('\u0000')) state.value = 'binary';
        else mount();
      } catch (e) {
        error.value = e.message;
        state.value = 'error';
      }
    });
    onUnmounted(() => { cm.value = null; });

    async function save() {
      if (!cm.value || saving.value) return;
      saving.value = true;
      const gen = cm.value.changeGeneration();
      const ok = await attempt(() => api('PUT', `${props.base}/content?${q({ path: props.path })}`, cm.value.getValue()));
      saving.value = false;
      if (ok === undefined) return;
      cleanGen = gen;
      dirty.value = !cm.value.isClean(cleanGen);
      toast('已保存', 'ok');
      emit('saved');
    }
    async function close() {
      if (dirty.value && !(await confirmAction('放弃未保存的修改？', props.path, { okText: '放弃修改', danger: true }))) return;
      emit('close');
    }
    const setLang = () => cm.value?.setOption('mode', LANGUAGES[langIndex.value][1]);
    const toggleWrap = () => {
      wrap.value = !wrap.value;
      cm.value?.setOption('lineWrapping', wrap.value);
      savePrefs({ ...prefs(), wrap: wrap.value });
    };
    const search = () => cm.value?.execCommand('find');

    return { host, state, error, dirty, saving, cursor, wrap, langIndex, LANGUAGES, save, close, setLang, toggleWrap, search, mount };
  },
  template: `
    <div class="modal-backdrop" @keydown.esc.self="close">
      <div class="card modal editor" role="dialog" aria-modal="true" :aria-label="'编辑 ' + path">
        <div class="editor-head">
          <span class="row editor-title"><Icon name="file-code" class="muted" /><code class="ellipsis" :title="path">{{ path }}</code>
            <span v-if="dirty" class="dirty-dot" title="有未保存的修改"></span></span>
          <div class="row">
            <button class="icon-only" title="查找 (Ctrl+F)" @click="search" :disabled="state !== 'ready'"><Icon name="search" /></button>
            <button class="icon-only" :class="{ pressed: wrap }" :aria-pressed="wrap" title="自动换行" @click="toggleWrap"><Icon name="wrap" /></button>
            <button @click="close">关闭</button>
            <button class="primary" @click="save" :disabled="state !== 'ready' || saving">
              <Icon :name="saving ? 'refresh' : 'check'" :class="{ spin: saving }" />保存
            </button>
          </div>
        </div>
        <div class="editor-body">
          <div v-if="state === 'loading'" class="editor-msg"><Icon name="refresh" class="spin" />正在打开…</div>
          <div v-else-if="state === 'error'" class="editor-msg error"><Icon name="alert" />{{ error }}</div>
          <div v-else-if="state === 'binary'" class="editor-msg">
            <Icon name="alert" :size="28" /><p>这看起来是二进制文件，用文本方式编辑可能会损坏它。</p>
            <button @click="mount">仍然打开</button>
          </div>
          <div v-show="state === 'ready'" ref="host" class="editor-host"></div>
        </div>
        <div class="editor-foot muted small">
          <span>行 {{ cursor.line }}，列 {{ cursor.ch }}</span>
          <span class="row">
            <span class="hide-sm">Ctrl+S 保存 · Ctrl+F 查找 · Ctrl+/ 注释</span>
            <select v-model="langIndex" @change="setLang" class="lang-select" aria-label="语法">
              <option v-for="(l, i) in LANGUAGES" :key="i" :value="i">{{ l[0] }}</option>
            </select>
          </span>
        </div>
      </div>
    </div>
  `,
};
