// Web console: xterm.js attached to the instance's PTY over WebSocket.
// Protocol is documented in internal/instance/http.go.
//
// Output is written in small batches; when a batch scrolls the screen, the
// rows glide up and the new lines fade in, so a flood of errors reads as
// "scrolling" rather than a glitching terminal. Skipped for the history
// replayed on connect, while scrolled back, and with reduced motion.

const FLUSH_MS = 40;   // batch output this long before writing
const SLIDE_MS = 160;  // glide of the screen per batch
const MAX_SLIDE = 3;   // rows; a bigger burst still glides only this far
const REPLAY_MS = 600; // after connecting, output is history being replayed

export const TerminalPane = {
  props: { id: String, state: String },
  emits: ['status', 'start'],
  setup(props, { emit }) {
    const { ref, computed, onMounted, onUnmounted } = Vue;
    const el = ref(null);
    const veil = ref(null);
    const phase = ref('connecting'); // connecting | open | reconnecting
    const attempts = ref(0);
    const hasOutput = ref(false);
    const revealed = ref(false); // fades the terminal in once history is drawn
    const command = ref('');
    let term, fit, ws, retry, resizeObs, closed = false;
    let queue = [], flushTimer = 0, lineFeeds = 0, replayUntil = 0, slide;
    const reduceMotion = matchMedia('(prefers-reduced-motion: reduce)');

    const connected = computed(() => phase.value === 'open');
    const send = (msg) => ws && ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(msg));

    function connect() {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      ws = new WebSocket(`${proto}://${location.host}/api/instances/${props.id}/terminal`);
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => {
        phase.value = 'open';
        attempts.value = 0;
        hasOutput.value = false;
        queue = [];
        term.reset();
        replayUntil = performance.now() + REPLAY_MS;
        send({ type: 'resize', cols: term.cols, rows: term.rows });
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
        queue.push(data);
        if (!flushTimer) flushTimer = setTimeout(flush, performance.now() < replayUntil ? 0 : FLUSH_MS);
      };
      ws.onclose = () => {
        if (closed) return;
        phase.value = phase.value === 'open' || attempts.value ? 'reconnecting' : 'connecting';
        attempts.value++;
        retry = setTimeout(connect, Math.min(2000 * attempts.value, 10000));
      };
    }

    function flush() {
      flushTimer = 0;
      if (!queue.length) return;
      const chunks = queue;
      queue = [];
      let data = chunks[0];
      if (chunks.length > 1) {
        data = new Uint8Array(chunks.reduce((n, c) => n + c.length, 0));
        let off = 0;
        for (const c of chunks) { data.set(c, off); off += c.length; }
      }
      const buf = term.buffer.active;
      const following = buf.viewportY === buf.baseY; // showing the live end, not scrolled back
      term.write(data, () => {
        const n = lineFeeds;
        lineFeeds = 0;
        const b = term.buffer.active;
        if (n && following && b.baseY > 0 && performance.now() >= replayUntil && !reduceMotion.matches) animate(n);
      });
    }

    // Glide the screen up by the rows just added (continuing from wherever
    // a running glide is), and fade the new bottom rows in.
    function animate(lines) {
      const screen = el.value?.querySelector('.xterm-screen');
      if (!screen) return;
      const rowH = screen.clientHeight / term.rows;
      const rows = Math.min(lines, MAX_SLIDE, term.rows);
      const now = new DOMMatrixReadOnly(getComputedStyle(screen).transform).m42 || 0;
      slide?.cancel();
      slide = screen.animate(
        [{ transform: `translateY(${Math.min(now + rows * rowH, MAX_SLIDE * rowH)}px)` }, { transform: 'none' }],
        { duration: SLIDE_MS, easing: 'cubic-bezier(.2, .8, .2, 1)' },
      );
      // The glide uncovers a strip at the top; fade that edge while moving.
      el.value.classList.add('sliding');
      slide.onfinish = () => el.value?.classList.remove('sliding');
      if (veil.value) {
        // Over the screen's bottom rows (the screen can be a few px shorter than its box).
        const h = Math.min(lines, term.rows) * rowH;
        const top = screen.getBoundingClientRect().top - el.value.getBoundingClientRect().top + term.rows * rowH - h;
        Object.assign(veil.value.style, { top: `${top}px`, height: `${h}px` });
        veil.value.animate([{ opacity: 0.9 }, { opacity: 0 }], { duration: 260, easing: 'ease-out', fill: 'forwards' });
      }
    }

    onMounted(() => {
      term = new Terminal({
        convertEol: false, cursorBlink: true, scrollback: 5000, fontSize: 13,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
        theme: { background: '#0f1115', foreground: '#d8dee9' },
        smoothScrollDuration: reduceMotion.matches ? 0 : 120, // mouse-wheel scrolling
      });
      fit = new FitAddon.FitAddon();
      term.loadAddon(fit);
      term.open(el.value);
      fit.fit();
      term.onData((data) => send({ type: 'input', data }));
      term.onResize(({ cols, rows }) => send({ type: 'resize', cols, rows }));
      term.onLineFeed(() => lineFeeds++);
      resizeObs = new ResizeObserver(() => fit.fit());
      resizeObs.observe(el.value);
      connect();
    });

    onUnmounted(() => {
      closed = true;
      clearTimeout(retry);
      clearTimeout(flushTimer);
      resizeObs?.disconnect();
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
    return { el, veil, phase, connected, revealed, overlay, command, sendCommand, emit };
  },
  template: `
    <div class="terminal-wrap">
      <!-- xterm is sized from its parent's height; FitAddon counts the
           parent's padding as usable space, so the padding lives on an outer
           box and the parent (.term-fit) has none, or the last row would
           spill over the command bar. -->
      <div class="term-host" :class="{ revealed }">
        <div ref="el" class="term-fit"><div ref="veil" class="term-veil"></div></div>
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
