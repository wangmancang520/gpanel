// Package gsmanager imports game instances from a native (non-Docker)
// GSManager 3 install (github.com/GSManagerXZ/GameServerManager) into
// GPanel: it reads <install>/data/instances.json, converts each instance's
// start command, maps its Java version onto a GPanel runtime, and creates
// the instance in place. Game files stay where they are unless listed in
// Options.Move.
//
// The GPanel service must be stopped while importing: the running panel
// keeps instances.json in memory and would overwrite the new entries.
package gsmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gpanel/internal/events"
	"gpanel/internal/instance"
	"gpanel/internal/runtimes"
)

// ExtensionKey is where the original GSManager entry is kept in
// instance.Config.Extensions; its "id" makes re-running the import a no-op.
const ExtensionKey = "gsmanager"

// gsmInstance is the part of GSManager's instance record we use.
type gsmInstance struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	WorkingDirectory    string `json:"workingDirectory"`
	StartCommand        string `json:"startCommand"`
	StopCommand         string `json:"stopCommand"` // ctrl+c | stop | exit | quit
	AutoStart           bool   `json:"autoStart"`
	EnableStreamForward bool   `json:"enableStreamForward"`
	TerminalUser        string `json:"terminalUser"`
	InstanceType        string `json:"instanceType"` // generic | minecraft-java | minecraft-bedrock
	JavaVersion         string `json:"javaVersion"`  // java8, java17, ...
	Steam               *struct {
		AppID string `json:"appId"`
	} `json:"steam"`
}

type Options struct {
	GSMDir  string          // GSManager install dir, holding data/instances.json
	DataDir string          // GPanel data dir
	Move    map[string]bool // work dirs to move into GPanel's instance dir
	DryRun  bool            // report only, change nothing
	// RewriteJava replaces hard-coded GSManager Java paths in start scripts
	// with plain "java" (backing the script up), so the instance's runtime is
	// used and removing GSManager doesn't break it.
	RewriteJava bool
	Report      io.Writer // human-readable progress (Chinese)
	Dirs        io.Writer // one final work dir per imported instance (for the installer)
}

type Result struct {
	Imported, Skipped int
}

