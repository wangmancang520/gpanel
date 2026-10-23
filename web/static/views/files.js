import { api, attempt, q, formatSize, toast } from '../api.js';

export const FileManager = {
  props: { id: String },
  setup(props) {
    const { ref, computed, onMounted } = Vue;
    const path = ref('/');
    const entries = ref([]);
    const selected = ref(new Set());
    const editor = ref(null); // { path, content }
    const uploading = ref(false);
    const fileInput = ref(null);

    const base = computed(() => `/api/instances/${props.id}/files`);
    const join = (name) => (path.value.endsWith('/') ? path.value : path.value + '/') + name;
    const crumbs = computed(() => {
      const parts = path.value.split('/').filter(Boolean);
      return [{ name: '根目录', path: '/' }, ...parts.map((p, i) => ({ name: p, path: '/' + parts.slice(0, i + 1).join('/') }))];
    });

    async function load(p = path.value) {
      const list = await attempt(() => api('GET', `${base.value}?${q({ path: p })}`));
      if (list) { entries.value = list; path.value = p; selected.value = new Set(); }
    }
    onMounted(() => load('/'));

    const open = (e) => {
      if (e.isDir) return load(join(e.name));
      if (e.size > 5 * 1024 * 1024) return toast('文件过大，请下载后编辑', 'error');
      attempt(async () => {
        const p = join(e.name);
        editor.value = { path: p, content: await api('GET', `${base.value}/content?${q({ path: p })}`, undefined, { text: true }) };
      });
    };
    const saveEditor = () => attempt(async () => {
      await api('PUT', `${base.value}/content?${q({ path: editor.value.path })}`, editor.value.content);
      editor.value = null;
      load();
    }, '已保存');

    const downloadUrl = (e) => `${base.value}/download?${q({ path: join(e.name) })}`;
    const toggle = (name) => {
      const s = new Set(selected.value);
      s.has(name) ? s.delete(name) : s.add(name);
      selected.value = s;
    };
    const selectedPaths = () => [...selected.value].map(join);

    const mkdir = () => {
      const name = prompt('新文件夹名称');
      if (name) attempt(async () => { await api('POST', `${base.value}/mkdir`, { path: join(name) }); load(); });
    };
    const newFile = () => {
      const name = prompt('新文件名称');
      if (name) editor.value = { path: join(name), content: '' };
    };
    const rename = (e) => {
      const name = prompt('重命名为', e.name);
      if (name && name !== e.name) attempt(async () => { await api('POST', `${base.value}/rename`, { from: join(e.name), to: join(name) }); load(); });
    };
    const remove = () => {
      const paths = selectedPaths();
      if (!paths.length || !confirm(`确定删除选中的 ${paths.length} 项？`)) return;
      attempt(async () => { await api('POST', `${base.value}/delete`, { paths }); load(); }, '已删除');
    };
    const zip = () => {
      const paths = selectedPaths();
      if (!paths.length) return;
      const name = prompt('压缩包名称', 'archive.zip');
      if (name) attempt(async () => { await api('POST', `${base.value}/zip`, { paths, dest: join(name) }); load(); }, '已压缩');
    };
    const unzip = (e) => {
      const dest = prompt('解压到目录', path.value);
      if (dest !== null) attempt(async () => { await api('POST', `${base.value}/unzip`, { path: join(e.name), dest }); load(); }, '已解压');
    };
    const upload = async (ev) => {
      const files = ev.target.files;
      if (!files.length) return;
      const fd = new FormData();
      for (const f of files) fd.append('file', f, f.name);
      uploading.value = true;
      await attempt(() => api('POST', `${base.value}/upload?${q({ path: path.value })}`, fd), '上传完成');
      uploading.value = false;
      ev.target.value = '';
      load();
    };

    const fmtTime = (t) => new Date(t).toLocaleString();
    return {
      path, entries, selected, editor, uploading, fileInput, crumbs, load, open, saveEditor,
      downloadUrl, toggle, mkdir, newFile, rename, remove, zip, unzip, upload, formatSize, fmtTime,
    };
  },
  template: `
    <div class="card files">
      <div class="files-toolbar">
        <div class="crumbs">
          <template v-for="(c, i) in crumbs" :key="c.path">
            <span v-if="i" class="muted">/</span>
            <a href="" @click.prevent="load(c.path)">{{ c.name }}</a>
          </template>
        </div>
        <div class="row">
          <button @click="newFile">新建文件</button>
          <button @click="mkdir">新建文件夹</button>
          <button @click="fileInput.click()" :disabled="uploading">{{ uploading ? '上传中…' : '上传' }}</button>
          <input ref="fileInput" type="file" multiple hidden @change="upload">
          <button :disabled="!selected.size" @click="zip">压缩</button>
          <button class="danger" :disabled="!selected.size" @click="remove">删除</button>
        </div>
      </div>
      <table class="table">
        <thead><tr><th class="w-check"></th><th>名称</th><th>大小</th><th>修改时间</th><th></th></tr></thead>
        <tbody>
          <tr v-if="!entries.length"><td colspan="5" class="empty">空目录</td></tr>
          <tr v-for="e in entries" :key="e.name">
            <td><input type="checkbox" :checked="selected.has(e.name)" @change="toggle(e.name)"></td>
            <td><a href="" @click.prevent="open(e)" :class="{ dir: e.isDir }">{{ e.isDir ? '📁 ' : '' }}{{ e.name }}</a></td>
            <td class="muted">{{ e.isDir ? '' : formatSize(e.size) }}</td>
            <td class="muted">{{ fmtTime(e.modTime) }}</td>
            <td class="actions">
              <a v-if="!e.isDir" :href="downloadUrl(e)">下载</a>
              <a v-if="e.name.toLowerCase().endsWith('.zip')" href="" @click.prevent="unzip(e)">解压</a>
              <a href="" @click.prevent="rename(e)">重命名</a>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-if="editor" class="modal-backdrop">
        <div class="card modal editor">
          <div class="row between"><code>{{ editor.path }}</code>
            <div class="row"><button @click="editor = null">取消</button><button class="primary" @click="saveEditor">保存</button></div>
          </div>
          <textarea v-model="editor.content" class="mono" spellcheck="false"></textarea>
        </div>
      </div>
    </div>
  `,
};
