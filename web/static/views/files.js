import { api, attempt, q, formatSize, toast } from '../api.js';
import { ask, confirmAction, ContextMenu } from '../dialogs.js';
import { fileType } from './files/filetypes.js';
import { CodeEditor } from './files/editor.js';

// File explorer for an instance's directory: icon / list / compact views,
// click + Ctrl/Shift + rubber-band selection, context menus, drag to move
// (Ctrl/Alt+drag copies), drag files in from the desktop to upload, cut /
// copy / paste, inline rename, keyboard shortcuts, image preview and a code
// editor.

const DND_TYPE = 'application/x-gpanel-files';
const VIEWS = [
  { key: 'grid', label: '图标', icon: 'grid' },
  { key: 'list', label: '详细信息', icon: 'list' },
  { key: 'compact', label: '紧凑列表', icon: 'columns' },
];
const SORTS = [
  { key: 'name', label: '名称' }, { key: 'time', label: '修改时间' },
  { key: 'type', label: '类型' }, { key: 'size', label: '大小' },
];

// Shared across explorer instances so a cut in one tab can be pasted after
// switching tabs. Paths only make sense within the same instance (id).
const clipboard = Vue.reactive({ id: null, mode: null, paths: [] });
let dragSource = null; // { id, paths } while an internal drag is in progress

const prefs = () => { try { return JSON.parse(localStorage.getItem('gpanel.files') || '{}'); } catch { return {}; } };
const savePrefs = (p) => { try { localStorage.setItem('gpanel.files', JSON.stringify({ ...prefs(), ...p })); } catch {} };

const parentOf = (p) => p.replace(/\/[^/]+\/?$/, '') || '/';
const baseName = (p) => p.split('/').filter(Boolean).pop() || '/';
const stem = (name) => { const i = name.lastIndexOf('.'); return i > 0 ? name.slice(0, i) : name; };
const fmtTime = (t) => new Date(t).toLocaleString('zh-CN', { hour12: false, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });
const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
const mod = isMac ? '⌘' : 'Ctrl+';

const ImagePreview = {
  props: { base: String, images: Array, start: Number },
  emits: ['close'],
  setup(props) {
    const { ref, computed } = Vue;
    const i = ref(props.start);
    const cur = computed(() => props.images[i.value]);
    const src = computed(() => `${props.base}/download?${q({ path: cur.value, inline: 1 })}`);
    const step = (d) => { i.value = (i.value + d + props.images.length) % props.images.length; };
    return { i, cur, src, step, baseName };
  },
  template: `
    <div class="modal-backdrop preview" tabindex="-1" @click.self="$emit('close')" @keydown.esc="$emit('close')"
      @keydown.left="step(-1)" @keydown.right="step(1)" v-focus>
      <figure class="preview-fig">
        <img :src="src" :alt="baseName(cur)" :key="src">
        <figcaption><span>{{ baseName(cur) }}</span><span class="muted">{{ i + 1 }} / {{ images.length }}</span></figcaption>
      </figure>
      <button v-if="images.length > 1" class="preview-nav prev icon-only" @click="step(-1)" aria-label="上一张"><Icon name="chevron-left" :size="22" /></button>
      <button v-if="images.length > 1" class="preview-nav next icon-only" @click="step(1)" aria-label="下一张"><Icon name="chevron-right" :size="22" /></button>
      <button class="preview-close icon-only" @click="$emit('close')" aria-label="关闭"><Icon name="x" :size="20" /></button>
    </div>
  `,
};

const PropsDialog = {
  props: { info: Object },
  emits: ['close'],
  template: `
    <div class="modal-backdrop" @click.self="$emit('close')" @keydown.esc="$emit('close')" tabindex="-1" v-focus>
      <div class="card modal" role="dialog" aria-modal="true">
        <h2 class="row"><Icon :name="info.icon" />{{ info.title }}</h2>
        <dl class="facts">
          <template v-for="r in info.rows" :key="r[0]"><dt>{{ r[0] }}</dt><dd :class="{ mono: r[2] }">{{ r[1] }}</dd></template>
        </dl>
        <div class="row end"><button class="primary" @click="$emit('close')">确定</button></div>
      </div>
    </div>
  `,
};

