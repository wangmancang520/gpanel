import { api, attempt, toast } from '../api.js';

// "申请证书" panel on the security page: Let's Encrypt (ACME) settings,
// the issuing job's progress, and the manual-DNS record table.

const METHODS = [
  { key: 'http', title: 'HTTP 验证', icon: 'server', desc: '临时监听 80 端口完成验证。要求域名已解析到本机、公网 80 端口可访问且未被占用。' },
  { key: 'dns-api', title: 'DNS 自动验证', icon: 'refresh', desc: '用 DNS 服务商的 API 自动添加 TXT 记录。无需开放端口，支持通配符域名。' },
  { key: 'dns-manual', title: 'DNS 手动验证', icon: 'edit', desc: '面板给出 TXT 记录，你到 DNS 控制台手动添加。通用，但每次续期都要手动操作。' },
];
const CAS = [
  { key: 'letsencrypt', label: "Let's Encrypt" },
  { key: 'letsencrypt-staging', label: "Let's Encrypt 测试环境（证书不受信任，用于调试，不占正式额度）" },
  { key: 'custom', label: '自定义 ACME 地址' },
];
const STEP_ICON = { info: 'info', ok: 'check', warn: 'alert', error: 'alert' };
const fmtTime = (t) => new Date(t).toLocaleTimeString('zh-CN', { hour12: false });
const fmtDate = (t) => new Date(t).toLocaleString('zh-CN', { hour12: false, year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' });

export const AcmePanel = {
  props: { showForm: Boolean },
  emits: ['changed', 'close'],
  setup(props, { emit }) {
    const { ref, reactive, computed, onMounted, onUnmounted, watch } = Vue;
    const s = ref(null);
    const busy = ref(false);
    const checking = ref(false);
    const form = reactive({
      domains: '', email: '', ca: 'letsencrypt', customUrl: '', method: 'http', provider: 'aliyun',
      credentials: {}, httpPort: 80, autoRenew: true, autoEnable: true,
    });

    function fill(cfg) {
      const known = CAS.some((c) => c.key === cfg.directory);
      // Suggest the address the panel is opened with, if it is a domain name.
      const host = location.hostname;
      const guess = host.includes('.') && !/^[\d.]+$/.test(host) && !host.includes(':') ? host : '';
      Object.assign(form, {
        domains: cfg.domains?.length ? cfg.domains.join('\n') : guess,
        email: cfg.email || '', ca: !cfg.directory ? 'letsencrypt' : known ? cfg.directory : 'custom',
        customUrl: known ? '' : cfg.directory || '', method: cfg.method || 'http', provider: cfg.provider || 'aliyun',
        credentials: { ...(cfg.credentials || {}) }, httpPort: cfg.httpPort || 80,
        autoRenew: cfg.domains?.length ? cfg.autoRenew : true, autoEnable: cfg.domains?.length ? cfg.autoEnable : true,
      });
    }
    let timer;
    const active = computed(() => s.value?.job && !s.value.job.finishedAt);
    async function load(first = false) {
      const r = await api('GET', '/api/system/tls/acme').catch(() => null);
      if (!r) return;
      const wasActive = active.value;
      s.value = r;
      if (first) fill(r.config);
      if (wasActive && !active.value) {
        emit('changed');
        if (r.job.state === 'done') toast('证书申请成功', 'ok');
      }
    }
    onMounted(() => load(true));
    watch(active, (on) => { clearInterval(timer); if (on) timer = setInterval(load, 1500); }, { immediate: false });
    onUnmounted(() => clearInterval(timer));

    const provider = computed(() => s.value?.providers.find((p) => p.name === form.provider));
    const domainList = computed(() => form.domains.split(/[\s,，]+/).map((d) => d.trim()).filter(Boolean));
    const hasWildcard = computed(() => domainList.value.some((d) => d.startsWith('*.')));
    const job = computed(() => s.value?.job);
    const cfg = computed(() => s.value?.config);
    const configured = computed(() => cfg.value?.domains?.length > 0);

    function body() {
      return {
        domains: domainList.value, email: form.email.trim(),
        directory: form.ca === 'custom' ? form.customUrl.trim() : form.ca,
        method: form.method, provider: form.method === 'dns-api' ? form.provider : '',
        credentials: form.method === 'dns-api' ? form.credentials : {},
        httpPort: Number(form.httpPort) || 80, autoRenew: form.method !== 'dns-manual' && form.autoRenew, autoEnable: form.autoEnable,
      };
    }
    async function save(andIssue) {
      busy.value = true;
      let r = await attempt(() => api('PUT', '/api/system/tls/acme', body()));
      if (r && andIssue) r = await attempt(() => api('POST', '/api/system/tls/acme/issue'));
      busy.value = false;
      if (!r) return;
      s.value = r;
      // Secrets are never sent back; clear what was typed so it isn't kept around.
      for (const f of provider.value?.fields || []) if (f.secret) form.credentials[f.key] = '';
      if (!andIssue) toast('设置已保存', 'ok');
      else emit('close'); // collapse the form so the progress is in view
    }
    const issue = async () => { const r = await attempt(() => api('POST', '/api/system/tls/acme/issue')); if (r) s.value = r; };
    const cont = async () => { const r = await attempt(() => api('POST', '/api/system/tls/acme/continue')); if (r) s.value = r; };
    const cancel = async () => { const r = await attempt(() => api('DELETE', '/api/system/tls/acme/job')); if (r) s.value = r; };
    async function checkDNS() {
      checking.value = true;
      const r = await attempt(() => api('POST', '/api/system/tls/acme/check-dns'));
      checking.value = false;
      if (r) s.value = r;
    }
    const copy = (text) => navigator.clipboard?.writeText(text).then(() => toast('已复制', 'ok'));
    const caLabel = (dir) => CAS.find((c) => c.key === dir)?.label.replace(/（.*/, '') || dir;
    const methodTitle = (m) => METHODS.find((x) => x.key === m)?.title || m;

    return {
      s, form, busy, checking, METHODS, CAS, STEP_ICON, provider, domainList, hasWildcard, job, cfg, configured, active,
      save, issue, cont, cancel, checkDNS, copy, fmtTime, fmtDate, caLabel, methodTitle,
    };
  },
  template: `
    <div class="acme" v-if="s">
      <div v-if="configured && !showForm && !job" class="acme-summary">
        <Icon name="shield-check" :size="18" />
        <span>{{ caLabel(cfg.directory) }} · {{ methodTitle(cfg.method) }} · {{ cfg.domains.join('、') }}</span>
        <span class="muted small" v-if="cfg.lastIssued">上次签发 {{ fmtDate(cfg.lastIssued) }}</span>
      </div>

      <form v-if="showForm" class="form cert-form" @submit.prevent="save(true)">
        <div class="row between"><h3>通过域名申请证书</h3>
          <button type="button" class="link" @click="$emit('close')" aria-label="收起"><Icon name="x" /></button></div>
        <p class="muted small">证书由 Let's Encrypt 免费签发，有效期 90 天。申请前请确认域名已解析到这台服务器（HTTP 验证），或托管在下面支持的 DNS 服务商（DNS 自动验证）。</p>
        <div class="grid2">
          <label>域名（每行一个，或用逗号分隔）
            <textarea v-model="form.domains" class="mono" rows="3" placeholder="panel.example.com&#10;*.example.com" spellcheck="false"></textarea>
            <small class="muted">通配符如 <code>*.example.com</code> 只能用 DNS 验证，且不包含 example.com 本身。</small>
          </label>
          <div class="form">
            <label>邮箱（可选）<input v-model="form.email" type="email" placeholder="用于接收证书到期提醒"></label>
            <label>证书颁发机构
              <select v-model="form.ca"><option v-for="c in CAS" :key="c.key" :value="c.key">{{ c.label }}</option></select>
              <input v-if="form.ca === 'custom'" v-model="form.customUrl" class="mono" placeholder="https://…/directory" required>
            </label>
          </div>
        </div>

        <div class="method-cards" role="radiogroup" aria-label="验证方式">
          <button v-for="m in METHODS" :key="m.key" type="button" role="radio" :aria-checked="form.method === m.key"
            class="method-card" :class="{ on: form.method === m.key, disabled: m.key === 'http' && hasWildcard }"
            :disabled="m.key === 'http' && hasWildcard" @click="form.method = m.key">
            <span class="row"><Icon :name="m.icon" /><strong>{{ m.title }}</strong></span>
            <small class="muted">{{ m.desc }}</small>
          </button>
        </div>

        <div v-if="form.method === 'http'" class="grid2">
          <label>验证端口<input v-model.number="form.httpPort" type="number" min="1" max="65535">
            <small class="muted">Let's Encrypt 固定访问 80 端口；只有在端口转发或反向代理时才需要改。面板没有 root 权限时可能无法监听 80 端口，可以让反向代理把 <code>/.well-known/acme-challenge/</code> 转发到面板端口。</small>
          </label>
        </div>
        <div v-if="form.method === 'dns-api'" class="dns-api">
          <div class="seg provider-seg" role="radiogroup" aria-label="DNS 服务商">
            <button v-for="p in s.providers" :key="p.name" type="button" :class="{ pressed: form.provider === p.name }" :aria-checked="form.provider === p.name" role="radio" @click="form.provider = p.name">{{ p.label }}</button>
          </div>
          <p class="muted small">{{ provider?.help }}</p>
          <div class="grid2">
            <label v-for="f in provider?.fields" :key="form.provider + f.key">{{ f.label }}
              <input v-model="form.credentials[f.key]" :type="f.secret ? 'password' : 'text'" class="mono" autocomplete="off"
                :placeholder="f.secret && cfg.provider === form.provider && cfg.credentialsSet[f.key] ? '已保存，留空则不修改' : ''">
            </label>
          </div>
        </div>
        <p v-if="form.method === 'dns-manual'" class="notice warn"><Icon name="alert" />手动验证无法自动续期，证书到期前需要回到这里重新申请（每 60–90 天一次）。</p>

        <div class="row">
          <label class="check"><input type="checkbox" v-model="form.autoRenew" :disabled="form.method === 'dns-manual'"> 到期前 30 天自动续期</label>
          <label class="check"><input type="checkbox" v-model="form.autoEnable"> 签发后自动启用 HTTPS</label>
        </div>
        <div class="row end">
          <button type="button" @click="save(false)" :disabled="busy || active">保存设置</button>
          <button class="primary" :disabled="busy || active || !domainList.length"><Icon :name="busy ? 'refresh' : 'shield-check'" :class="{ spin: busy }" />申请证书</button>
        </div>
      </form>

      <Transition name="pop">
        <div v-if="job" class="acme-job" :class="'job-' + job.state">
          <div class="row between">
            <strong class="row">
              <Icon :name="job.state === 'done' ? 'check' : job.state === 'failed' ? 'alert' : job.state === 'cancelled' ? 'x' : 'refresh'" :class="{ spin: job.state === 'running' }" />
              {{ { running: job.renewal ? '正在自动续期…' : '正在申请证书…', waiting: '等待添加 DNS 记录', done: '申请成功', failed: '申请失败', cancelled: '已取消' }[job.state] }}
            </strong>
            <span class="row">
              <button v-if="!job.finishedAt" class="link danger" @click="cancel">取消</button>
              <button v-else-if="configured" class="link" @click="issue" :disabled="busy"><Icon name="refresh" />重新申请</button>
            </span>
          </div>
          <ol class="steps">
            <li v-for="(st, i) in job.steps" :key="i" :class="'lv-' + st.level">
              <Icon :name="STEP_ICON[st.level]" :size="14" /><span>{{ st.text }}</span><time class="muted">{{ fmtTime(st.time) }}</time>
            </li>
          </ol>
          <div v-if="job.records?.length && job.state === 'waiting'" class="txt-records">
            <p class="small">在域名的 DNS 控制台添加以下 TXT 记录（同名记录有多条时都要添加）。添加后可以先点「检查解析」，看到全部生效再开始验证：</p>
            <div class="table-scroll"><table class="table">
              <thead><tr><th>主机记录</th><th>类型</th><th>记录值</th><th></th></tr></thead>
              <tbody><tr v-for="(r, i) in job.records" :key="i">
                <td><code class="copyable" @click="copy(r.name)" title="点击复制">{{ r.name }}</code></td>
                <td>TXT</td>
                <td><code class="copyable" @click="copy(r.value)" title="点击复制">{{ r.value }}</code></td>
                <td class="nowrap"><span v-if="r.found === true" class="badge running">已生效</span><span v-else-if="r.found === false" class="badge starting">未查到</span></td>
              </tr></tbody>
            </table></div>
            <p class="muted small">部分 DNS 控制台的「主机记录」只填前缀，例如 <code>{{ job.records[0].name.split('.').slice(0, -2).join('.') }}</code>。</p>
            <div class="row end">
              <button @click="checkDNS" :disabled="checking"><Icon name="search" :class="{ spin: checking }" />检查解析</button>
              <button class="primary" @click="cont"><Icon name="check" />已添加，开始验证</button>
            </div>
          </div>
        </div>
      </Transition>
      <p v-if="!job && cfg.lastError" class="notice warn"><Icon name="alert" />上次申请失败：{{ cfg.lastError }}</p>
    </div>
  `,
};