// Import runs the import described by opts.
func Import(opts Options) (Result, error) {
	var res Result
	if opts.Report == nil {
		opts.Report = io.Discard
	}
	if opts.Dirs == nil {
		opts.Dirs = io.Discard
	}
	list, err := readInstances(filepath.Join(opts.GSMDir, "data", "instances.json"))
	if err != nil {
		return res, err
	}
	mgr, err := instance.NewManager(opts.DataDir, events.NewBus())
	if err != nil {
		return res, err
	}
	imported := map[string]bool{}
	for _, s := range mgr.List() {
		var ext struct{ ID string }
		if json.Unmarshal(s.Extensions[ExtensionKey], &ext) == nil && ext.ID != "" {
			imported[ext.ID] = true
		}
	}
	java := &javaMapper{
		gsmJavaDir: filepath.Join(opts.GSMDir, "data", "environment", "Java"),
		managedDir: filepath.Join(opts.DataDir, "runtimes", "java"),
		dryRun:     opts.DryRun,
		done:       map[string]javaResult{},
	}
	java.registry = runtimes.NewRegistry(&runtimes.Java{ManagedDir: java.managedDir})
	java.registry.Scan(context.Background())

	say := func(format string, args ...any) { fmt.Fprintf(opts.Report, format+"\n", args...) }
	for _, it := range list {
		in := it.inst
		say("· %s", in.Name)
		if in.ID != "" && imported[in.ID] {
			say("    已导入过，跳过")
			res.Skipped++
			continue
		}
		dir := in.WorkingDirectory
		if dir != "" && !filepath.IsAbs(dir) {
			dir = filepath.Join(opts.GSMDir, dir) // GSManager runs with its install dir as cwd
		}
		dir = filepath.Clean(dir)
		if st, err := os.Stat(dir); in.WorkingDirectory == "" || err != nil || !st.IsDir() {
			say("    ✗ 工作目录不存在：%s，跳过", in.WorkingDirectory)
			res.Skipped++
			continue
		}
		conv, err := convertCommand(in, dir)
		if err != nil {
			say("    ✗ %v，跳过", err)
			res.Skipped++
			continue
		}
		cfg := instance.Config{
			Name: in.Name, WorkDir: dir, Command: conv.command,
			StopCommand: stopCommand(in.StopCommand), AutoStart: in.AutoStart,
			Extensions: map[string]json.RawMessage{ExtensionKey: it.raw},
		}
		say("    目录  %s", dir)
		say("    命令  %s", cfg.Command)
		// Start scripts often call GSManager's JDK by absolute path; that is
		// the Java the server really runs on, whatever the instance says.
		version := in.JavaVersion
		scripts := scriptJava(conv.shell, dir)
		for _, s := range scripts {
			say("    脚本  %s 直接调用 GSManager 的 Java（%s）", s.name, s.path)
			if version != "" && version != s.version {
				say("    ! 实例设置的是 %s，但脚本实际使用 %s，以脚本为准", version, s.version)
			}
			version = s.version
		}
		if version != "" {
			jr := java.resolve(version)
			cfg.Runtime = jr.id
			say("    Java  %s", jr.note)
		}
		if len(scripts) > 0 {
			switch {
			case !opts.RewriteJava:
				say("    ! 卸载 GSManager 后这些脚本将无法启动；可用 -rewrite-java 改为使用所选的运行时")
			case opts.DryRun:
				say("    将把脚本中的 GSManager Java 路径改为 java（原文件备份为 *%s），改用所选的运行时", backupSuffix)
			default:
				for _, s := range scripts {
					left, err := rewriteJava(filepath.Join(dir, s.name))
					if err != nil {
						say("    ! 改写 %s 失败：%v", s.name, err)
						continue
					}
					say("    已把 %s 中的 GSManager Java 路径改为 java（原文件备份为 %s%s）", s.name, s.name, backupSuffix)
					if left {
						say("    ! %s 中仍有其他地方引用 GSManager 的 Java 目录（如 JAVA_HOME），请手动检查", s.name)
					}
				}
			}
		}
		for _, w := range warnings(in) {
			say("    ! %s", w)
		}
		moving := opts.Move[dir]
		if moving {
			say("    将移动到 GPanel 数据目录（gpanel 用户无法访问原位置）")
		}
		if opts.DryRun {
			fmt.Fprintln(opts.Dirs, dir)
			res.Imported++
			continue
		}

		if conv.chmod != "" {
			if err := makeExecutable(filepath.Join(dir, conv.chmod)); err != nil {
				say("    ! 无法为 %s 添加执行权限：%v", conv.chmod, err)
			}
		}
		created, err := mgr.Create(cfg)
		if err != nil {
			say("    ✗ 创建失败：%v", err)
			res.Skipped++
			continue
		}
		final := dir
		if moving {
			if moved, err := moveIntoPanel(mgr, created); err != nil {
				say("    ! 移动失败，保留原位置：%v", err)
			} else {
				final = moved
				say("    已移动到 %s", final)
			}
		}
		fmt.Fprintln(opts.Dirs, final)
		say("    ✓ 已导入")
		res.Imported++
	}
	return res, nil
}

type entry struct {
	inst gsmInstance
	raw  json.RawMessage
}

func readInstances(path string) ([]entry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 GSManager 实例配置失败：%w", err)
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(b, &raws); err != nil {
		return nil, fmt.Errorf("GSManager 实例配置格式无法识别：%w", err)
	}
	out := make([]entry, 0, len(raws))
	for _, raw := range raws {
		var in gsmInstance
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("GSManager 实例配置格式无法识别：%w", err)
		}
		out = append(out, entry{in, raw})
	}
	return out, nil
}

func stopCommand(s string) string {
	switch s {
	case "ctrl+c":
		return instance.CtrlC
	case "":
		return "stop" // GSManager's default
	}
	return s
}

func warnings(in gsmInstance) []string {
	var w []string
	if in.TerminalUser != "" {
		w = append(w, fmt.Sprintf("原先以 %s 用户运行，GPanel 统一以 gpanel 用户运行", in.TerminalUser))
	}
	if in.EnableStreamForward {
		w = append(w, "原先开启了输出流转发，GPanel 统一使用终端模式")
	}
	if in.Steam != nil && in.Steam.AppID != "" {
		w = append(w, fmt.Sprintf("Steam 游戏（AppID %s）：游戏文件照常运行，但 GPanel 暂不支持 SteamCMD 一键更新", in.Steam.AppID))
	}
	return w
}

// ---- start commands

type converted struct {
	command string
	shell   string // the command as bash would run it, before wrapping
	chmod   string // file in the work dir to make executable, as GSManager does on start
}

