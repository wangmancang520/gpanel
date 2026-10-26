import { api, attempt, formatSize } from '../api.js';

const jobLabels = {
  downloading: '下载中', verifying: '校验中', extracting: '解压中',
  done: '已完成', failed: '失败', cancelled: '已取消',
};
const active = (j) => !j.finishedAt;

export const RuntimesView = {
  setup() {
    const { ref, computed, onMounted, onUnmounted } = Vue;
    const items = ref([]);
    const releases = ref([]);
    const releasesError = ref('');
    const loadingReleases = ref(false);
    const showAll = ref(false);
    const jobs = ref([]);
    const scanning = ref(false);

    const loadRuntimes = async () => { items.value = await attempt(() => api('GET', '/api/runtimes')) || items.value; };
    const loadReleases = async () => {
      loadingReleases.value = true;
      try {
        releases.value = await api('GET', '/api/runtimes/releases?kind=java');
        releasesError.value = '';
      } catch (e) {
        releasesError.value = e.message;
      } finally {
        loadingReleases.value = false;
      }
    };

    // Poll jobs quickly while something is installing, slowly otherwise;
    // refresh lists when a job finishes.
    let timer, wasActive = new Set();
    async function pollJobs() {
      const list = await api('GET', '/api/runtimes/jobs').catch(() => null);
      if (list) {
        jobs.value = list;
        const nowActive = new Set(list.filter(active).map((j) => j.id));
        if ([...wasActive].some((id) => !nowActive.has(id))) { loadRuntimes(); loadReleases(); }
        wasActive = nowActive;
      }
      timer = setTimeout(pollJobs, wasActive.size ? 700 : 3000);
    }
    onMounted(() => { loadRuntimes(); loadReleases(); pollJobs(); });
    onUnmounted(() => clearTimeout(timer));

    // Group releases by major version: one row per major with JDK/JRE buttons.
    const groups = computed(() => {
      const byMajor = new Map();
      for (const r of releases.value) {
        if (!showAll.value && !r.lts) continue;
        if (!byMajor.has(r.major)) byMajor.set(r.major, { major: r.major, lts: r.lts, version: r.version, items: [] });
        byMajor.get(r.major).items.push(r);
      }
      return [...byMajor.values()];
    });
    const installing = (rel) => jobs.value.some((j) => active(j) && j.release.id === rel.id);

    const install = (rel) => attempt(async () => {
      await api('POST', '/api/runtimes/install', { kind: 'java', releaseId: rel.id });
      clearTimeout(timer);
      pollJobs();
    }, `开始安装 Java ${rel.version} ${rel.imageType.toUpperCase()}`);
    const cancel = (job) => attempt(() => api('DELETE', `/api/runtimes/jobs/${job.id}`));
    const uninstall = (rt) => {
      if (!confirm(`确定卸载 ${rt.name}？将删除 ${rt.home}`)) return;
      attempt(async () => { await api('DELETE', `/api/runtimes/${rt.id}`); await loadRuntimes(); loadReleases(); }, '已卸载');
    };
    const scan = async () => {
      scanning.value = true;
      items.value = await attempt(() => api('POST', '/api/runtimes/scan'), '扫描完成') || items.value;
      scanning.value = false;
    };
    const percent = (j) => (j.total ? Math.min(100, Math.round((j.downloaded / j.total) * 100)) : 0);

    return {
      items, releases, releasesError, loadingReleases, showAll, jobs, groups, scanning,
      install, installing, cancel, uninstall, scan, loadReleases, percent, active, jobLabels, formatSize,
    };
  },
  template: `
    <div class="page">
      <header class="page-head">
        <h1>运行时</h1>
        <button @click="scan" :disabled="scanning"><Icon name="refresh" :class="{ spin: scanning }" />{{ scanning ? '扫描中…' : '重新扫描' }}</button>
      </header>

      <section class="card">
        <h2>已安装</h2>
        <div class="table-scroll"><table class="table">
          <thead><tr><th>名称</th><th>主版本</th><th>路径</th><th>来源</th><th></th></tr></thead>
          <tbody>
            <tr v-if="!items.length"><td colspan="5" class="empty">未检测到运行时，可在下方在线安装</td></tr>
            <tr v-for="r in items" :key="r.id">
              <td class="nowrap"><span class="entry"><Icon name="layers" class="muted" />{{ r.name }}</span></td>
              <td>{{ r.major }}</td>
              <td>
                <code v-if="r.managed" class="path" :title="r.home"><span class="muted">runtimes/java/</span>{{ r.managedPath }}</code>
                <code v-else class="path">{{ r.home }}</code>
              </td>
              <td class="muted nowrap">{{ r.managed ? '面板管理' : '系统' }}</td>
              <td class="actions"><a v-if="r.managed" href="" class="danger" @click.prevent="uninstall(r)"><Icon name="trash" :size="14" />卸载</a></td>
            </tr>
          </tbody>
        </table></div>
      </section>

      <section v-if="jobs.length" class="card">
        <h2>安装任务</h2>
        <div class="jobs">
          <div v-for="j in jobs" :key="j.id" class="job">
            <div class="row between">
              <span>Java {{ j.release.version }} {{ j.release.imageType.toUpperCase() }}
                <span class="muted small" v-if="j.source">· {{ j.source === 'mirror' ? '镜像' : '官方源' }}</span>
              </span>
              <span class="row">
                <span class="badge" :class="'job-' + j.state">{{ jobLabels[j.state] }}</span>
                <button v-if="active(j)" class="link" @click="cancel(j)">取消</button>
              </span>
            </div>
            <div v-if="active(j)" class="progress"><div :style="{ width: percent(j) + '%' }"></div></div>
            <div class="muted small" v-if="j.state === 'downloading'">{{ formatSize(j.downloaded) }} / {{ formatSize(j.total) }}（{{ percent(j) }}%）</div>
            <div class="error small" v-if="j.error && j.state === 'failed'">{{ j.error }}</div>
          </div>
        </div>
      </section>

      <section class="card">
        <div class="row between">
          <h2>在线安装 <span class="muted small">Eclipse Temurin（Adoptium）</span></h2>
          <div class="row">
            <label class="check"><input type="checkbox" v-model="showAll"> 显示非 LTS 版本</label>
            <button @click="loadReleases" :disabled="loadingReleases"><Icon name="refresh" :class="{ spin: loadingReleases }" />刷新</button>
          </div>
        </div>
        <p class="muted small">JRE 体积更小，足够运行游戏服务器；需要编译工具时选 JDK。Minecraft 1.20.5+ 需要 Java 21，1.17–1.20.4 用 Java 17，更早版本用 Java 8。下载后会校验 SHA-256。</p>
        <p v-if="releasesError" class="error">获取版本列表失败：{{ releasesError }}</p>
        <p v-else-if="loadingReleases && !releases.length" class="empty">正在获取版本列表…</p>
        <div v-else class="table-scroll"><table class="table">
          <thead><tr><th>版本</th><th>最新构建</th><th></th></tr></thead>
          <tbody>
            <tr v-for="g in groups" :key="g.major">
              <td><strong>Java {{ g.major }}</strong> <span v-if="g.lts" class="badge lts no-dot">LTS</span></td>
              <td class="muted">{{ g.version }}</td>
              <td class="actions">
                <template v-for="r in g.items" :key="r.id">
                  <span v-if="r.installed" class="muted install-btn"><Icon name="check" />{{ r.imageType.toUpperCase() }} 已安装</span>
                  <button v-else class="install-btn" :disabled="installing(r)" @click="install(r)">
                    <Icon :name="installing(r) ? 'refresh' : 'download'" :class="{ spin: installing(r) }" />{{ installing(r) ? '安装中…' : '安装 ' + r.imageType.toUpperCase() }}
                    <span class="muted small">{{ formatSize(r.size) }}</span>
                  </button>
                </template>
              </td>
            </tr>
          </tbody>
        </table></div>
      </section>
    </div>
  `,
};
