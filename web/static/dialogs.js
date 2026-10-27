// Promise-based replacements for prompt()/confirm(), rendered by <DialogHost>
// (mounted once in app.js) so they match the panel's look.
//
//   const name = await ask({ title: '重命名', input: 'old.txt' });   // string | null
//   if (await confirmAction('删除？', '无法恢复', { danger: true })) ...

const state = Vue.reactive({ current: null });

export function ask(opts) {
  state.current?.resolve(null); // only one dialog at a time
  return new Promise((resolve) => {
    state.current = {
      title: '', message: '', okText: '确定', cancelText: '取消', danger: false,
      ...opts,
      value: opts.input ?? null,
      resolve: (v) => { state.current = null; resolve(v); },
    };
  });
}

export const confirmAction = (title, message, opts = {}) =>
  ask({ title, message, ...opts }).then((v) => v !== null);

export const DialogHost = {
  setup() {
    const { ref, watch, nextTick } = Vue;
    const input = ref(null);
    const okBtn = ref(null);
    // Focus the field (selecting `select` range, e.g. the name without its
    // extension) or the OK button whenever a dialog opens.
    watch(() => state.current, async (d) => {
      if (!d) return;
      await nextTick();
      if (input.value) {
        input.value.focus();
        const [a, b] = d.select || [0, input.value.value.length];
        input.value.setSelectionRange(a, b);
      } else {
        okBtn.value?.focus();
      }
    });
    const ok = () => {
      const d = state.current;
      if (d.input !== undefined && !String(d.value).trim()) return;
      d.resolve(d.input !== undefined ? d.value : true);
    };
    const cancel = () => state.current?.resolve(null);
    return { state, input, okBtn, ok, cancel };
  },
  template: `
    <Transition name="modal">
      <div v-if="state.current" class="modal-backdrop" @click.self="cancel" @keydown.esc="cancel">
        <form class="card modal dialog" @submit.prevent="ok" role="dialog" aria-modal="true">
          <h2>{{ state.current.title }}</h2>
          <p v-if="state.current.message" class="muted">{{ state.current.message }}</p>
          <input v-if="state.current.input !== undefined" ref="input" v-model="state.current.value"
            :placeholder="state.current.placeholder" class="mono" spellcheck="false">
          <div class="row end">
            <button type="button" @click="cancel">{{ state.current.cancelText }}</button>
            <button ref="okBtn" :class="state.current.danger ? 'danger-fill' : 'primary'">{{ state.current.okText }}</button>
          </div>
        </form>
      </div>
    </Transition>
  `,
};

// Right-click menu. Items: { label, icon?, shortcut?, action, danger?, disabled?, checked? }
// or { divider: true }. Positioned at (x, y) and kept inside the viewport.
export const ContextMenu = {
  props: { x: Number, y: Number, items: Array },
  emits: ['close'],
  setup(props, { emit }) {
    const { ref, onMounted, onUnmounted, nextTick } = Vue;
    const el = ref(null);
    const pos = ref({ left: props.x + 'px', top: props.y + 'px' });
    const close = () => emit('close');
    const onDown = (e) => { if (el.value && !el.value.contains(e.target)) close(); };
    const onKey = (e) => { if (e.key === 'Escape') { e.stopPropagation(); close(); } };
    onMounted(async () => {
      await nextTick();
      // offset* ignore the scale-in animation's transform.
      const { offsetWidth: w, offsetHeight: h } = el.value;
      pos.value = {
        left: Math.max(4, Math.min(props.x, innerWidth - w - 4)) + 'px',
        top: Math.max(4, Math.min(props.y, innerHeight - h - 4)) + 'px',
      };
      el.value.querySelector('button:not(:disabled)')?.focus({ preventScroll: true });
      addEventListener('pointerdown', onDown, true);
      addEventListener('keydown', onKey, true);
      addEventListener('resize', close);
      addEventListener('scroll', close, true);
      addEventListener('blur', close);
    });
    onUnmounted(() => {
      removeEventListener('pointerdown', onDown, true);
      removeEventListener('keydown', onKey, true);
      removeEventListener('resize', close);
      removeEventListener('scroll', close, true);
      removeEventListener('blur', close);
    });
    const run = (item) => { if (!item.disabled) { close(); item.action(); } };
    // Arrow keys move between items, like a native menu.
    const nav = (e, dir) => {
      const btns = [...el.value.querySelectorAll('button:not(:disabled)')];
      const i = btns.indexOf(document.activeElement);
      btns[(i + dir + btns.length) % btns.length]?.focus();
    };
    return { el, pos, run, nav };
  },
  template: `
    <Teleport to="body">
      <div ref="el" class="ctx-menu" :style="pos" role="menu" @contextmenu.prevent
        @keydown.down.prevent="nav($event, 1)" @keydown.up.prevent="nav($event, -1)">
        <template v-for="(it, i) in items" :key="i">
          <div v-if="it.divider" class="ctx-divider"></div>
          <button v-else type="button" role="menuitem" :class="{ danger: it.danger }" :disabled="it.disabled" @click="run(it)">
            <span class="ctx-icon"><Icon v-if="it.checked !== undefined ? it.checked : it.icon" :name="it.checked !== undefined ? 'check' : it.icon" /></span>
            <span class="ctx-label">{{ it.label }}</span>
            <kbd v-if="it.shortcut">{{ it.shortcut }}</kbd>
          </button>
        </template>
      </div>
    </Teleport>
  `,
};