// convertCommand reproduces what GSManager runs: Minecraft types detect a
// start script or jar at start time, and every command is typed into an
// interactive bash in the work dir. GPanel runs commands without a shell,
// so anything that needs one is wrapped in `bash -c '…'`.
func convertCommand(in gsmInstance, dir string) (converted, error) {
	var c converted
	cmd := strings.TrimSpace(in.StartCommand)
	switch in.InstanceType {
	case "minecraft-java":
		if s := detectStartScript(dir); s != "" {
			cmd = "./" + s
		} else if jar := detectJar(dir); jar != "" {
			cmd = "java -jar " + instance.QuoteArg(jar) + " nogui"
		} else {
			return c, errors.New("我的世界 Java 版：工作目录中没有 start.sh / run.sh 或 .jar 文件")
		}
	case "minecraft-bedrock":
		if !isFile(filepath.Join(dir, "bedrock_server")) {
			return c, errors.New("我的世界基岩版：工作目录中没有 bedrock_server")
		}
		cmd = "./bedrock_server"
	default:
		if cmd == "" {
			return c, errors.New("没有启动命令")
		}
		cmd = prefixLocalScript(cmd, dir)
	}
	if first := strings.Fields(cmd)[0]; strings.HasPrefix(first, "./") && isFile(filepath.Join(dir, first)) {
		c.chmod = first[2:]
	}
	c.shell, c.command = cmd, wrapShell(cmd)
	return c, nil
}

func detectStartScript(dir string) string {
	for _, s := range []string{"start.sh", "run.sh"} {
		if isFile(filepath.Join(dir, s)) {
			return s
		}
	}
	return ""
}

// detectJar: the only jar, else the first containing "server", else the
// first one (readdir order is sorted here; GSManager's is too on most systems).
func detectJar(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var jars []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jar") && !e.IsDir() {
			jars = append(jars, e.Name())
		}
	}
	if len(jars) == 0 {
		return ""
	}
	if i := slices.IndexFunc(jars, func(n string) bool { return strings.Contains(strings.ToLower(n), "server") }); i >= 0 && len(jars) > 1 {
		return jars[i]
	}
	return jars[0]
}

// prefixLocalScript adds "./" to a bare *.sh first word that exists in dir,
// like GSManager's start-command normalization.
func prefixLocalScript(cmd, dir string) string {
	first, rest, _ := strings.Cut(cmd, " ")
	quoted := len(first) > 2 && strings.HasPrefix(first, `"`) && strings.HasSuffix(first, `"`)
	name := strings.Trim(first, `"`)
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") ||
		filepath.Ext(name) != ".sh" || !isFile(filepath.Join(dir, name)) {
		return cmd
	}
	first = "./" + name
	if quoted {
		first = `"` + first + `"`
	}
	if rest == "" {
		return first
	}
	return first + " " + rest
}

var plainCommand = regexp.MustCompile(`^[A-Za-z0-9_/:=+,@%-][A-Za-z0-9_./:=+,@%-]*( +[A-Za-z0-9_./:=+,@%-]+)*$`)

// wrapShell leaves simple commands ("java -Xmx4G -jar server.jar nogui")
// as they are and runs everything else through bash, as GSManager did:
// shell syntax, quotes, variables, and ./scripts (which bash also runs when
// they lack a shebang).
func wrapShell(cmd string) string {
	if plainCommand.MatchString(cmd) {
		return cmd
	}
	return "bash -c " + instance.QuoteArg(cmd)
}

// ---- Java paths hard-coded in start scripts

const backupSuffix = ".gsmanager.bak"

// gsmJavaPath matches a call to a JDK that GSManager installed, e.g.
// /opt/gsmanager3/data/environment/Java/java21/jdk-21/bin/java.
var gsmJavaPath = regexp.MustCompile(`/[^\s"'=;:]*/data/environment/Java/(java[0-9]+)/(?:[^\s"';:]*/)?bin/java\b`)

type scriptRef struct {
	name, version, path string
}

// scriptJava finds the *.sh files in dir that cmd runs and reports those
// calling a GSManager JDK directly.
func scriptJava(cmd, dir string) []scriptRef {
	var refs []scriptRef
	seen := map[string]bool{}
	for _, tok := range strings.FieldsFunc(cmd, func(r rune) bool { return strings.ContainsRune(" \t\n;&|()", r) }) {
		name := strings.TrimPrefix(strings.Trim(tok, `"'`), "./")
		if seen[name] || !strings.HasSuffix(name, ".sh") || strings.Contains(name, "/") || !isFile(filepath.Join(dir, name)) {
			continue
		}
		seen[name] = true
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(b) > 1<<20 {
			continue
		}
		if m := gsmJavaPath.FindSubmatch(b); m != nil {
			refs = append(refs, scriptRef{name: name, version: string(m[1]), path: string(m[0])})
		}
	}
	return refs
}

// rewriteJava replaces GSManager JDK calls in the script with "java",
// keeping the original as <script>.gsmanager.bak (an existing backup, from
// an earlier run, is kept). It reports whether other references to
// GSManager's Java directory remain.
func rewriteJava(path string) (leftover bool, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(path + backupSuffix); os.IsNotExist(err) {
		if err := os.WriteFile(path+backupSuffix, b, st.Mode().Perm()); err != nil {
			return false, err
		}
	}
	out := gsmJavaPath.ReplaceAll(b, []byte("java"))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, st.Mode().Perm()); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return strings.Contains(string(out), "/data/environment/Java/"), nil
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func makeExecutable(p string) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	return os.Chmod(p, st.Mode().Perm()|0o111)
}

