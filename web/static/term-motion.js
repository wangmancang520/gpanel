// Output motion for the web console: when lines arrive, the lines already
// on screen are pushed up (eased), then each new line fades in from the
// right, one after another. Tuned in the terminal motion testbed.
//
// How it works: output is cut at line ends and written in steps. After each
// step the rows container starts shifted down by the rows the screen scrolled
// and eases back to 0, and each new logical line (with its wrapped rows)
// slides in. The terminal keeps EXTRA rows above the visible area (clipped by
// the host), so the push reveals real lines at the top instead of a gap; the
// server is told only the visible size. xterm's DOM renderer draws into fixed
// row elements, so when a step scrolls while earlier lines are still sliding
// in, their animations are moved up with them.
//
// Not animated: output written while scrolled back, the history replayed on
// connect, partial lines (typing echo, prompts) and reduced motion. Output
// that positions the cursor absolutely or switches to the alternate screen
// (full-screen programs) turns the hidden rows off for the rest of the
// session, since such programs address rows from the top of the screen.

export const MOTION = {
  push: 260,                            // ms, old lines moving up
  easing: 'cubic-bezier(.16, 1, .3, 1)', // fast start, slow stop
  slide: 320,                           // ms, a new line moving in
  dist: 48,                             // px it comes in from
  stagger: 40,                          // ms between new lines (capped per step)
  start: 0.4,                           // slide in once the push is this far along
  minGap: 50,                           // ms between steps; output in between is batched
};
const EXTRA = 12;
const MAX_STAGGER = 360; // ms, all new lines of one step have started by then
// CUP/HVP/VPA/DECSTBM, and the alternate screen.
const ABSOLUTE = /\x1b\[[\d;]*[Hfdr]|\x1b\[\?(?:1049|1047|47)h/;
const latin1 = new TextDecoder('latin1'); // byte-for-byte, for matching escapes

export class TermMotion {
  constructor(term, fit, clip, box) {
    Object.assign(this, { term, fit, clip, box });
    this.extra = EXTRA;
    this.rowH = 0;
    this.pending = []; // Uint8Array chunks not written yet
    this.slides = [];  // { row, frames, timing, anim }
    this.push = null;
    this.timer = 0;
    this.writing = false;
    this.last = 0;
    this.reduce = matchMedia('(prefers-reduced-motion: reduce)');
  }

  rowsEl() { return this.term.element?.querySelector('.xterm-rows'); }

  // The size the program behind the terminal should see.
  serverSize() { return { cols: this.term.cols, rows: Math.max(1, this.term.rows - this.extra) }; }

  // Size the terminal to the visible box plus the hidden rows above it.
  // Returns false until a row has been rendered and can be measured.
  layout() {
    const first = this.rowsEl()?.children[0];
    const rowH = first?.getBoundingClientRect().height;
    if (!rowH || !this.clip.clientHeight) return false;
    this.rowH = rowH;
    const visible = Math.max(4, Math.floor(this.clip.clientHeight / rowH));
    this.box.style.top = `${-this.extra * rowH}px`;
    this.box.style.height = `${(visible + this.extra) * rowH}px`;
    const cols = this.fit.proposeDimensions()?.cols || this.term.cols;
    if (this.term.rows !== visible + this.extra || this.term.cols !== cols) this.term.resize(cols, visible + this.extra);
    return true;
  }

  // After term.reset(): start the first line on the first visible row.
  reset() {
    this.pending = [];
    clearTimeout(this.timer);
    this.timer = 0;
    this.cancelAll();
    if (this.extra) this.term.write('\r\n'.repeat(this.extra));
  }

  cancelAll() {
    this.push?.cancel();
    this.push = null;
    this.slides.forEach((s) => s.anim.cancel());
    this.slides = [];
  }

  // Output from the server. `instant` (history replay) skips the animation
  // but keeps the order with anything already queued.
  feed(data, { instant = false } = {}) {
    this.pending.push(data);
    if (instant) this.step(false);
    else this.schedule();
  }

  schedule() {
    if (this.timer || this.writing) return;
    const wait = Math.max(0, this.last + MOTION.minGap - performance.now());
    this.timer = setTimeout(() => { this.timer = 0; this.step(true); }, wait);
  }

  step(animated) {
    if (this.writing || !this.pending.length) return;
    let data = this.pending.length === 1 ? this.pending[0] : concat(this.pending);
    this.pending = [];
    if (this.extra && ABSOLUTE.test(latin1.decode(data))) this.dropExtraRows();

    // Animate complete lines only; a partial line (typing echo, a prompt)
    // is written as it is, right away.
    const cut = data.lastIndexOf(10);
    if (!animated || cut < 0) {
      this.term.write(data);
      return;
    }
    if (cut < data.length - 1) {
      this.pending.push(data.subarray(cut + 1));
      data = data.subarray(0, cut + 1);
    }
    this.last = performance.now();
    let lines = 0;
    for (const c of data) if (c === 10) lines++;
    const b = this.term.buffer.active;
    const before = { baseY: b.baseY, following: b.viewportY >= b.baseY, normal: b.type === 'normal' };
    this.writing = true;
    this.term.write(data, () => {
      this.writing = false;
      if (before.following && before.normal && this.rowH && !this.reduce.matches && this.rowsEl()) {
        const groups = this.newGroups(lines);
        this.animate(groups, this.scrolledBy(before, groups));
      }
      if (this.pending.length) this.schedule();
    });
  }

  dropExtraRows() {
    this.cancelAll();
    this.extra = 0;
    this.layout();
  }

  // The rows of each new logical line (with its wrapped rows), oldest first,
  // ending above the cursor.
  newGroups(lines) {
    const b = this.term.buffer.active;
    const groups = [];
    for (let r = b.cursorY - 1; r >= 0 && groups.length < lines; r--) {
      const g = [r];
      while (r > 0 && b.getLine(b.viewportY + r)?.isWrapped) g.unshift(--r);
      groups.unshift(g);
    }
    return groups;
  }

  // Rows the screen moved up during the write. Once scrollback is full
  // baseY stops growing, and the screen moved by as many rows as were added.
  scrolledBy(before, groups) {
    const b = this.term.buffer.active;
    if (b.baseY !== before.baseY) return b.baseY - before.baseY;
    const full = b.length >= this.term.rows + (this.term.options.scrollback ?? 1000);
    return full ? groups.reduce((n, g) => n + g.length, 0) : 0;
  }

  animate(groups, scrolled) {
    const el = this.rowsEl(), rowEls = el.children, rowH = this.rowH;

    // Lines still sliding in moved up with the scroll: move their animations too.
    this.slides = this.slides.filter((s) => s.anim.playState !== 'finished');
    if (scrolled) {
      this.slides = this.slides.flatMap((s) => {
        const t = s.anim.currentTime;
        s.anim.cancel();
        const row = s.row - scrolled;
        if (row < 0 || !rowEls[row]) return [];
        const anim = rowEls[row].animate(s.frames, s.timing);
        anim.currentTime = t;
        return [{ ...s, row, anim }];
      });
    }

    // 1. Push, continuing from wherever a running push is; at most the rows kept above.
    const pushRows = Math.min(scrolled, this.extra);
    if (pushRows) {
      const now = new DOMMatrixReadOnly(getComputedStyle(el).transform).m42 || 0;
      const from = Math.min(now + pushRows * rowH, this.extra * rowH);
      this.push?.cancel();
      this.push = el.animate([{ transform: `translateY(${from}px)` }, { transform: 'translateY(0)' }],
        { duration: MOTION.push, easing: MOTION.easing });
    }

    // 2. Slide in, one logical line after another.
    const lead = pushRows ? MOTION.push * MOTION.start : 0;
    const per = groups.length > 1 ? Math.min(MOTION.stagger, MAX_STAGGER / (groups.length - 1)) : 0;
    const frames = [
      { opacity: 0, transform: `translateX(${MOTION.dist}px)` },
      { opacity: 1, transform: 'translateX(0)' },
    ];
    groups.forEach((g, i) => {
      const timing = { duration: MOTION.slide, delay: lead + i * per, easing: 'cubic-bezier(.16, 1, .3, 1)', fill: 'backwards' };
      for (const row of g) {
        if (rowEls[row]) this.slides.push({ row, frames, timing, anim: rowEls[row].animate(frames, timing) });
      }
    });
  }

  dispose() {
    clearTimeout(this.timer);
    this.cancelAll();
  }
}

function concat(chunks) {
  const out = new Uint8Array(chunks.reduce((n, c) => n + c.length, 0));
  let off = 0;
  for (const c of chunks) { out.set(c, off); off += c.length; }
  return out;
}
