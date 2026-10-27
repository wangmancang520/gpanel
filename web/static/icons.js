// Inline line icons (24×24, stroke = currentColor) so the panel needs no icon
// font or extra requests. Add an icon: one entry of SVG inner markup.
const paths = {
  gauge: '<path d="M12 14l4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/>',
  server: '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>',
  layers: '<path d="M12 3 3 7.5l9 4.5 9-4.5z"/><path d="m3 12 9 4.5 9-4.5"/><path d="m3 16.5 9 4.5 9-4.5"/>',
  terminal: '<path d="m5 17 5-5-5-5"/><path d="M12 19h7"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  'folder-plus': '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/><path d="M12 10v6M9 13h6"/>',
  file: '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/>',
  'file-plus': '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/><path d="M12 11v6M9 14h6"/>',
  sliders: '<path d="M4 6h9M17 6h3M4 12h3M11 12h9M4 18h11M19 18h1"/><circle cx="15" cy="6" r="2"/><circle cx="9" cy="12" r="2"/><circle cx="17" cy="18" r="2"/>',
  play: '<path d="M7 4.5v15l12.5-7.5z"/>',
  stop: '<rect x="6" y="6" width="12" height="12" rx="2"/>',
  restart: '<path d="M3.5 12a8.5 8.5 0 1 0 2.6-6.1L3.5 8.5"/><path d="M3.5 3.5v5h5"/>',
  power: '<path d="M12 3v9"/><path d="M18.4 6.6a9 9 0 1 1-12.8 0"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  upload: '<path d="M12 15V4"/><path d="m7 9 5-5 5 5"/><path d="M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/>',
  download: '<path d="M12 4v11"/><path d="m7 10 5 5 5-5"/><path d="M4 17v2a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2"/>',
  trash: '<path d="M4 7h16"/><path d="M9 7V4h6v3"/><path d="m6 7 1 12a2 2 0 0 0 2 2h6a2 2 0 0 0 2-2l1-12"/>',
  archive: '<rect x="3" y="4" width="18" height="5" rx="1"/><path d="M5 9v10a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V9"/><path d="M10 13h4"/>',
  edit: '<path d="M4 20h4L19 9l-4-4L4 16z"/><path d="m13.5 6.5 4 4"/>',
  logout: '<path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><path d="m16 17 5-5-5-5"/><path d="M21 12H9"/>',
  key: '<circle cx="7.5" cy="15.5" r="4.5"/><path d="m10.7 12.3 9.3-9.3"/><path d="m16 7 3 3"/>',
  check: '<path d="m5 12.5 4.5 4.5L19 7.5"/>',
  alert: '<circle cx="12" cy="12" r="9"/><path d="M12 7.5v5M12 16.5h.01"/>',
  info: '<circle cx="12" cy="12" r="9"/><path d="M12 16.5v-5M12 7.5h.01"/>',
  refresh: '<path d="M20.5 12a8.5 8.5 0 0 1-14.6 5.9L3.5 15.5"/><path d="M3.5 12a8.5 8.5 0 0 1 14.6-5.9l2.4 2.4"/><path d="M20.5 3.5v5h-5"/><path d="M3.5 20.5v-5h5"/>',
  send: '<path d="M4 12 20 4l-6 16-3-7z"/><path d="m11 13 9-9"/>',
  cpu: '<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M10 10h4v4h-4z"/><path d="M9 3v3M15 3v3M9 18v3M15 18v3M3 9h3M3 15h3M18 9h3M18 15h3"/>',
  memory: '<rect x="3" y="7" width="18" height="10" rx="2"/><path d="M7 11v2M11 11v2M15 11v2M7 17v3M12 17v3M17 17v3"/>',
  disk: '<ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6"/><path d="M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3"/>',
  arrow: '<path d="M5 12h14"/><path d="m13 6 6 6-6 6"/>',
  'arrow-left': '<path d="M19 12H5"/><path d="m11 18-6-6 6-6"/>',
  'arrow-up': '<path d="M12 19V5"/><path d="m6 11 6-6 6 6"/>',
  'file-code': '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/><path d="m10 12.5-2 2 2 2M14 12.5l2 2-2 2"/>',
  'file-image': '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/><circle cx="10" cy="12" r="1.3"/><path d="m18 17.5-3.5-3.5L8 20"/>',
  'file-text': '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/><path d="M9 13h6M9 17h4"/>',
  'file-zip': '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/><path d="M10 3v2M11 6v2M10 9v2M10 13h2v3.5h-2z"/>',
  'file-cog': '<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/><circle cx="12" cy="15" r="2.2"/><path d="M12 11v1.3M12 17.7V19M8 15h1.3M14.7 15H16"/>',
  grid: '<rect x="4" y="4" width="7" height="7" rx="1.5"/><rect x="13" y="4" width="7" height="7" rx="1.5"/><rect x="4" y="13" width="7" height="7" rx="1.5"/><rect x="13" y="13" width="7" height="7" rx="1.5"/>',
  list: '<path d="M9 6h11M9 12h11M9 18h11M4.5 6h.01M4.5 12h.01M4.5 18h.01"/>',
  columns: '<path d="M4 6h6M4 12h6M4 18h6M14 6h6M14 12h6M14 18h6"/>',
  search: '<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
  scissors: '<circle cx="6" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M8.6 7.6 20 18M8.6 16.4 20 6"/>',
  copy: '<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2"/>',
  clipboard: '<rect x="5" y="4" width="14" height="17" rx="2"/><path d="M9 3h6v3H9z"/>',
  eye: '<path d="M2.5 12S6 5 12 5s9.5 7 9.5 7-3.5 7-9.5 7-9.5-7-9.5-7z"/><circle cx="12" cy="12" r="3"/>',
  x: '<path d="M6 6l12 12M18 6 6 18"/>',
  'select-all': '<path d="M4 8V5a1 1 0 0 1 1-1h3M16 4h3a1 1 0 0 1 1 1v3M20 16v3a1 1 0 0 1-1 1h-3M8 20H5a1 1 0 0 1-1-1v-3"/><path d="m8.5 12 2.5 2.5 4.5-5"/>',
  wrap: '<path d="M4 6h16M4 12h13a3 3 0 0 1 0 6h-4"/><path d="m15 16-2 2 2 2"/><path d="M4 18h5"/>',
  sort: '<path d="M7 4v16M3 16l4 4 4-4"/><path d="M14 6h7M14 12h5M14 18h3"/>',
  'chevron-left': '<path d="m15 6-6 6 6 6"/>',
  'chevron-right': '<path d="m9 6 6 6-6 6"/>',
};

export const Icon = {
  props: { name: String, size: { type: [Number, String], default: 16 } },
  setup(props) {
    return { inner: () => paths[props.name] || '' };
  },
  template: `<svg class="icon" :width="size" :height="size" viewBox="0 0 24 24" fill="none" stroke="currentColor"
    stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" v-html="inner()"></svg>`,
};

// Brand mark: a rounded tile with a play glyph.
export const Logo = {
  props: { size: { type: Number, default: 28 } },
  template: `
    <svg class="logo" :width="size" :height="size" viewBox="0 0 32 32" aria-hidden="true">
      <defs>
        <linearGradient id="gp-logo" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stop-color="#5b8cff"/><stop offset="1" stop-color="#2f5fd8"/>
        </linearGradient>
      </defs>
      <rect width="32" height="32" rx="8" fill="url(#gp-logo)"/>
      <path d="M12.5 10.5v11l9-5.5z" fill="#fff" stroke="#fff" stroke-width="1.5" stroke-linejoin="round"/>
    </svg>`,
};