export const FileManager = {
  components: { ContextMenu, CodeEditor, ImagePreview, PropsDialog },
  props: { id: String },
  setup(props) {
    const { ref, reactive, computed, watch, nextTick, onMounted, onUnmounted } = Vue;
    const p0 = prefs();

    const root = ref(null);
    const body = ref(null);
    const fileInput = ref(null);
    const path = ref('/');
    const entries = ref([]);
    const loading = ref(false);
    const view = ref(VIEWS.some((v) => v.key === p0.view) ? p0.view : 'grid');
    const sort = reactive({ key: p0.sortKey || 'name', dir: p0.sortDir || 1 });
    const showHidden = ref(p0.showHidden ?? true);
    const filter = ref('');
    const selected = ref(new Set());
    const anchor = ref(null);
    const focusName = ref(null);
    const renaming = ref(null);
    const renameValue = ref('');
    const menu = ref(null);
    const editing = ref(null);
    const preview = ref(null);
    const propsInfo = ref(null);
    const dropTarget = ref(null);
    const osDrag = ref(false);
    const marquee = ref(null);
    const upload = ref(null); // { label, loaded, total }
    const history = reactive({ back: [], fwd: [] });
    let lastPointer = 'mouse';

    const base = computed(() => `/api/instances/${props.id}/files`);
    const join = (name, dir = path.value) => (dir.endsWith('/') ? dir : dir + '/') + name;
    const crumbs = computed(() => {
      const parts = path.value.split('/').filter(Boolean);
      return [{ name: '根目录', path: '/' }, ...parts.map((p, i) => ({ name: p, path: '/' + parts.slice(0, i + 1).join('/') }))];
    });
    const typeOf = (e) => fileType(e);

    const visible = computed(() => {
      const f = filter.value.trim().toLowerCase();
      const list = entries.value.filter((e) => (showHidden.value || !e.name.startsWith('.')) && (!f || e.name.toLowerCase().includes(f)));
      const cmp = {
        name: (a, b) => a.name.localeCompare(b.name, 'zh-CN', { numeric: true, sensitivity: 'base' }),
        time: (a, b) => new Date(a.modTime) - new Date(b.modTime),
        size: (a, b) => a.size - b.size,
        type: (a, b) => typeOf(a).label.localeCompare(typeOf(b).label, 'zh-CN'),
      }[sort.key];
      // Folders always first, like most file managers.
      return list.sort((a, b) => (b.isDir - a.isDir) || (cmp(a, b) * sort.dir) || a.name.localeCompare(b.name));
    });
    const selectedEntries = computed(() => entries.value.filter((e) => selected.value.has(e.name)));
    const selectedSize = computed(() => selectedEntries.value.reduce((n, e) => n + (e.isDir ? 0 : e.size), 0));
    const counts = computed(() => {
      const dirs = visible.value.filter((e) => e.isDir).length;
      return { dirs, files: visible.value.length - dirs };
    });
    const clipHere = computed(() => clipboard.id === props.id && clipboard.paths.length > 0);
    const isCut = (e) => clipboard.id === props.id && clipboard.mode === 'cut' && clipboard.paths.includes(join(e.name));

    watch(view, (v) => savePrefs({ view: v }));
    watch(showHidden, (v) => savePrefs({ showHidden: v }));
    watch(sort, () => savePrefs({ sortKey: sort.key, sortDir: sort.dir }));

    // ---- navigation
    async function load(p = path.value, { keepSelection = false } = {}) {
      loading.value = true;
      const list = await attempt(() => api('GET', `${base.value}?${q({ path: p })}`));
      loading.value = false;
      if (!list) return false;
      const changed = p !== path.value;
      entries.value = list;
      path.value = p;
      if (changed) filter.value = '';
      if (!keepSelection || changed) selected.value = new Set();
      else selected.value = new Set([...selected.value].filter((n) => list.some((e) => e.name === n)));
      return true;
    }
    async function go(p) {
      if (p === path.value) return load(p);
      const from = path.value;
      if (await load(p)) { history.back.push(from); history.fwd = []; }
    }
    async function back() { const p = history.back.pop(); if (p !== undefined) { history.fwd.push(path.value); await load(p); } }
    async function forward() { const p = history.fwd.pop(); if (p !== undefined) { history.back.push(path.value); await load(p); } }
    const up = () => path.value !== '/' && go(parentOf(path.value));
    const refresh = () => load(path.value, { keepSelection: true });
    onMounted(() => load('/'));

    // ---- selection
    function selectOnly(name) { selected.value = new Set(name ? [name] : []); anchor.value = name; focusName.value = name; }
    function onItemPointerDown(ev) { lastPointer = ev.pointerType; }
    function onItemClick(ev, e) {
      if (renaming.value) return;
      root.value.focus({ preventScroll: true });
      if (lastPointer === 'touch' && !selected.value.size) return open(e);
      const names = visible.value.map((x) => x.name);
      if (ev.shiftKey && anchor.value && names.includes(anchor.value)) {
        const [a, b] = [names.indexOf(anchor.value), names.indexOf(e.name)].sort((x, y) => x - y);
        const range = names.slice(a, b + 1);
        selected.value = new Set(ev.ctrlKey || ev.metaKey ? [...selected.value, ...range] : range);
        focusName.value = e.name;
      } else if (ev.ctrlKey || ev.metaKey || (lastPointer === 'touch' && selected.value.size)) {
        const s = new Set(selected.value);
        s.has(e.name) ? s.delete(e.name) : s.add(e.name);
        selected.value = s;
        anchor.value = focusName.value = e.name;
      } else {
        selectOnly(e.name);
      }
    }
    const selectAll = () => { selected.value = new Set(visible.value.map((e) => e.name)); };

    // Rubber-band selection on empty space. Hit-testing uses the items' real
    // boxes, so it works the same in every view.
    function onBodyPointerDown(ev) {
      lastPointer = ev.pointerType;
      if (ev.button !== 0 || ev.pointerType === 'touch' || ev.target.closest('.fm-item, .fm-head, input, button')) return;
      root.value.focus({ preventScroll: true });
      const additive = ev.ctrlKey || ev.metaKey || ev.shiftKey;
      const start = { x: ev.clientX, y: ev.clientY };
      const baseSel = additive ? new Set(selected.value) : new Set();
      let active = false;
      const items = [...body.value.querySelectorAll('.fm-item')];
      const move = (m) => {
        if (!active && Math.hypot(m.clientX - start.x, m.clientY - start.y) < 4) return;
        active = true;
        const r = { l: Math.min(start.x, m.clientX), t: Math.min(start.y, m.clientY), r: Math.max(start.x, m.clientX), b: Math.max(start.y, m.clientY) };
        const br = body.value.getBoundingClientRect();
        marquee.value = { left: r.l - br.left + 'px', top: r.t - br.top + 'px', width: r.r - r.l + 'px', height: r.b - r.t + 'px' };
        const s = new Set(baseSel);
        for (const el of items) {
          const b = el.getBoundingClientRect();
          if (b.left < r.r && b.right > r.l && b.top < r.b && b.bottom > r.t) s.add(el.dataset.name);
        }
        selected.value = s;
      };
      const end = () => {
        removeEventListener('pointermove', move);
        removeEventListener('pointerup', end);
        if (!active && !additive) selectOnly(null);
        marquee.value = null;
      };
      addEventListener('pointermove', move);
      addEventListener('pointerup', end);
      ev.preventDefault(); // no text selection while dragging
    }

    // ---- opening
    function open(e) {
      if (!e) return;
      const t = typeOf(e);
      if (t.open === 'dir') return go(join(e.name));
      if (t.open === 'image') {
        const images = visible.value.filter((x) => typeOf(x).open === 'image').map((x) => join(x.name));
        preview.value = { images, start: images.indexOf(join(e.name)) };
        return;
      }
      if (t.open === 'text') {
        if (e.size > 5 * 1024 * 1024) return toast('文件超过 5 MB，请下载后编辑', 'error');
        editing.value = { path: join(e.name), mode: t.mode };
        return;
      }
      if (t.zip) return unzip(e, 'here');
      toast(`无法在面板中打开${t.label}，可以下载后查看`, 'info');
    }
    const editAsText = (e) => { editing.value = { path: join(e.name), mode: typeOf(e).mode }; };
    const download = (e) => {
      const a = document.createElement('a');
      a.href = `${base.value}/download?${q({ path: join(e.name) })}`;
      a.download = e.name;
      a.click();
    };

    // ---- file operations
    const names = () => [...selected.value];
    const uniqueName = (want) => {
      const taken = new Set(entries.value.map((e) => e.name));
      if (!taken.has(want)) return want;
      const ext = want.includes('.') ? want.slice(want.lastIndexOf('.')) : '';
      for (let i = 2; ; i++) { const n = `${stem(want)} (${i})${ext}`; if (!taken.has(n)) return n; }
    };
    async function afterCreate(name) {
      await load();
      selectOnly(name);
      startRename(name);
    }
    const newFolder = () => attempt(async () => {
      const name = uniqueName('新建文件夹');
      await api('POST', `${base.value}/mkdir`, { path: join(name) });
      await afterCreate(name);
    });
    const newFile = () => attempt(async () => {
      const name = uniqueName('新建文件.txt');
      await api('PUT', `${base.value}/content?${q({ path: join(name) })}`, '');
      await afterCreate(name);
    });

    function startRename(name) {
      renaming.value = name;
      renameValue.value = name;
      nextTick(() => {
        const input = body.value?.querySelector('.fm-rename');
        if (!input) return;
        input.focus();
        const e = entries.value.find((x) => x.name === name);
        input.setSelectionRange(0, e && !e.isDir ? stem(name).length : name.length);
      });
    }
    async function commitRename() {
      const from = renaming.value;
      if (from === null) return;
      const to = renameValue.value.trim();
      renaming.value = null;
      root.value?.focus({ preventScroll: true });
      if (!to || to === from) return;
      if (/[/\\]/.test(to)) return toast('名称不能包含 / 或 \\', 'error');
      if (entries.value.some((e) => e.name === to)) return toast(`已存在名为「${to}」的项目`, 'error');
      const ok = await attempt(() => api('POST', `${base.value}/rename`, { from: join(from), to: join(to) }));
      if (ok !== undefined) { await load(); selectOnly(to); }
    }
    const cancelRename = () => { renaming.value = null; root.value?.focus({ preventScroll: true }); };

    async function remove(list = names()) {
      if (!list.length) return;
      const label = list.length === 1 ? `「${list[0]}」` : `选中的 ${list.length} 项`;
      if (!(await confirmAction(`删除${label}？`, '删除后无法恢复，文件夹会连同其内容一起删除。', { okText: '删除', danger: true }))) return;
      const ok = await attempt(() => api('POST', `${base.value}/delete`, { paths: list.map((n) => join(n)) }), '已删除');
      if (ok !== undefined) load();
    }
    const setClipboard = (mode) => {
      if (!selected.value.size) return;
      Object.assign(clipboard, { id: props.id, mode, paths: names().map((n) => join(n)) });
      toast(`已${mode === 'cut' ? '剪切' : '复制'} ${clipboard.paths.length} 项`, 'info');
    };
    async function paste(dest = path.value) {
      if (!clipHere.value) return;
      const { mode, paths } = clipboard;
      await transfer(paths, dest, mode === 'cut' ? 'move' : 'copy');
      if (mode === 'cut') Object.assign(clipboard, { id: null, mode: null, paths: [] });
    }
    // Move or copy paths into dest, then show the result.
    async function transfer(paths, dest, op) {
      if (op === 'move' && paths.every((p) => parentOf(p) === dest)) return;
      const r = await attempt(() => api('POST', `${base.value}/${op}`, { paths, dest }));
      if (r === undefined) return;
      const n = paths.length;
      toast(`已${op === 'move' ? '移动' : '复制'} ${n} 项${dest === path.value ? '' : '到 ' + baseName(dest)}`, 'ok');
      await load();
      if (dest === path.value) selected.value = new Set(op === 'copy' ? r.created : paths.map(baseName));
    }
    async function zip(list = names()) {
      if (!list.length) return;
      const def = (list.length === 1 ? stem(list[0]) : baseName(path.value) === '/' ? '归档' : baseName(path.value)) + '.zip';
      const name = await ask({ title: '压缩为 ZIP', message: `${list.length} 项将被压缩到当前文件夹`, input: uniqueName(def), select: [0, stem(uniqueName(def)).length], okText: '压缩' });
      if (!name) return;
      const ok = await attempt(() => api('POST', `${base.value}/zip`, { paths: list.map((n) => join(n)), dest: join(name) }), '压缩完成');
      if (ok !== undefined) { await load(); selectOnly(name); }
    }
    async function unzip(e, where) {
      let dest = path.value;
      if (where === 'folder') dest = join(uniqueName(stem(e.name)));
      if (where === 'ask') {
        const d = await ask({ title: '解压到', message: '相对实例根目录的路径，不存在会自动创建', input: path.value, okText: '解压' });
        if (!d) return;
        dest = d;
      }
      if (where === 'here' && !(await confirmAction(`解压「${e.name}」到当前文件夹？`, '同名文件会被覆盖。', { okText: '解压' }))) return;
      const ok = await attempt(() => api('POST', `${base.value}/unzip`, { path: join(e.name), dest }), '解压完成');
      if (ok !== undefined) load();
    }
    function showProps(list = selectedEntries.value) {
      if (list.length === 1) {
        const e = list[0];
        const t = typeOf(e);
        propsInfo.value = {
          title: e.name, icon: t.icon,
          rows: [
            ['类型', t.label + (e.symlink ? '（符号链接）' : '')],
            ['位置', path.value, true],
            ...(e.isDir ? [] : [['大小', `${formatSize(e.size)}（${e.size.toLocaleString()} 字节）`]]),
            ['修改时间', new Date(e.modTime).toLocaleString('zh-CN', { hour12: false })],
            ['权限', e.mode, true],
          ],
        };
      } else {
        const dirs = list.filter((e) => e.isDir).length;
        propsInfo.value = {
          title: `${list.length} 项`, icon: 'copy',
          rows: [['包含', `${list.length - dirs} 个文件，${dirs} 个文件夹`], ['位置', path.value, true], ['文件总大小', formatSize(selectedSize.value) + '（不含文件夹）']],
        };
      }
    }

    // ---- upload (button, or files dropped from the desktop)
    // Files go up in chunks (see internal/files/upload.go). A dropped
    // connection retries from the offset the server has; uploading the same
    // file again after a reload or panel restart continues where it stopped.
    const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
    function putChunk(url, blob, onProgress, signal) {
      return new Promise((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        xhr.open('PUT', url);
        xhr.upload.onprogress = (e) => onProgress(e.loaded);
        xhr.onload = () => {
          let body = {};
          try { body = JSON.parse(xhr.responseText); } catch {}
          if (xhr.status === 401) window.dispatchEvent(new Event('gpanel:unauthorized'));
          if (xhr.status < 300) return resolve(body);
          reject(Object.assign(new Error(body.error || xhr.statusText), { status: xhr.status, body }));
        };
        xhr.onerror = () => reject(Object.assign(new Error('网络错误'), { status: 0 }));
        xhr.onabort = () => reject(Object.assign(new Error('已取消'), { aborted: true }));
        signal.addEventListener('abort', () => xhr.abort(), { once: true });
        xhr.send(blob);
      });
    }
    async function uploadOne(file, dest, onProgress, signal, track) {
      const spec = { path: dest, name: file.name, size: file.size, modified: file.lastModified };
      const begin = () => api('POST', `${base.value}/uploads`, spec);
      let { id, offset, chunk } = await begin();
      track.id = id; track.spec = spec; // for cancelling
      if (offset > 0) toast(`${file.name}：从 ${Math.round((offset / file.size) * 100)}% 处继续上传`, 'info');
      const qs = q({ ...spec, path: dest });
      for (let failures = 0; ;) {
        if (signal.aborted) throw Object.assign(new Error('已取消'), { aborted: true });
        const end = Math.min(offset + chunk, file.size);
        onProgress(offset);
        try {
          const r = await putChunk(`${base.value}/uploads/${id}?${qs}&offset=${offset}`, file.slice(offset, end), (n) => onProgress(offset + n), signal);
          offset = r.offset;
          failures = 0;
          if (r.done) return;
        } catch (e) {
          if (e.aborted) throw e;
          if (e.status === 409 && typeof e.body?.offset === 'number') { offset = e.body.offset; continue; }
          if (e.status && e.status !== 408 && e.status < 500) throw e; // refused: don't retry
          if (++failures > 6) throw new Error('网络多次中断，已暂停。重新上传同一个文件会从断点继续');
          await sleep(Math.min(1000 * 2 ** (failures - 1), 15000));
          try { ({ offset } = await begin()); } catch {} // where the server got to
        }
      }
    }
    async function uploadFiles(files, dest = path.value) {
      files = [...files].filter(Boolean);
      if (!files.length || upload.value) return;
      if (dest === path.value) {
        const clash = files.filter((f) => entries.value.some((e) => e.name === f.name && !e.isDir));
        if (clash.length && !(await confirmAction(`覆盖 ${clash.length} 个同名文件？`, clash.slice(0, 5).map((f) => f.name).join('、') + (clash.length > 5 ? ' 等' : ''), { okText: '覆盖', danger: true }))) return;
      }
      const total = files.reduce((n, f) => n + f.size, 0);
      const ctrl = new AbortController();
      upload.value = { label: files.length === 1 ? files[0].name : `${files.length} 个文件`, loaded: 0, total, cancel: () => ctrl.abort() };
      let done = 0;
      const track = {}; // the upload in progress: { id, spec }
      try {
        for (const f of files) {
          await uploadOne(f, dest, (n) => { if (upload.value) upload.value.loaded = done + n; }, ctrl.signal, track);
          done += f.size;
        }
        toast(`已上传 ${files.length} 个文件${dest === path.value ? '' : '到 ' + baseName(dest)}`, 'ok');
        await load();
        if (dest === path.value) selected.value = new Set(files.map((f) => f.name));
      } catch (e) {
        if (e.aborted) {
          if (track.id) api('DELETE', `${base.value}/uploads/${track.id}?${q(track.spec)}`).catch(() => {});
          toast('已取消上传', 'info');
        } else toast('上传失败：' + e.message, 'error');
        if (done) load();
      } finally {
        upload.value = null;
      }
    }
    const pickFiles = () => fileInput.value.click();
    const onPicked = (ev) => { uploadFiles(ev.target.files); ev.target.value = ''; };
    function filesFromDrop(dt) {
      const out = [];
      let dirs = 0;
      for (const item of dt.items || []) {
        if (item.kind !== 'file') continue;
        if (item.webkitGetAsEntry?.()?.isDirectory) { dirs++; continue; }
        out.push(item.getAsFile());
      }
      if (dirs) toast(`暂不支持上传文件夹（已跳过 ${dirs} 个），可以先压缩成 zip 再上传后解压`, 'info');
      return out.length || dt.items ? out : [...dt.files];
    }

    // ---- drag & drop
    function onDragStart(ev, e) {
      if (renaming.value) return ev.preventDefault();
      if (!selected.value.has(e.name)) selectOnly(e.name);
      const paths = names().map((n) => join(n));
      dragSource = { id: props.id, paths };
      ev.dataTransfer.effectAllowed = 'copyMove';
      ev.dataTransfer.setData(DND_TYPE, JSON.stringify(dragSource));
      ev.dataTransfer.setData('text/plain', paths.join('\n'));
      const ghost = document.createElement('div');
      ghost.className = 'fm-drag-ghost';
      ghost.textContent = paths.length > 1 ? `${paths.length} 项` : e.name;
      document.body.appendChild(ghost);
      ev.dataTransfer.setDragImage(ghost, -10, -10);
      setTimeout(() => ghost.remove());
    }
    const onDragEnd = () => { dragSource = null; dropTarget.value = null; };
    // Returns 'internal' | 'files' | null for a drag over dest.
    function dragKind(ev, dest) {
      const types = ev.dataTransfer.types;
      if (types.includes(DND_TYPE)) {
        if (!dragSource || dragSource.id !== props.id) return null;
        if (dragSource.paths.some((p) => dest === p || dest.startsWith(p + '/'))) return null;
        return 'internal';
      }
      return types.includes('Files') ? 'files' : null;
    }
    function overTarget(ev, dest) {
      const kind = dragKind(ev, dest);
      if (!kind) return;
      ev.preventDefault();
      ev.stopPropagation();
      ev.dataTransfer.dropEffect = kind === 'files' || ev.ctrlKey || ev.altKey ? 'copy' : 'move';
      dropTarget.value = dest;
      osDrag.value = false;
    }
    const leaveTarget = (ev, dest) => {
      if (dropTarget.value === dest && !ev.currentTarget.contains(ev.relatedTarget)) dropTarget.value = null;
    };
    function dropOn(ev, dest) {
      const kind = dragKind(ev, dest);
      dropTarget.value = null;
      osDrag.value = false;
      if (!kind) return;
      ev.preventDefault();
      ev.stopPropagation();
      if (kind === 'internal') transfer(dragSource.paths, dest, ev.ctrlKey || ev.altKey ? 'copy' : 'move');
      else uploadFiles(filesFromDrop(ev.dataTransfer), dest);
    }
    // Background of the file area: desktop files upload into the current folder.
    function onBodyDragOver(ev) {
      if (!ev.dataTransfer.types.includes('Files')) return;
      ev.preventDefault();
      ev.dataTransfer.dropEffect = 'copy';
      if (!dropTarget.value) osDrag.value = true;
    }
    const onBodyDragLeave = (ev) => { if (!body.value.contains(ev.relatedTarget)) osDrag.value = false; };
    function onBodyDrop(ev) {
      osDrag.value = false;
      if (!ev.dataTransfer.types.includes('Files')) return;
      ev.preventDefault();
      uploadFiles(filesFromDrop(ev.dataTransfer));
    }

    // ---- context menus
    function itemMenu(ev, e) {
      if (!selected.value.has(e.name)) selectOnly(e.name);
      const sel = selectedEntries.value;
      const one = sel.length === 1 ? sel[0] : null;
      const t = one && typeOf(one);
      const items = [];
      if (one) {
        items.push({ label: one.isDir ? '打开' : t.open === 'image' ? '预览' : t.open === 'text' ? '编辑' : '打开', icon: one.isDir ? 'folder' : t.open === 'image' ? 'eye' : 'edit', shortcut: 'Enter', action: () => open(one), disabled: !one.isDir && !t.open && !t.zip });
        if (!one.isDir && !t.open && !t.zip) items.push({ label: '以文本方式编辑', icon: 'file-text', action: () => editAsText(one) });
        if (!one.isDir) items.push({ label: '下载', icon: 'download', action: () => download(one) });
        if (t.zip) {
          items.push({ divider: true });
          items.push({ label: '解压到当前文件夹', icon: 'archive', action: () => unzip(one, 'here') });
          items.push({ label: `解压到「${stem(one.name)}/」`, icon: 'folder-plus', action: () => unzip(one, 'folder') });
          items.push({ label: '解压到…', icon: 'archive', action: () => unzip(one, 'ask') });
        }
        items.push({ divider: true });
      }
      items.push(
        { label: '剪切', icon: 'scissors', shortcut: mod + 'X', action: () => setClipboard('cut') },
        { label: '复制', icon: 'copy', shortcut: mod + 'C', action: () => setClipboard('copy') },
      );
      if (one?.isDir && clipHere.value) items.push({ label: '粘贴到此文件夹', icon: 'clipboard', action: () => paste(join(one.name)) });
      items.push({ divider: true });
      if (one) items.push({ label: '重命名', icon: 'edit', shortcut: 'F2', action: () => startRename(one.name) });
      items.push(
        { label: '压缩为 ZIP…', icon: 'archive', action: () => zip() },
        { label: '删除', icon: 'trash', shortcut: 'Del', danger: true, action: () => remove() },
        { divider: true },
        { label: '属性', icon: 'info', action: () => showProps() },
      );
      menu.value = { x: ev.clientX, y: ev.clientY, items };
    }
    function blankMenu(ev) {
      if (ev.target.closest('.fm-item')) return;
      selectOnly(null);
      menu.value = {
        x: ev.clientX, y: ev.clientY,
        items: [
          { label: '新建文件', icon: 'file-plus', action: newFile },
          { label: '新建文件夹', icon: 'folder-plus', action: newFolder },
          { label: '上传文件…', icon: 'upload', action: pickFiles },
          { divider: true },
          { label: clipHere.value ? `粘贴（${clipboard.paths.length} 项）` : '粘贴', icon: 'clipboard', shortcut: mod + 'V', disabled: !clipHere.value, action: () => paste() },
          { label: '全选', icon: 'select-all', shortcut: mod + 'A', action: selectAll, disabled: !visible.value.length },
          { label: '刷新', icon: 'refresh', action: refresh },
          { divider: true },
          ...VIEWS.map((v) => ({ label: v.label + '视图', checked: view.value === v.key, action: () => { view.value = v.key; } })),
          { divider: true },
          ...SORTS.map((s) => ({ label: '按' + s.label + '排序', checked: sort.key === s.key, action: () => setSort(s.key) })),
          { label: '降序', checked: sort.dir < 0, action: () => { sort.dir = -sort.dir; } },
          { label: '显示隐藏文件', checked: showHidden.value, action: () => { showHidden.value = !showHidden.value; } },
        ],
      };
    }
    function setSort(key) {
      if (sort.key === key) sort.dir = -sort.dir;
      else { sort.key = key; sort.dir = key === 'name' || key === 'type' ? 1 : -1; }
    }

    // ---- keyboard
    function columns() {
      const items = body.value?.querySelectorAll('.fm-item');
      if (!items?.length) return 1;
      const top = items[0].offsetTop;
      let n = 0;
      for (const el of items) { if (el.offsetTop !== top) break; n++; }
      return Math.max(1, n);
    }
    function moveFocus(delta, extend) {
      const list = visible.value;
      if (!list.length) return;
      const cur = list.findIndex((e) => e.name === focusName.value);
      const i = cur < 0 ? 0 : Math.max(0, Math.min(list.length - 1, cur + delta));
      const name = list[i].name;
      if (extend && anchor.value) {
        const names = list.map((e) => e.name);
        const [a, b] = [names.indexOf(anchor.value), i].sort((x, y) => x - y);
        selected.value = new Set(names.slice(a, b + 1));
        focusName.value = name;
      } else {
        selectOnly(name);
      }
      nextTick(() => body.value.querySelector(`.fm-item[data-name="${CSS.escape(name)}"]`)?.scrollIntoView({ block: 'nearest' }));
    }
    function onKey(ev) {
      if (ev.target.closest('input, textarea, select') || editing.value || preview.value || menu.value) return;
      const ctrl = ev.ctrlKey || ev.metaKey;
      const k = ev.key;
      const handled = () => { ev.preventDefault(); ev.stopPropagation(); };
      if (ctrl && k.toLowerCase() === 'a') { handled(); selectAll(); }
      else if (ctrl && k.toLowerCase() === 'c') { handled(); setClipboard('copy'); }
      else if (ctrl && k.toLowerCase() === 'x') { handled(); setClipboard('cut'); }
      else if (ctrl && k.toLowerCase() === 'v') { handled(); paste(); }
      else if (k === 'Delete' || (ctrl && k === 'Backspace')) { handled(); remove(); }
      else if (k === 'F2' && selected.value.size === 1) { handled(); startRename(names()[0]); }
      else if (k === 'Enter' && selected.value.size === 1) { handled(); open(selectedEntries.value[0]); }
      else if (k === 'Backspace' || (ev.altKey && k === 'ArrowUp')) { handled(); up(); }
      else if (ev.altKey && k === 'ArrowLeft') { handled(); back(); }
      else if (ev.altKey && k === 'ArrowRight') { handled(); forward(); }
      else if (k === 'Escape' && selected.value.size) { handled(); selectOnly(null); }
      else if (k === 'ArrowDown') { handled(); moveFocus(view.value === 'list' ? 1 : columns(), ev.shiftKey); }
      else if (k === 'ArrowUp') { handled(); moveFocus(-(view.value === 'list' ? 1 : columns()), ev.shiftKey); }
      else if (k === 'ArrowRight' && view.value !== 'list') { handled(); moveFocus(1, ev.shiftKey); }
      else if (k === 'ArrowLeft' && view.value !== 'list') { handled(); moveFocus(-1, ev.shiftKey); }
      else if (k === 'Home') { handled(); moveFocus(-1e9, ev.shiftKey); }
      else if (k === 'End') { handled(); moveFocus(1e9, ev.shiftKey); }
    }

    // Close the menu if the instance tab is left with it open.
    onUnmounted(() => { menu.value = null; });

    const imagePreviewSrc = (e) => `${base.value}/download?${q({ path: join(e.name), inline: 1 })}`;
    const uploadPct = computed(() => (upload.value?.total ? Math.round((upload.value.loaded / upload.value.total) * 100) : 0));

    return {
      root, body, fileInput, path, entries, loading, view, sort, showHidden, filter, selected, focusName,
      renaming, renameValue, menu, editing, preview, propsInfo, dropTarget, osDrag, marquee, upload, uploadPct,
      history, base, crumbs, visible, selectedEntries, selectedSize, counts, clipboard, clipHere, VIEWS, SORTS,
      typeOf, isCut, join, go, back, forward, up, refresh, load, onItemPointerDown, onItemClick, onBodyPointerDown,
      open, download, newFolder, newFile, startRename, commitRename, cancelRename, remove, setClipboard, paste, zip,
      showProps, pickFiles, onPicked, onDragStart, onDragEnd, overTarget, leaveTarget, dropOn, onBodyDragOver,
      onBodyDragLeave, onBodyDrop, itemMenu, blankMenu, setSort, onKey, selectOnly, formatSize, fmtTime,
      imagePreviewSrc, mod,
    };
  },
  template: `
    <div class="card fm" :class="'fm-' + view" ref="root" tabindex="0" @keydown="onKey">
      <div class="fm-toolbar">
        <div class="fm-nav">
          <button class="icon-only" @click="back" :disabled="!history.back.length" title="后退 (Alt+←)" aria-label="后退"><Icon name="arrow-left" /></button>
          <button class="icon-only" @click="forward" :disabled="!history.fwd.length" title="前进 (Alt+→)" aria-label="前进"><Icon name="arrow" /></button>
          <button class="icon-only" @click="up" :disabled="path === '/'" title="上一级 (Backspace)" aria-label="上一级"
            :class="{ 'drop-target': path !== '/' && dropTarget === crumbs[crumbs.length - 2]?.path }"
            @dragover="path !== '/' && overTarget($event, crumbs[crumbs.length - 2].path)"
            @dragleave="leaveTarget($event, crumbs[crumbs.length - 2]?.path)"
            @drop="path !== '/' && dropOn($event, crumbs[crumbs.length - 2].path)"><Icon name="arrow-up" /></button>
          <nav class="crumbs" aria-label="路径">
            <template v-for="(c, i) in crumbs" :key="c.path">
              <Icon v-if="i" name="chevron-right" :size="14" class="muted" />
              <a href="" @click.prevent="go(c.path)" :class="{ 'drop-target': dropTarget === c.path }"
                @dragover="overTarget($event, c.path)" @dragleave="leaveTarget($event, c.path)" @drop="dropOn($event, c.path)">{{ c.name }}</a>
            </template>
          </nav>
        </div>
        <div class="fm-actions">
          <label class="fm-search"><Icon name="search" class="muted" />
            <input v-model="filter" placeholder="筛选当前文件夹" aria-label="筛选" @keydown.esc="filter = ''">
          </label>
          <button @click="newFile" title="新建文件"><Icon name="file-plus" /><span class="hide-sm">新建文件</span></button>
          <button @click="newFolder" title="新建文件夹"><Icon name="folder-plus" /><span class="hide-sm">新建文件夹</span></button>
          <button @click="pickFiles" :disabled="!!upload" title="上传文件"><Icon name="upload" /><span class="hide-sm">上传</span></button>
          <input ref="fileInput" type="file" multiple hidden @change="onPicked">
          <button class="icon-only" @click="refresh" title="刷新" aria-label="刷新"><Icon name="refresh" :class="{ spin: loading }" /></button>
          <div class="seg" role="group" aria-label="视图">
            <button v-for="v in VIEWS" :key="v.key" class="icon-only" :class="{ pressed: view === v.key }" :aria-pressed="view === v.key"
              :title="v.label + '视图'" @click="view = v.key"><Icon :name="v.icon" /></button>
          </div>
        </div>
      </div>


      <div class="fm-body" ref="body" @pointerdown="onBodyPointerDown" @contextmenu.prevent="blankMenu"
        @dragover="onBodyDragOver" @dragleave="onBodyDragLeave" @drop="onBodyDrop">
        <div v-if="view === 'list'" class="fm-head">
          <button v-for="s in SORTS" :key="s.key" class="link" :class="'col-' + s.key" @click="setSort(s.key)">
            {{ s.label }}<Icon v-if="sort.key === s.key" name="arrow-up" :size="12" :class="{ 'sort-desc': sort.dir < 0 }" />
          </button>
        </div>
        <div class="fm-items" :key="path">
          <div v-for="(e, i) in visible" :key="e.name" class="fm-item" :data-name="e.name"
            :class="{ selected: selected.has(e.name), focused: focusName === e.name, cut: isCut(e), 'drop-target': dropTarget === join(e.name), dir: e.isDir }"
            :style="{ '--i': Math.min(i, 24) }" :title="view === 'grid' ? e.name : null"
            :draggable="renaming !== e.name" @dragstart="onDragStart($event, e)" @dragend="onDragEnd"
            @dragover="e.isDir && overTarget($event, join(e.name))" @dragleave="e.isDir && leaveTarget($event, join(e.name))" @drop="e.isDir && dropOn($event, join(e.name))"
            @pointerdown="onItemPointerDown" @click="onItemClick($event, e)" @dblclick="open(e)" @contextmenu.prevent.stop="itemMenu($event, e)">
            <span class="fm-icon" :class="'ft-' + typeOf(e).kind">
              <img v-if="view === 'grid' && typeOf(e).open === 'image' && e.size < 2097152" :src="imagePreviewSrc(e)" loading="lazy" alt="" draggable="false">
              <Icon v-else :name="typeOf(e).icon" :size="view === 'grid' ? 40 : 18" />
              <span v-if="e.symlink" class="fm-link" title="符号链接">↗</span>
            </span>
            <span class="fm-name">
              <input v-if="renaming === e.name" v-model="renameValue" class="fm-rename" spellcheck="false"
                @keydown.enter.prevent="commitRename" @keydown.esc.prevent.stop="cancelRename" @blur="commitRename" @click.stop @dblclick.stop @pointerdown.stop>
              <template v-else>{{ e.name }}</template>
            </span>
            <template v-if="view === 'list'">
              <span class="fm-col col-time">{{ fmtTime(e.modTime) }}</span>
              <span class="fm-col col-type">{{ typeOf(e).label }}</span>
              <span class="fm-col col-size">{{ e.isDir ? '' : formatSize(e.size) }}</span>
            </template>
          </div>
        </div>
        <div v-if="!visible.length && !loading" class="empty fm-empty">
          <template v-if="filter"><Icon name="search" :size="36" /><p>没有名称包含「{{ filter }}」的项目</p></template>
          <template v-else><Icon name="folder" :size="36" /><p>这个文件夹是空的</p><p class="small">把文件拖到这里即可上传，或右键新建</p></template>
        </div>
        <div v-if="marquee" class="fm-marquee" :style="marquee"></div>
        <Transition name="fade"><div v-if="osDrag" class="fm-dropzone"><Icon name="upload" :size="32" />松开即可上传到「{{ path === '/' ? '根目录' : path }}」</div></Transition>
      </div>

      <div class="fm-dock">
      <Transition name="pop">
        <div v-if="selected.size" class="fm-selbar" role="toolbar" aria-label="所选项目操作">
          <span>已选择 <strong>{{ selected.size }}</strong> 项<span v-if="selectedSize" class="muted"> · {{ formatSize(selectedSize) }}</span></span>
          <div class="row">
            <button v-if="selected.size === 1 && !selectedEntries[0]?.isDir" class="link" @click="download(selectedEntries[0])"><Icon name="download" />下载</button>
            <button class="link" @click="setClipboard('cut')"><Icon name="scissors" />剪切</button>
            <button class="link" @click="setClipboard('copy')"><Icon name="copy" />复制</button>
            <button v-if="selected.size === 1" class="link" @click="startRename([...selected][0])"><Icon name="edit" />重命名</button>
            <button class="link" @click="zip()"><Icon name="archive" />压缩</button>
            <button class="link danger" @click="remove()"><Icon name="trash" />删除</button>
            <button class="link" @click="selectOnly(null)" title="取消选择 (Esc)" aria-label="取消选择"><Icon name="x" /></button>
          </div>
        </div>
      </Transition>
      </div>
      <div class="fm-status muted small">
        <span>{{ counts.dirs }} 个文件夹，{{ counts.files }} 个文件<template v-if="visible.length !== entries.length">（共 {{ entries.length }} 项）</template></span>
        <span v-if="clipHere" class="row fm-clip">
          <Icon :name="clipboard.mode === 'cut' ? 'scissors' : 'copy'" :size="14" />剪贴板 {{ clipboard.paths.length }} 项
          <button class="link" @click="paste()">粘贴到这里</button>
        </span>
        <span v-if="upload" class="fm-upload">
          <span class="ellipsis">上传 {{ upload.label }}</span>
          <span class="progress"><span :style="{ width: uploadPct + '%' }"></span></span>{{ uploadPct }}%
          <button class="link" @click="upload.cancel()" title="取消上传"><Icon name="x" :size="14" />取消</button>
        </span>
        <span class="hide-sm fm-hint">双击打开 · 拖动框选 · 右键更多操作 · 按住 Ctrl 拖动为复制</span>
      </div>

      <ContextMenu v-if="menu" v-bind="menu" @close="menu = null" />
      <Transition name="modal">
        <CodeEditor v-if="editing" :base="base" :path="editing.path" :mode="editing.mode" @close="editing = null; root.focus()" @saved="refresh" />
      </Transition>
      <Transition name="modal">
        <ImagePreview v-if="preview" :base="base" :images="preview.images" :start="preview.start" @close="preview = null; root.focus()" />
      </Transition>
      <Transition name="modal">
        <PropsDialog v-if="propsInfo" :info="propsInfo" @close="propsInfo = null; root.focus()" />
      </Transition>
    </div>
  `,
};
