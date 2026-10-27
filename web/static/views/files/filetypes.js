// File type detection by name: icon, colour class, list label, how to open it
// and which CodeMirror mode highlights it. Add a type: extend the tables.

const extOf = (name) => {
  const i = name.lastIndexOf('.');
  return i > 0 ? name.slice(i + 1).toLowerCase() : '';
};

const IMAGE = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico']); // previewable (see files/http.go)
const ARCHIVE = new Set(['zip', 'jar', 'war', 'mrpack', 'gz', 'tgz', 'tar', '7z', 'rar', 'xz', 'bz2']);
const BINARY = new Set([
  'exe', 'dll', 'so', 'dylib', 'bin', 'dat', 'dat_old', 'mca', 'mcr', 'nbt', 'db', 'sqlite', 'class', 'jfr',
  'pdf', 'mp3', 'ogg', 'wav', 'mp4', 'webm', 'ttf', 'otf', 'woff', 'woff2', 'psd',
]);

// ext -> [kind, CodeMirror mode, label]
const TEXT = {
  properties: ['config', 'properties', 'Properties'], cfg: ['config', 'properties', 'INI'], ini: ['config', 'properties', 'INI'],
  conf: ['config', 'properties', '配置'], env: ['config', 'properties', 'ENV'],
  yml: ['config', 'yaml', 'YAML'], yaml: ['config', 'yaml', 'YAML'],
  json: ['config', { name: 'javascript', json: true }, 'JSON'], json5: ['config', { name: 'javascript', json: true }, 'JSON'],
  mcmeta: ['config', { name: 'javascript', json: true }, 'JSON'],
  toml: ['config', 'toml', 'TOML'], xml: ['config', 'xml', 'XML'], svg: ['image', 'xml', 'SVG'],
  js: ['code', 'javascript', 'JavaScript'], mjs: ['code', 'javascript', 'JavaScript'], ts: ['code', 'text/typescript', 'TypeScript'],
  sh: ['code', 'shell', 'Shell'], bash: ['code', 'shell', 'Shell'], zsh: ['code', 'shell', 'Shell'],
  bat: ['code', null, '批处理'], cmd: ['code', null, '批处理'], ps1: ['code', null, 'PowerShell'],
  py: ['code', 'python', 'Python'], java: ['code', 'text/x-java', 'Java'], kt: ['code', 'text/x-kotlin', 'Kotlin'],
  c: ['code', 'text/x-csrc', 'C'], h: ['code', 'text/x-csrc', 'C'], cpp: ['code', 'text/x-c++src', 'C++'],
  cs: ['code', 'text/x-csharp', 'C#'], go: ['code', 'go', 'Go'], lua: ['code', 'lua', 'Lua'], sql: ['code', 'sql', 'SQL'],
  html: ['code', 'htmlmixed', 'HTML'], htm: ['code', 'htmlmixed', 'HTML'], css: ['code', 'css', 'CSS'],
  md: ['text', 'markdown', 'Markdown'], txt: ['text', null, '文本'], log: ['log', 'gplog', '日志'],
  diff: ['code', 'diff', 'Diff'], patch: ['code', 'diff', 'Diff'],
};
const BY_NAME = {
  dockerfile: ['code', 'dockerfile', 'Dockerfile'], 'nginx.conf': ['config', 'nginx', 'Nginx'],
  '.env': ['config', 'properties', 'ENV'], '.gitignore': ['config', 'properties', 'Git'], 'eula.txt': ['config', 'properties', '配置'],
};

const ICON = { dir: 'folder', image: 'file-image', archive: 'file-zip', config: 'file-cog', code: 'file-code', log: 'file-text', text: 'file-text', binary: 'file', other: 'file' };
const KIND_LABEL = { dir: '文件夹', image: '图像', archive: '压缩包', config: '配置文件', code: '脚本 / 代码', log: '日志', text: '文本', binary: '二进制文件' };

export function fileType(entry) {
  if (entry.isDir) return { kind: 'dir', icon: ICON.dir, label: KIND_LABEL.dir, open: 'dir' };
  const name = entry.name.toLowerCase();
  const ext = extOf(name);
  // Rotated logs such as latest.log.1 or 2024-01-01-1.log.gz count as logs/archives.
  const t = BY_NAME[name] || TEXT[ext] || (/\.log(\.\d+)?$/.test(name) ? TEXT.log : null);
  let kind, mode = null, label;
  if (IMAGE.has(ext)) [kind, label] = ['image', ext.toUpperCase() + ' 图像'];
  else if (ARCHIVE.has(ext)) [kind, label] = ['archive', ext === 'jar' ? 'Java 程序包' : ext.toUpperCase() + ' 压缩包'];
  else if (BINARY.has(ext)) [kind, label] = ['binary', ext.toUpperCase() + ' 文件'];
  else if (t) [kind, mode, label] = t;
  else [kind, label] = ['other', ext ? ext.toUpperCase() + ' 文件' : '文件'];
  const open = kind === 'image' && IMAGE.has(ext) ? 'image'
    : kind === 'archive' || kind === 'binary' ? null : 'text';
  return { kind, icon: ICON[kind], label, mode, open, ext, zip: ext === 'zip' || ext === 'jar' || ext === 'mrpack' };
}

// Languages offered in the editor's mode picker.
export const LANGUAGES = [
  ['纯文本', null], ['Properties / INI', 'properties'], ['YAML', 'yaml'], ['JSON', { name: 'javascript', json: true }],
  ['TOML', 'toml'], ['XML', 'xml'], ['Shell', 'shell'], ['JavaScript', 'javascript'], ['Python', 'python'],
  ['Java', 'text/x-java'], ['Lua', 'lua'], ['SQL', 'sql'], ['Markdown', 'markdown'], ['HTML', 'htmlmixed'],
  ['CSS', 'css'], ['Go', 'go'], ['日志', 'gplog'], ['Diff', 'diff'],
];
