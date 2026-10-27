import { api, attempt, toast } from '../api.js';
import { confirmAction } from '../dialogs.js';
import { AcmePanel } from './security-acme.js';

// Security page: change the password, and manage the panel's HTTPS
// certificate (upload PEM, or generate a self-signed one) and switches.

const DAY = 24 * 3600 * 1000;
const fmtDate = (t) => new Date(t).toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' });

// Rough strength from length and character classes; only a hint.
function strength(pw) {
  if (!pw) return null;
  const classes = [/[a-z]/, /[A-Z]/, /\d/, /[^A-Za-z0-9]/].filter((r) => r.test(pw)).length;
  const score = (pw.length >= 8) + (pw.length >= 12) + (pw.length >= 16) + (classes >= 2) + (classes >= 3);
  if (pw.length < 8) return { level: 0, label: '太短（至少 8 位）' };
  return [null, { level: 1, label: '弱' }, { level: 1, label: '弱' }, { level: 2, label: '中' }, { level: 3, label: '强' }, { level: 3, label: '很强' }][score] || { level: 1, label: '弱' };
}

const PasswordCard = {
  props: { user: String },
  setup() {
    const { reactive, computed, ref } = Vue;
    const form = reactive({ old: '', next: '', confirm: '' });
    const busy = ref(false);
    const meter = computed(() => strength(form.next));
    const mismatch = computed(() => form.confirm && form.confirm !== form.next);
    const canSubmit = computed(() => form.old && form.next.length >= 8 && form.next === form.confirm && !busy.value);
    async function submit() {
      if (!canSubmit.value) return;
      busy.value = true;
      const r = await attempt(() => api('POST', '/api/auth/password', { oldPassword: form.old, newPassword: form.next }));
      busy.value = false;
      if (!r) return;
      toast(r.loggedOut ? `密码已修改，已退出其他 ${r.loggedOut} 个登录会话` : '密码已修改', 'ok');
      Object.assign(form, { old: '', next: '', confirm: '' });
    }
    return { form, busy, meter, mismatch, canSubmit, submit };
  },
  template: `
    <section class="card sec-card rise" style="--i: 0">
      <header class="sec-head">
        <span class="sec-icon"><Icon name="key" :size="20" /></span>
        <div><h2>修改密码</h2><p class="muted small">当前账号 <strong>{{ user }}</strong>。修改后，其他设备上的登录会话会被立即退出。</p></div>
      </header>
      <form class="form sec-form" @submit.prevent="submit" autocomplete="on">
        <input type="text" :value="user" autocomplete="username" hidden>
        <label>当前密码<input v-model="form.old" type="password" autocomplete="current-password" required></label>
        <div class="grid2">
          <label>新密码
            <input v-model="form.next" type="password" autocomplete="new-password" minlength="8" required>
            <span v-if="meter" class="pw-meter" :class="'lv' + meter.level"><i></i><i></i><i></i><small>{{ meter.label }}</small></span>
          </label>
          <label>确认新密码
            <input v-model="form.confirm" type="password" autocomplete="new-password" required :aria-invalid="mismatch">
            <small v-if="mismatch" class="error">两次输入不一致</small>
          </label>
        </div>
        <div class="row end"><button class="primary" :disabled="!canSubmit"><Icon :name="busy ? 'refresh' : 'check'" :class="{ spin: busy }" />修改密码</button></div>
      </form>
    </section>
  `,
};

