// Web console: xterm.js attached to the instance's PTY over WebSocket.
// Protocol is documented in internal/instance/http.go.
export const TerminalPane = {
  props: { id: String },
  emits: ['status'],
  setup(props, { emit }) {
    const { ref, onMounted, onUnmounted } = Vue;
    const el = ref(null);
    const connected = ref(false);
    const command = ref('');
    let term, fit, ws, retry, resizeObs, closed = false;

    const send = (msg) => ws && ws.readyState === WebSocket.OPEN && ws.send(JSON.stringify(msg));

    function connect() {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      ws = new WebSocket(`${proto}://${location.host}/api/instances/${props.id}/terminal`);
      ws.binaryType = 'arraybuffer';
      ws.onopen = () => {
        connected.value = true;
        term.reset();
        send({ type: 'resize', cols: term.cols, rows: term.rows });
      };
      ws.onmessage = (ev) => {
        if (typeof ev.data === 'string') {
          const msg = JSON.parse(ev.data);
          if (msg.type === 'status') emit('status', msg.data);
        } else {
          term.write(new Uint8Array(ev.data));
        }
      };
      ws.onclose = () => {
        connected.value = false;
        if (!closed) retry = setTimeout(connect, 2000);
      };
    }

    onMounted(() => {
      term = new Terminal({
        convertEol: false, cursorBlink: true, scrollback: 5000, fontSize: 13,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
        theme: { background: '#0f1115', foreground: '#d8dee9' },
      });
      fit = new FitAddon.FitAddon();
      term.loadAddon(fit);
      term.open(el.value);
      fit.fit();
      term.onData((data) => send({ type: 'input', data }));
      term.onResize(({ cols, rows }) => send({ type: 'resize', cols, rows }));
      resizeObs = new ResizeObserver(() => fit.fit());
      resizeObs.observe(el.value);
      connect();
    });

    onUnmounted(() => {
      closed = true;
      clearTimeout(retry);
      resizeObs?.disconnect();
      ws?.close();
      term?.dispose();
    });

    // Line input for touch devices / pasting whole commands.
    const sendCommand = () => {
      if (!command.value) return;
      send({ type: 'input', data: command.value + '\r' });
      command.value = '';
      term.focus();
    };
    return { el, connected, command, sendCommand };
  },
  template: `
    <div class="terminal-wrap">
      <!-- xterm is sized from its parent's height; FitAddon counts the
           parent's padding as usable space, so the padding lives on an outer
           box and the parent (.term-fit) has none, or the last row would
           spill over the command bar. -->
      <div class="term-host"><div ref="el" class="term-fit"></div></div>
      <form class="command-bar" @submit.prevent="sendCommand">
        <span class="conn"><span class="dot" :class="{ on: connected }"></span>{{ connected ? '已连接' : '连接中…' }}</span>
        <input v-model="command" class="mono" placeholder="输入命令后回车发送">
        <button class="primary" :disabled="!connected || !command"><Icon name="send" />发送</button>
      </form>
    </div>
  `,
};
