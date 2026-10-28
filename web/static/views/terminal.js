import { TermMotion } from '../term-motion.js';

// Web console: xterm.js attached to the instance's PTY over WebSocket.
// Protocol is documented in internal/instance/http.go. Output goes through
// TermMotion (term-motion.js): old lines are pushed up and new ones slide in.

const REPLAY_MS = 600; // after connecting, output is history being replayed

export const TerminalPane = {
  props: { id: String, state: String },
  emits: ['status', 'start'],
  setup(props, { emit }) {
    const { ref, computed, onMounted, onUnmounted } = Vue;
    const clip = ref(null);
    const el = ref(null);
    const phase = ref('connecting'); // connecting | open | reconnecting
    const attempts = ref(0);
    const hasOutput = ref(false);
    const revealed = ref(false); // fades the terminal in once history is drawn
    const command = ref('');
    let term, fit, motion, ws, retry, resizeObs, layoutFrame, closed = false;
    let replayUntil = 0;

    const connected = computed(() => phase.value === 'open');
    const send = (msg) => ws && ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(msg));
    const sendSize = () => send({ type: 'resize', ...motion.serverSize() });

    function connect() {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      ws = new WebSocket(`${proto}://${location.host}/api/instances/${props.id}/terminal`);
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => {
        phase.value = 'open';
        attempts.value = 0;
        hasOutput.value = false;
        term.reset();
        motion.reset();
        replayUntil = performance.now() + REPLAY_MS;
        sendSize();
        setTimeout(() => (revealed.value = true), 120);
      };
      ws.onmessage = (ev) => {
        if (typeof ev.data === 'string') {
          const msg = JSON.parse(ev.data);
          if (msg.type === 'status') emit('status', msg.data);
          return;
        }
        const data = new Uint8Array(ev.data);
        if (!data.length) return;
        hasOutput.value = true;
        motion.feed(data, { instant: performance.now() < replayUntil });
      };
      ws.onclose = () => {
        if (closed) return;
        phase.value = phase.value === 'open' || attempts.value ? 'reconnecting' : 'connecting';
        attempts.value++;
        retry = setTimeout(connect, Math.min(2000 * attempts.value, 10000));
      };
    }

    // The visible box can only be split into rows once a row has rendered.
    const layout = () => {
      cancelAnimationFrame(layoutFrame);
      if (!motion.layout()) layoutFrame = requestAnimationFrame(layout);
    };

    onMounted(() => {
      term = new Terminal({
        convertEol: false, cursorBlink: true, scrollback: 5000, fontSize: 13,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
        theme: { background: '#0f1115', foreground: '#d8dee9' },
        smoothScrollDuration: matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : 120, // mouse-wheel scrolling
      });
      fit = new FitAddon.FitAddon();
      term.loadAddon(fit);
      el.value.style.height = `${clip.value.clientHeight}px`;
      term.open(el.value);
      fit.fit();
      motion = new TermMotion(term, fit, clip.value, el.value);
      term.onData((data) => send({ type: 'input', data }));
      term.onResize(sendSize);
      resizeObs = new ResizeObserver(layout);
      resizeObs.observe(clip.value);
      layout();
      connect();
    });

    onUnmounted(() => {
      closed = true;
      clearTimeout(retry);
      cancelAnimationFrame(layoutFrame);
      resizeObs?.disconnect();
      motion?.dispose();
      ws?.close();
      term?.dispose();
    });

    // What to say over the terminal while there's nothing (useful) in it.
    const overlay = computed(() => {
      if (phase.value === 'connecting') return { kind: 'busy', title: '正在连接终端…' };
      if (phase.value === 'reconnecting') return { kind: 'busy', title: '连接已断开，正在重连…', detail: attempts.value > 1 ? `第 ${attempts.value} 次尝试` : '', dim: true };
      if (hasOutput.value) return null;
      if (['stopped', 'crashed'].includes(props.state)) {
        return { kind: 'idle', title: props.state === 'crashed' ? '实例已崩溃退出' : '实例未运行', detail: '启动后，服务器的控制台输出会实时显示在这里。' };
      }
      if (props.state) return { kind: 'wait', title: '已连接，等待服务器输出…' };
      return null;
    });

    // Line input for touch devices / pasting whole commands.
    const sendCommand = () => {
      if (!command.value) return;
      send({ type: 'input', data: command.value + '\r' });
      command.value = '';
      term.focus();
    };
    return { clip, el, phase, connected, revealed, overlay, command, sendCommand, emit };
  },
  template: `
    <div class="terminal-wrap">
      <!-- .term-clip is the visible area. The terminal's own box (.term-fit,
           no padding, sized by TermMotion) is taller and starts above it:
           the extra rows are what the push reveals. -->
      <div class="term-host" :class="{ revealed }">
        <div ref="clip" class="term-clip"><div ref="el" class="term-fit"></div></div>
        <Transition name="fade">
          <div v-if="overlay" class="term-overlay" :class="[overlay.kind, { dim: overlay.dim }]" role="status">
            <span v-if="overlay.kind !== 'idle'" class="term-spinner"></span>
            <Icon v-else :name="state === 'crashed' ? 'alert' : 'power'" :size="30" />
            <p class="term-overlay-title">{{ overlay.title }}</p>
            <p v-if="overlay.detail" class="term-overlay-detail">{{ overlay.detail }}</p>
            <button v-if="overlay.kind === 'idle'" class="primary" @click="emit('start')"><Icon name="play" />启动实例</button>
          </div>
        </Transition>
      </div>
      <form class="command-bar" @submit.prevent="sendCommand">
        <span class="conn" :class="phase"><span class="dot" :class="{ on: connected }"></span>{{ connected ? '已连接' : phase === 'reconnecting' ? '重连中…' : '连接中…' }}</span>
        <input v-model="command" class="mono" :placeholder="connected ? '输入命令后回车发送' : '等待终端连接…'" :disabled="!connected">
        <button class="primary" :disabled="!connected || !command"><Icon name="send" />发送</button>
      </form>
    </div>
  `,
};
