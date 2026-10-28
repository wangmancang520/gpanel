import { api, attempt, stateLabels, formatSize, grant, playersChip } from '../api.js';

// Home page: host configuration, live load and the running instances.
// Data comes from /api/system (sampled every 2s server-side) and /api/instances.

const pct = (part, whole) => (whole ? (part / whole) * 100 : 0);
const fmtPct = (v) => (v < 10 ? v.toFixed(1) : Math.round(v)) + '%';
const clock = (t) => new Date(t).toLocaleTimeString('zh-CN', { hour12: false });

function formatDuration(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  if (d) return `${d} 天 ${h} 小时`;
  if (h) return `${h} 小时 ${m} 分`;
  if (m) return `${m} 分 ${s % 60} 秒`;
  return `${s} 秒`;
}

// Single-series trend line with a fixed y range (0..max) and a hover readout.
// The SVG stretches to the container; the end dot and crosshair are HTML so
// they don't distort with it.
const Sparkline = {
  props: { points: Array, max: Number, format: Function, label: String },
  setup(props) {
    const { ref, computed } = Vue;
    const hover = ref(-1);
    const n = computed(() => props.points.length);
    const x = (i) => (n.value > 1 ? (i / (n.value - 1)) * 100 : 100);
    const y = (v) => 100 - Math.min(100, pct(v, props.max));
    const line = computed(() => props.points.map((p, i) => `${i ? 'L' : 'M'}${x(i)},${y(p.v)}`).join(''));
    const area = computed(() => (n.value ? `${line.value}L100,100L0,100Z` : ''));
    const move = (e) => {
      const r = e.currentTarget.getBoundingClientRect();
      hover.value = n.value ? Math.round(((e.clientX - r.left) / r.width) * (n.value - 1)) : -1;
    };
    const active = computed(() => props.points[hover.value >= 0 ? hover.value : n.value - 1]);
    const activeIndex = computed(() => (hover.value >= 0 ? hover.value : n.value - 1));
    return { hover, line, area, move, x, y, active, activeIndex, clock };
  },
  template: `
    <div class="spark" @mousemove="move" @mouseleave="hover = -1" role="img" :aria-label="label">
      <svg viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
        <path :d="area" class="spark-area" />
        <path :d="line" class="spark-line" vector-effect="non-scaling-stroke" />
      </svg>
      <template v-if="active">
        <div v-if="hover >= 0" class="spark-cross" :style="{ left: x(activeIndex) + '%' }"></div>
        <div class="spark-dot" :style="{ left: x(activeIndex) + '%', top: y(active.v) + '%' }"></div>
        <div v-if="hover >= 0" class="spark-tip" :class="{ flip: x(activeIndex) > 60 }" :style="{ left: x(activeIndex) + '%' }">
          <span class="muted">{{ clock(active.t) }}</span> {{ format(active.v) }}
        </div>
      </template>
      <div v-if="points.length < 2" class="spark-empty muted small">采集中…</div>
    </div>
  `,
};

// Horizontal meter; the fill turns warning/danger past 80% / 90%.
const Meter = {
  props: { value: Number },
  template: `
    <div class="meter" role="meter" :aria-valuenow="Math.round(value)" aria-valuemin="0" aria-valuemax="100">
      <div :class="{ warn: value >= 80 && value < 90, danger: value >= 90 }" :style="{ width: Math.min(100, value) + '%' }"></div>
    </div>
  `,
};