// moveIntoPanel moves an imported instance's files into GPanel's default
// instance dir and points the instance there. Same filesystem only: a
// rename is instant, while copying a world across disks is left to the admin.
func moveIntoPanel(mgr *instance.Manager, cfg instance.Config) (string, error) {
	src := cfg.WorkDir
	moved := cfg
	moved.WorkDir = ""
	// Update first to learn the default path; it doesn't touch the disk.
	updated, err := mgr.Update(cfg.ID, moved)
	if err != nil {
		return "", err
	}
	dst, err := mgr.WorkDir(updated.ID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(src, dst); err != nil {
		if _, rerr := mgr.Update(cfg.ID, cfg); rerr != nil {
			return "", errors.Join(err, rerr)
		}
		return "", err
	}
	return dst, nil
}

// ---- Java

type javaResult struct {
	id   string // GPanel runtime ID; empty if none
	note string
}

type javaMapper struct {
	gsmJavaDir, managedDir string
	dryRun                 bool
	registry               *runtimes.Registry
	done                   map[string]javaResult
}

// resolve maps a GSManager Java version ("java17") onto a GPanel runtime,
// preferring what reproduces the instance's old setup: a panel-managed Java
// of that major version, else a copy of the JDK GSManager itself ran it
// with, and only then a system Java (possibly another distribution or a
// headless JRE) of that major version.
func (j *javaMapper) resolve(version string) javaResult {
	if r, ok := j.done[version]; ok {
		return r
	}
	r := j.lookup(version)
	j.done[version] = r
	return r
}

func (j *javaMapper) lookup(version string) javaResult {
	major, err := strconv.Atoi(strings.TrimPrefix(version, "java"))
	if err != nil {
		return javaResult{note: fmt.Sprintf("无法识别的 Java 版本 %q，将使用系统默认 java", version)}
	}
	var system *runtimes.Runtime
	for _, rt := range j.registry.List("java") {
		if rt.Major != major {
			continue
		}
		if rt.Managed {
			return javaResult{rt.ID, fmt.Sprintf("Java %d → 使用 GPanel 已有的 %s", major, rt.Name)}
		}
		if system == nil {
			system = &rt
		}
	}
	// fallback is used when GSManager's own JDK can't be used.
	fallback := func(why string) javaResult {
		if system != nil {
			return javaResult{system.ID, fmt.Sprintf("Java %d → %s，使用系统中的 %s（%s）", major, why, system.Name, system.Home)}
		}
		return javaResult{note: fmt.Sprintf("Java %d：%s，请在「运行时」页安装后到实例设置中选择", major, why)}
	}
	home := findJavaHome(filepath.Join(j.gsmJavaDir, version))
	if home == "" {
		return fallback("GSManager 中没有安装")
	}
	name := "gsmanager-" + version
	if j.dryRun {
		return javaResult{note: fmt.Sprintf("Java %d → 复制 GSManager 安装的 JDK（%s）", major, home)}
	}
	if err := copyTree(home, filepath.Join(j.managedDir, name)); err != nil {
		return fallback(fmt.Sprintf("复制 GSManager 的 JDK 失败（%v）", err))
	}
	j.registry.Scan(context.Background())
	for _, rt := range j.registry.List("java") {
		if rt.ManagedPath == name {
			return javaResult{rt.ID, fmt.Sprintf("Java %d → 已复制 GSManager 的 JDK 为 %s", major, rt.Name)}
		}
	}
	return fallback("复制的 JDK 未能识别")
}

// findJavaHome finds the directory holding bin/java under dir, a few levels
// deep (GSManager unpacks archives as java17/jdk-17.x/...).
func findJavaHome(dir string) string {
	var home string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || home != "" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() && strings.Count(rel, string(filepath.Separator)) > 3 {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "java" && filepath.Base(filepath.Dir(p)) == "bin" {
			home = filepath.Dir(filepath.Dir(p))
			return filepath.SkipAll
		}
		return nil
	})
	return home
}

// copyTree copies src to dst (which must not exist yet; an existing dst is
// kept as is), preserving modes and symlinks. It builds dst.tmp first so an
// interrupted copy never looks like a complete JDK.
func copyTree(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}
	tmp := dst + ".tmp"
	os.RemoveAll(tmp)
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(tmp, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		}
		return nil // devices, sockets: none in a JDK
	})
	if err != nil {
		os.RemoveAll(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