export const SecurityView = {
  components: { PasswordCard, AcmePanel },
  props: { user: String },
  setup() {
    const { ref, reactive, computed, onMounted } = Vue;
    const st = ref(null);
    const mode = ref(null); // null | 'upload' | 'self' | 'acme'
    const busy = ref(false);
    const up = reactive({ cert: '', key: '' });
    const self = reactive({ hosts: [location.hostname, 'localhost', '127.0.0.1'].filter((v, i, a) => a.indexOf(v) === i).join(', '), days: 825 });
    const https = location.protocol === 'https:';
    const httpsUrl = `https://${location.host}${location.pathname}#/security`;

    const load = async () => { st.value = await attempt(() => api('GET', '/api/system/tls')) || st.value; };
    onMounted(load);

    const cert = computed(() => st.value?.cert);
    const daysLeft = computed(() => cert.value && Math.floor((new Date(cert.value.notAfter) - Date.now()) / DAY));
    const validity = computed(() => {
      if (!cert.value) return null;
      const total = new Date(cert.value.notAfter) - new Date(cert.value.notBefore);
      const used = Math.min(1, Math.max(0, (Date.now() - new Date(cert.value.notBefore)) / total));
      const d = daysLeft.value;
      return { pct: used * 100, cls: d < 0 ? 'danger' : d < 30 ? 'warn' : '', label: d < 0 ? `已过期 ${-d} 天` : `剩余 ${d} 天` };
    });
    const names = computed(() => cert.value ? [...cert.value.dnsNames, ...cert.value.ips] : []);
    // A self-signed or mismatched certificate can't be verified by the browser.
    const hostCovered = computed(() => !cert.value || names.value.some((n) => n === location.hostname || (n.startsWith('*.') && location.hostname.endsWith(n.slice(1)))));

    async function run(fn, msg) {
      busy.value = true;
      const r = await attempt(fn, msg);
      busy.value = false;
      if (r) { st.value = r; return true; }
      return false;
    }
    const setSettings = (patch) => run(() => api('PUT', '/api/system/tls', { enabled: st.value.enabled, forceHttps: st.value.forceHttps, ...patch }));
    async function toggleEnabled() {
      if (st.value.enabled && st.value.forceHttps && https) {
        if (!(await confirmAction('关闭 HTTPS？', '关闭后需要改用 http:// 地址访问面板，当前页面会跳转过去。', { okText: '关闭 HTTPS' }))) return;
        if (await setSettings({ enabled: false })) location.href = `http://${location.host}${location.pathname}#/security`;
        return;
      }
      if (await setSettings({ enabled: !st.value.enabled })) toast(st.value.enabled ? 'HTTPS 已启用，可以用 https:// 访问了' : 'HTTPS 已关闭', 'ok');
    }
    async function toggleForce() {
      if (!st.value.forceHttps && !(await confirmAction('强制使用 HTTPS？', '之后所有 http:// 访问都会跳转到 https://。如果证书出问题导致无法访问，可以用 -disable-tls 参数启动面板临时关闭 HTTPS。', { okText: '强制 HTTPS' }))) return;
      setSettings({ forceHttps: !st.value.forceHttps });
    }
    async function uploadCert() {
      if (await run(() => api('POST', '/api/system/tls/certificate', { cert: up.cert, key: up.key }), '证书已更新')) {
        mode.value = null;
        Object.assign(up, { cert: '', key: '' });
      }
    }
    async function generate() {
      const hosts = self.hosts.split(/[\s,，]+/).filter(Boolean);
      if (await run(() => api('POST', '/api/system/tls/self-signed', { hosts, days: Number(self.days) }), '已生成自签名证书')) mode.value = null;
    }
    async function removeCert() {
      if (!(await confirmAction('删除证书？', st.value.enabled ? '删除后 HTTPS 会被关闭。' : '证书和私钥文件都会被删除。', { okText: '删除', danger: true }))) return;
      if (https && st.value.active) {
        await run(() => api('DELETE', '/api/system/tls/certificate'));
        location.href = `http://${location.host}${location.pathname}#/security`;
        return;
      }
      run(() => api('DELETE', '/api/system/tls/certificate'), '证书已删除');
    }
    // Read a .pem/.crt/.key file into one of the textareas.
    function readFile(ev, field) {
      const f = ev.target.files?.[0] || ev.dataTransfer?.files?.[0];
      if (!f) return;
      if (f.size > 256 * 1024) return toast('文件过大，不像是 PEM 证书', 'error');
      f.text().then((t) => { up[field] = t.trim() + '\n'; });
      if (ev.target.value) ev.target.value = '';
    }
    const copy = (text) => navigator.clipboard?.writeText(text).then(() => toast('已复制', 'ok'));

    return {
      hostname: location.hostname, st, mode, busy, up, self, https, httpsUrl, cert, validity, names, hostCovered, load,
      toggleEnabled, toggleForce, uploadCert, generate, removeCert, readFile, copy, fmtDate,
    };
  },
  template: `
    <div class="page">
      <header class="page-head"><h1>安全</h1></header>

      <PasswordCard :user="user" />

      <section class="card sec-card rise" style="--i: 1" v-if="st">
        <header class="sec-head">
          <span class="sec-icon" :class="{ on: st.active }"><Icon :name="st.active ? 'lock' : 'unlock'" :size="20" /></span>
          <div>
            <h2>HTTPS 与 SSL 证书</h2>
            <p class="muted small">面板在同一端口同时接受 HTTP 和 HTTPS，开关即时生效，无需重启。
              如果面板前面已有 Caddy / Nginx 等反向代理，请在代理上配置证书，这里保持关闭即可。</p>
          </div>
          <span class="badge no-dot sec-proto" :class="https ? 'running' : ''"><Icon :name="https ? 'lock' : 'unlock'" :size="12" />当前通过 {{ https ? 'HTTPS' : 'HTTP' }} 访问</span>
        </header>

        <p v-if="st.disabledByFlag" class="notice warn"><Icon name="alert" />面板以 <code>-disable-tls</code> 启动，HTTPS 已被临时关闭（设置会保留）。去掉该参数重启后恢复。</p>

        <div class="sec-toggles">
          <label class="switch-row" :class="{ disabled: !cert || busy || st.disabledByFlag }">
            <span><strong>启用 HTTPS</strong><small class="muted">接受 https:// 连接，HTTP 仍然可用</small></span>
            <button type="button" class="switch" role="switch" :aria-checked="st.enabled" :disabled="!cert || busy || st.disabledByFlag" @click="toggleEnabled"><i></i></button>
          </label>
          <label class="switch-row" :class="{ disabled: !st.enabled || !https || busy || st.disabledByFlag }">
            <span><strong>强制 HTTPS</strong>
              <small class="muted" v-if="st.enabled && !https">请先 <a :href="httpsUrl">通过 HTTPS 打开本页</a> 确认证书可用，再开启</small>
              <small class="muted" v-else>http:// 访问自动跳转到 https://</small>
            </span>
            <button type="button" class="switch" role="switch" :aria-checked="st.forceHttps" :disabled="!st.enabled || !https || busy || st.disabledByFlag" @click="toggleForce"><i></i></button>
          </label>
        </div>
        <p v-if="st.active && !https" class="notice"><Icon name="info" />HTTPS 已可用：<a :href="httpsUrl">{{ httpsUrl.replace(/#.*$/, '') }}</a>
          <span v-if="cert?.selfSigned" class="muted">（自签名证书，浏览器会提示不安全，确认继续即可）</span></p>

        <div v-if="cert" class="cert">
          <div class="cert-main">
            <span class="cert-icon"><Icon name="shield-check" :size="28" /></span>
            <div class="cert-title">
              <strong>{{ cert.subject }}</strong>
              <span class="row small">
                <span v-if="cert.selfSigned" class="badge no-dot starting">自签名</span>
                <span v-else class="badge no-dot running">由 {{ cert.issuer }} 签发</span>
                <span class="muted">{{ cert.keyType }}<template v-if="cert.chainLength > 1"> · 证书链 {{ cert.chainLength }} 级</template></span>
              </span>
            </div>
            <div class="row cert-actions">
              <button @click="mode = mode === 'acme' ? null : 'acme'"><Icon name="shield-check" />申请证书</button>
              <button @click="mode = mode === 'upload' ? null : 'upload'"><Icon name="upload" />替换</button>
              <button @click="mode = mode === 'self' ? null : 'self'"><Icon name="refresh" />重新生成</button>
              <button class="danger icon-only" title="删除证书" aria-label="删除证书" @click="removeCert"><Icon name="trash" /></button>
            </div>
          </div>
          <dl class="facts">
            <dt>域名 / IP</dt>
            <dd class="chips"><span v-for="n in names" :key="n" class="chip mono">{{ n }}</span><span v-if="!names.length" class="muted">（未包含 SAN）</span></dd>
            <dt>有效期</dt>
            <dd>
              <div class="row small"><span>{{ fmtDate(cert.notBefore) }} 至 {{ fmtDate(cert.notAfter) }}</span>
                <span class="badge no-dot" :class="validity.cls === 'danger' ? 'crashed' : validity.cls === 'warn' ? 'starting' : 'running'">{{ validity.label }}</span></div>
              <div class="meter cert-meter"><div :class="validity.cls" :style="{ width: validity.pct + '%' }"></div></div>
            </dd>
            <dt>SHA-256 指纹</dt>
            <dd><code class="fp" @click="copy(cert.fingerprint)" title="点击复制">{{ cert.fingerprint }}</code></dd>
          </dl>
          <p v-if="!hostCovered" class="notice warn"><Icon name="alert" />证书不包含当前访问的地址 <code>{{ hostname }}</code>，浏览器会提示证书与域名不匹配。</p>
        </div>
        <div v-else-if="!mode" class="cert-empty">
          <Icon name="shield" :size="34" />
          <p>还没有证书。可以用域名免费申请 Let's Encrypt 证书，上传已有证书，或生成一个自签名证书用于内网。</p>
          <div class="row"><button class="primary" @click="mode = 'acme'"><Icon name="shield-check" />通过域名申请</button>
            <button @click="mode = 'upload'"><Icon name="upload" />上传证书</button><button @click="mode = 'self'"><Icon name="shield" />生成自签名证书</button></div>
        </div>

        <AcmePanel :show-form="mode === 'acme'" @changed="load" @close="mode = null" />
        <Transition name="pop">
          <form v-if="mode === 'upload'" class="form cert-form" @submit.prevent="uploadCert">
            <h3>上传证书</h3>
            <p class="muted small">粘贴或选择 PEM 文件。证书请包含完整证书链（如 fullchain.pem），私钥不能设置密码。也可以把文件直接拖到输入框上。</p>
            <div class="grid2">
              <label>证书（fullchain.pem / .crt）
                <textarea v-model="up.cert" class="mono pem" rows="7" placeholder="-----BEGIN CERTIFICATE-----" spellcheck="false" @dragover.prevent @drop.prevent="readFile($event, 'cert')"></textarea>
                <span class="file-pick"><input type="file" accept=".pem,.crt,.cer,.txt" @change="readFile($event, 'cert')"></span>
              </label>
              <label>私钥（privkey.pem / .key）
                <textarea v-model="up.key" class="mono pem" rows="7" placeholder="-----BEGIN PRIVATE KEY-----" spellcheck="false" @dragover.prevent @drop.prevent="readFile($event, 'key')"></textarea>
                <span class="file-pick"><input type="file" accept=".pem,.key,.txt" @change="readFile($event, 'key')"></span>
              </label>
            </div>
            <div class="row end"><button type="button" @click="mode = null">取消</button>
              <button class="primary" :disabled="!up.cert.trim() || !up.key.trim() || busy"><Icon name="check" />保存证书</button></div>
          </form>
        </Transition>
        <Transition name="pop">
          <form v-if="mode === 'self'" class="form cert-form" @submit.prevent="generate">
            <h3>生成自签名证书</h3>
            <p class="muted small">适合内网或测试。浏览器无法验证自签名证书，首次访问会显示安全警告。{{ cert ? '会替换当前证书。' : '' }}</p>
            <div class="grid2">
              <label>域名 / IP（逗号或空格分隔）<input v-model="self.hosts" class="mono" required></label>
              <label>有效期
                <select v-model="self.days"><option :value="365">1 年</option><option :value="825">825 天（浏览器上限）</option><option :value="3650">10 年</option></select>
              </label>
            </div>
            <div class="row end"><button type="button" @click="mode = null">取消</button>
              <button class="primary" :disabled="busy"><Icon :name="busy ? 'refresh' : 'shield-check'" :class="{ spin: busy }" />生成</button></div>
          </form>
        </Transition>
      </section>
      <div v-else class="skeleton" style="height: 260px"></div>
    </div>
  `,
};