export const Dashboard = {
  components: { Sparkline, Meter },
  setup() {
    const { ref, computed, onMounted, onUnmounted } = Vue;
    const sys = ref(null);
    const instances = ref([]);
    const status = ref({}); // Minecraft players by instance ID
    const now = ref(Date.now());

    const load = async () => {
      const [s, list, st] = await Promise.all([
        api('GET', '/api/system').catch(() => null),
        api('GET', '/api/instances').catch(() => null),
        api('GET', '/api/status').catch(() => null),
      ]);
      if (s) sys.value = s;
      if (list) instances.value = list;
      if (st) status.value = st;
      now.value = Date.now();
    };
    let timer;
    onMounted(() => { load(); timer = setInterval(load, 2000); });
    onUnmounted(() => clearInterval(timer));

    const running = computed(() => instances.value.filter((i) => ['starting', 'running', 'stopping'].includes(i.state)));
    const crashed = computed(() => instances.value.filter((i) => i.state === 'crashed'));
    const cpuPoints = computed(() => (sys.value?.history || []).map((s) => ({ t: s.t, v: s.cpu })));
    const memPoints = computed(() => (sys.value?.history || []).map((s) => ({ t: s.t, v: s.mem })));
    const usage = (id) => sys.value?.instances?.[id];

    // Per logical CPU: current load and the history, one series per core.
    const CORE_LIMIT = 64;
    const allCores = ref(false);
    const cores = computed(() => sys.value?.load.cores || []);
    const coreSeries = computed(() => {
      const hist = (sys.value?.history || []).filter((s) => s.cores?.length === cores.value.length);
      return cores.value.map((_, i) => hist.map((s) => ({ t: s.t, v: s.cores[i] })));
    });
    const shownCores = computed(() => (allCores.value ? cores.value : cores.value.slice(0, CORE_LIMIT)));
    const busiest = computed(() => {
      let i = -1;
      cores.value.forEach((v, j) => { if (i < 0 || v > cores.value[i]) i = j; });
      return i < 0 ? null : { i, v: cores.value[i] };
    });
    // Cores near full load matter for game servers: their main thread
    // usually runs on one core, which the overall average hides.
    const hot = (v) => v >= 90;

    const stop = (inst) => attempt(async () => { await api('POST', `/api/instances/${inst.id}/stop`); await load(); });

    return {
      sys, instances, running, crashed, cpuPoints, memPoints, usage, stop, now,
      cores, coreSeries, shownCores, allCores, busiest, hot, CORE_LIMIT,
      pct, fmtPct, formatSize, formatDuration, stateLabels, grant,
      chip: (id) => playersChip(status.value[id]),
    };
  },
  template: `
    <div class="page" v-if="sys">
      <header class="page-head"><h1>概览</h1></header>

      <p v-if="!sys.supported" class="card muted">当前系统（{{ sys.host.os }}）暂不支持负载监控，仅显示基本信息。</p>

      <div class="tiles">
        <section class="card tile rise" style="--i: 0" v-if="sys.supported">
          <div class="tile-label"><Icon name="cpu" />CPU</div>
          <div class="tile-value">{{ fmtPct(sys.load.cpu) }}</div>
          <div class="muted small">负载 {{ sys.load.load1.toFixed(2) }} / {{ sys.load.load5.toFixed(2) }} / {{ sys.load.load15.toFixed(2) }}</div>
          <div v-if="busiest" class="small" :class="hot(busiest.v) ? 'hot-text' : 'muted'">最高单核 {{ fmtPct(busiest.v) }}<span class="muted">（#{{ busiest.i }}）</span></div>
          <Sparkline :points="cpuPoints" :max="100" :format="fmtPct" label="最近 3 分钟 CPU 使用率" />
        </section>
        <section class="card tile rise" style="--i: 1" v-if="sys.supported">
          <div class="tile-label"><Icon name="memory" />内存</div>
          <div class="tile-value">{{ fmtPct(pct(sys.load.memUsed, sys.load.memTotal)) }}</div>
          <div class="muted small">{{ formatSize(sys.load.memUsed) }} / {{ formatSize(sys.load.memTotal) }}
            <template v-if="sys.load.swapTotal"> · 交换 {{ formatSize(sys.load.swapUsed) }}</template>
          </div>
          <Sparkline :points="memPoints" :max="sys.load.memTotal" :format="formatSize" label="最近 3 分钟内存占用" />
        </section>
        <section class="card tile rise" style="--i: 2" v-if="sys.load.diskTotal">
          <div class="tile-label"><Icon name="disk" />磁盘 <span class="muted small">数据目录所在分区</span></div>
          <div class="tile-value">{{ fmtPct(pct(sys.load.diskUsed, sys.load.diskTotal)) }}</div>
          <div class="muted small">已用 {{ formatSize(sys.load.diskUsed) }} / {{ formatSize(sys.load.diskTotal) }}</div>
          <Meter :value="pct(sys.load.diskUsed, sys.load.diskTotal)" />
        </section>
        <section class="card tile rise" style="--i: 3">
          <div class="tile-label"><Icon name="server" />实例</div>
          <div class="tile-value">{{ running.length }} <span class="tile-unit muted">/ {{ instances.length }} 运行中</span></div>
          <div class="small" :class="crashed.length ? 'error' : 'muted'">
            {{ crashed.length ? crashed.length + ' 个已崩溃' : '没有崩溃的实例' }}
          </div>
          <a href="#/instances" class="small row" style="gap: 4px; margin-top: auto">管理实例<Icon name="arrow" :size="14" /></a>
        </section>
      </div>

      <section class="card rise cores-card" style="--i: 4" v-if="cores.length">
        <header class="cores-head">
          <h2><Icon name="cpu" />CPU 核心</h2>
          <span class="muted small">{{ cores.length }} 个逻辑处理器 · 最近 3 分钟 · 纵轴 0–100%</span>
        </header>
        <div class="cores" :class="{ dense: cores.length > 32 }">
          <div v-for="(v, i) in shownCores" :key="i" class="core" :class="{ hot: hot(v) }">
            <div class="core-head">
              <span class="muted">#{{ i }}</span>
              <span class="core-val">{{ fmtPct(v) }}</span>
            </div>
            <Sparkline :points="coreSeries[i]" :max="100" :format="fmtPct" :label="'核心 ' + i + ' 使用率'" />
          </div>
        </div>
        <button v-if="cores.length > CORE_LIMIT" class="link small" @click="allCores = !allCores">
          {{ allCores ? '收起' : '显示全部 ' + cores.length + ' 个' }}
        </button>
      </section>

      <section class="card rise" style="--i: 5">
        <h2>运行中的实例</h2>
        <div v-if="!running.length" class="empty"><Icon name="server" :size="32" /><p>没有正在运行的实例。<a href="#/instances">去启动一个</a></p></div>
        <div v-else class="table-scroll"><table class="table">
          <thead><tr><th>名称</th><th>状态</th><th>玩家</th><th>PID</th><th>运行时长</th><th class="num">CPU</th><th class="num">内存</th><th></th></tr></thead>
          <tbody>
            <tr v-for="it in running" :key="it.id">
              <td class="nowrap"><a :href="'#/instances/' + it.id" class="dir">{{ it.name }}</a></td>
              <td><span class="badge" :class="it.state">{{ stateLabels[it.state] }}</span></td>
              <td class="nowrap"><span v-if="chip(it.id)" class="players-chip" :class="{ starting: chip(it.id).starting, empty: chip(it.id).empty }" :title="chip(it.id).title"><Icon name="users" :size="14" />{{ chip(it.id).text }}</span><span v-else class="muted">—</span></td>
              <td class="muted">{{ it.pid || '—' }}</td>
              <td class="nowrap">{{ it.startedAt ? formatDuration(now - new Date(it.startedAt)) : '—' }}</td>
              <td class="num">{{ usage(it.id) ? fmtPct(usage(it.id).cpu) : '—' }}</td>
              <td class="num nowrap">{{ usage(it.id) ? formatSize(usage(it.id).mem) : '—' }}
                <span v-if="usage(it.id)?.procs > 1" class="muted small" :title="usage(it.id).procs + ' 个进程'">×{{ usage(it.id).procs }}</span>
              </td>
              <td class="actions">
                <template v-if="grant(it.id).console">
                  <a :href="'#/instances/' + it.id + '/terminal'"><Icon name="terminal" :size="14" />控制台</a>
                  <a href="" v-if="it.state === 'running'" @click.prevent="stop(it)"><Icon name="stop" :size="14" />停止</a>
                </template>
              </td>
            </tr>
          </tbody>
        </table></div>
      </section>

      <section class="card rise" style="--i: 6">
        <h2>系统信息</h2>
        <dl class="facts">
          <dt>主机名</dt><dd>{{ sys.host.hostname || '—' }}</dd>
          <dt>操作系统</dt><dd>{{ sys.host.os }}<span v-if="sys.host.kernel" class="muted"> · 内核 {{ sys.host.kernel }}</span></dd>
          <dt>架构</dt><dd>{{ sys.host.arch }}</dd>
          <dt>CPU</dt><dd>{{ sys.host.cpuModel || '未知型号' }} <span class="muted">· {{ sys.host.cpuCores }} 线程</span></dd>
          <dt v-if="sys.host.memTotal">内存</dt><dd v-if="sys.host.memTotal">{{ formatSize(sys.host.memTotal) }}<span v-if="sys.host.swapTotal" class="muted"> · 交换 {{ formatSize(sys.host.swapTotal) }}</span></dd>
          <dt v-if="sys.host.bootTime">开机时长</dt><dd v-if="sys.host.bootTime">{{ formatDuration(now - new Date(sys.host.bootTime)) }}</dd>
          <dt>GPanel 版本</dt><dd>{{ sys.host.version }} <span class="muted">· {{ sys.host.goVersion }}</span></dd>
          <dt>面板运行时长</dt><dd>{{ formatDuration(now - new Date(sys.host.startedAt)) }}</dd>
          <dt>数据目录</dt><dd><code class="path">{{ sys.host.dataDir }}</code></dd>
        </dl>
      </section>
    </div>
    <div class="page" v-else aria-busy="true">
      <header class="page-head"><h1>概览</h1></header>
      <div class="tiles"><div v-for="i in 4" :key="i" class="skeleton skeleton-tile"></div></div>
      <div class="skeleton" style="height: 160px"></div>
    </div>
  `,
};
