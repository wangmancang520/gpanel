package gsmanager

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gpanel/internal/events"
	"gpanel/internal/instance"
)

// fakeGSM lays out a native GSManager install under a temp dir and returns
// its directory and a games directory beside it.
func fakeGSM(t *testing.T, instances []map[string]any) (gsm, games string) {
	t.Helper()
	root := t.TempDir()
	gsm, games = filepath.Join(root, "gsmanager3"), filepath.Join(root, "games")
	write := func(p, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(games, "pal", "sub", "run.sh"), "#!/bin/sh\n", 0o755)
	write(filepath.Join(games, "pal2", "PalServer.sh"), "#!/bin/sh\n", 0o644)
	write(filepath.Join(games, "plain", "server.jar"), "", 0o644)
	write(filepath.Join(games, "mcs", "start.sh"), "#!/bin/sh\n", 0o644)
	write(filepath.Join(games, "mcs", "server.jar"), "", 0o644)
	write(filepath.Join(games, "mcj", "libraries.jar"), "", 0o644)
	write(filepath.Join(games, "mcj", "paper-server.jar"), "", 0o644)
	write(filepath.Join(games, "bed", "bedrock_server"), "", 0o644)
	write(filepath.Join(games, "move", "world", "level.dat"), "save", 0o644)
	write(filepath.Join(gsm, "games", "rel", "x.sh"), "", 0o644)
	// GSManager unpacks Java one level below the version directory.
	jdk := filepath.Join(gsm, "data", "environment", "Java", "java17", "jdk-17.0.9+9")
	write(filepath.Join(jdk, "bin", "java"), "#!/bin/sh\n", 0o755)
	write(filepath.Join(jdk, "release"), "JAVA_VERSION=\"17.0.9\"\n", 0o644)
	if err := os.Symlink("bin/java", filepath.Join(jdk, "java-link")); err != nil {
		t.Fatal(err)
	}
	jdk21 := filepath.Join(gsm, "data", "environment", "Java", "java21", "jdk-21") // as on a real server
	write(filepath.Join(jdk21, "bin", "java"), "#!/bin/sh\n", 0o755)
	write(filepath.Join(jdk21, "release"), "JAVA_VERSION=\"21.0.5\"\n", 0o644)

	for _, in := range instances {
		if d, ok := in["workingDirectory"].(string); ok && !filepath.IsAbs(d) && !strings.HasPrefix(d, "games/") {
			in["workingDirectory"] = filepath.Join(games, d)
		}
	}
	b, _ := json.Marshal(instances)
	write(filepath.Join(gsm, "data", "instances.json"), string(b), 0o644)
	return gsm, games
}

func TestImport(t *testing.T) {
	gsm, games := fakeGSM(t, []map[string]any{
		{"id": "g1", "name": "shell", "workingDirectory": "pal", "startCommand": "cd sub && ./run.sh -x", "stopCommand": "ctrl+c",
			"autoStart": true, "terminalUser": "steam", "steam": map[string]any{"appId": "2394010"}},
		{"id": "g2", "name": "bare-script", "workingDirectory": "pal2", "startCommand": "PalServer.sh -port=8211", "stopCommand": "exit"},
		{"id": "g3", "name": "plain", "workingDirectory": "plain", "startCommand": "java -Xmx2G -jar server.jar nogui", "stopCommand": "stop"},
		{"id": "g4", "name": "mc-script", "workingDirectory": "mcs", "startCommand": "echo Minecraft Java Edition", "stopCommand": "stop", "instanceType": "minecraft-java"},
		{"id": "g5", "name": "mc-jar", "workingDirectory": "mcj", "startCommand": "x", "stopCommand": "stop", "instanceType": "minecraft-java", "javaVersion": "java17"},
		{"id": "g6", "name": "bedrock", "workingDirectory": "bed", "startCommand": "x", "stopCommand": "stop", "instanceType": "minecraft-bedrock"},
		{"id": "g7", "name": "relative", "workingDirectory": "games/rel", "startCommand": "x.sh", "stopCommand": "stop"},
		{"id": "g8", "name": "missing", "workingDirectory": "/nonexistent/dir", "startCommand": "x", "stopCommand": "stop"},
		{"id": "g9", "name": "moved", "workingDirectory": "move", "startCommand": "./run.sh", "stopCommand": "stop", "javaVersion": "java17"},
	})
	data := t.TempDir()
	moveDir := filepath.Join(games, "move")
	var report, dirs bytes.Buffer
	res, err := Import(Options{GSMDir: gsm, DataDir: data, Move: map[string]bool{moveDir: true}, Report: &report, Dirs: &dirs})
	if err != nil {
		t.Fatal(err)
	}
	t.Log(report.String())
	if res.Imported != 8 || res.Skipped != 1 {
		t.Fatalf("Imported %d, Skipped %d; want 8, 1", res.Imported, res.Skipped)
	}

	m, err := instance.NewManager(data, events.NewBus())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]instance.Status{}
	for _, s := range m.List() {
		got[s.Name] = s
	}
	want := map[string]struct{ cmd, stop string }{
		"shell":       {`bash -c 'cd sub && ./run.sh -x'`, instance.CtrlC},
		"bare-script": {`bash -c './PalServer.sh -port=8211'`, "exit"},
		"plain":       {"java -Xmx2G -jar server.jar nogui", "stop"},
		"mc-script":   {"bash -c ./start.sh", "stop"},
		"mc-jar":      {"java -jar paper-server.jar nogui", "stop"},
		"bedrock":     {"bash -c ./bedrock_server", "stop"},
		"relative":    {"bash -c ./x.sh", "stop"},
		"moved":       {"bash -c ./run.sh", "stop"},
	}
	for name, w := range want {
		s, ok := got[name]
		if !ok {
			t.Errorf("%s not imported", name)
			continue
		}
		if s.Command != w.cmd || s.StopCommand != w.stop {
			t.Errorf("%s: command %q stop %q; want %q %q", name, s.Command, s.StopCommand, w.cmd, w.stop)
		}
	}
	if !got["shell"].AutoStart || got["plain"].AutoStart {
		t.Error("autoStart not carried over")
	}
	if got["relative"].Dir != filepath.Join(gsm, "games", "rel") {
		t.Errorf("relative work dir resolved to %q", got["relative"].Dir)
	}

	// GSManager chmods ./scripts on start; the import does it once.
	if st, _ := os.Stat(filepath.Join(games, "pal2", "PalServer.sh")); st.Mode()&0o100 == 0 {
		t.Error("bare script not made executable")
	}

	// Java 17 was copied from GSManager into the managed dir, once, and
	// both instances that use it point at it.
	if got["mc-jar"].Runtime == "" || got["mc-jar"].Runtime != got["moved"].Runtime {
		t.Errorf("runtimes: %q, %q", got["mc-jar"].Runtime, got["moved"].Runtime)
	}
	copied := filepath.Join(data, "runtimes", "java", "gsmanager-java17")
	if target, err := os.Readlink(filepath.Join(copied, "java-link")); err != nil || target != "bin/java" {
		t.Errorf("symlink not preserved: %q, %v", target, err)
	}
	if st, err := os.Stat(filepath.Join(copied, "bin", "java")); err != nil || st.Mode()&0o111 == 0 {
		t.Errorf("copied java not executable: %v", err)
	}

	// The moved instance now lives in the data dir, with its files.
	moved := got["moved"]
	if moved.WorkDir != "" || moved.Dir != filepath.Join(data, "instances", moved.ID) {
		t.Errorf("moved instance: WorkDir %q Dir %q", moved.WorkDir, moved.Dir)
	}
	if b, err := os.ReadFile(filepath.Join(moved.Dir, "world", "level.dat")); err != nil || string(b) != "save" {
		t.Errorf("moved files missing: %v", err)
	}
	if _, err := os.Stat(moveDir); !os.IsNotExist(err) {
		t.Error("original directory still present after move")
	}
	if !strings.Contains(dirs.String(), moved.Dir+"\n") || strings.Contains(dirs.String(), moveDir+"\n") {
		t.Errorf("dirs output should list the final location:\n%s", dirs.String())
	}

	// The original record is kept for later features (SteamCMD, ...).
	var ext map[string]any
	json.Unmarshal(got["shell"].Extensions[ExtensionKey], &ext)
	if ext["id"] != "g1" || ext["terminalUser"] != "steam" {
		t.Errorf("extension = %v", ext)
	}
	for _, w := range []string{"steam 用户运行", "SteamCMD", "工作目录不存在"} {
		if !strings.Contains(report.String(), w) {
			t.Errorf("report lacks %q", w)
		}
	}
	// Moving is the caller's choice (e.g. all dirs at once): the report must
	// not claim the original location was inaccessible.
	if !strings.Contains(report.String(), "将移动到 GPanel 数据目录") || strings.Contains(report.String(), "无法访问") {
		t.Errorf("move wording:\n%s", report.String())
	}

	// Running it again imports nothing new.
	res, err = Import(Options{GSMDir: gsm, DataDir: data})
	if err != nil || res.Imported != 0 || res.Skipped != 9 {
		t.Fatalf("second run: %+v, %v", res, err)
	}
}

func TestImportDryRunChangesNothing(t *testing.T) {
	gsm, games := fakeGSM(t, []map[string]any{
		{"id": "g2", "name": "bare-script", "workingDirectory": "pal2", "startCommand": "PalServer.sh", "stopCommand": "stop", "javaVersion": "java17"},
	})
	data := filepath.Join(t.TempDir(), "data")
	var report, dirs bytes.Buffer
	res, err := Import(Options{GSMDir: gsm, DataDir: data, DryRun: true, Report: &report, Dirs: &dirs})
	if err != nil || res.Imported != 1 {
		t.Fatalf("%+v, %v", res, err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Error("dry run created the data dir")
	}
	if st, _ := os.Stat(filepath.Join(games, "pal2", "PalServer.sh")); st.Mode()&0o111 != 0 {
		t.Error("dry run changed file modes")
	}
	if dirs.String() != filepath.Join(games, "pal2")+"\n" {
		t.Errorf("dirs = %q", dirs.String())
	}
	if !strings.Contains(report.String(), "复制 GSManager 安装的 JDK") {
		t.Errorf("report:\n%s", report.String())
	}
}

func TestImportPrefersExistingRuntime(t *testing.T) {
	gsm, _ := fakeGSM(t, []map[string]any{
		{"id": "g5", "name": "mc", "workingDirectory": "mcj", "startCommand": "x", "stopCommand": "stop", "instanceType": "minecraft-java", "javaVersion": "java17"},
	})
	data := t.TempDir()
	// GPanel already manages a Java 17.
	home := filepath.Join(data, "runtimes", "java", "jdk-17.0.12+7-jre")
	os.MkdirAll(filepath.Join(home, "bin"), 0o755)
	os.WriteFile(filepath.Join(home, "bin", "java"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(home, "release"), []byte("JAVA_VERSION=\"17.0.12\"\n"), 0o644)

	if _, err := Import(Options{GSMDir: gsm, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "runtimes", "java", "gsmanager-java17")); !os.IsNotExist(err) {
		t.Error("copied a JDK although GPanel already had Java 17")
	}
	m, _ := instance.NewManager(data, events.NewBus())
	if m.List()[0].Runtime == "" {
		t.Error("existing Java 17 not selected")
	}
}

func TestPrefixLocalScript(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "start.sh"), nil, 0o644)
	cases := map[string]string{
		"start.sh":        "./start.sh",
		"start.sh -a b":   "./start.sh -a b",
		`"start.sh" -a`:   `"./start.sh" -a`,
		"missing.sh":      "missing.sh",
		"./start.sh":      "./start.sh",
		"java -jar x.jar": "java -jar x.jar",
		"sub/start.sh":    "sub/start.sh",
	}
	for in, want := range cases {
		if got := prefixLocalScript(in, dir); got != want {
			t.Errorf("prefixLocalScript(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeSystemJDK makes a JDK of the given version visible as JAVA_HOME,
// standing in for a distribution package (CI runners ship several).
func fakeSystemJDK(t *testing.T, version string) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "system-jdk-"+version)
	os.MkdirAll(filepath.Join(home, "bin"), 0o755)
	os.WriteFile(filepath.Join(home, "bin", "java"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(home, "release"), []byte(`JAVA_VERSION="`+version+`"`+"\n"), 0o644)
	t.Setenv("JAVA_HOME", home)
	return home
}

func runtimeOf(t *testing.T, data, name string) string {
	t.Helper()
	m, err := instance.NewManager(data, events.NewBus())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range m.List() {
		if s.Name == name {
			return s.Runtime
		}
	}
	t.Fatalf("%s not imported", name)
	return ""
}

func TestImportPrefersGSManagerJDKOverSystemJava(t *testing.T) {
	fakeSystemJDK(t, "17.0.1")
	gsm, _ := fakeGSM(t, []map[string]any{
		{"id": "g5", "name": "mc", "workingDirectory": "mcj", "startCommand": "x", "stopCommand": "stop", "instanceType": "minecraft-java", "javaVersion": "java17"},
	})
	data := t.TempDir()
	if _, err := Import(Options{GSMDir: gsm, DataDir: data}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "runtimes", "java", "gsmanager-java17", "bin", "java")); err != nil {
		t.Fatalf("GSManager's JDK not copied although it is what the instance ran with: %v", err)
	}
	if runtimeOf(t, data, "mc") == "" {
		t.Error("no runtime selected")
	}
}

func TestImportFallsBackToSystemJava(t *testing.T) {
	fakeSystemJDK(t, "11.0.24")
	gsm, _ := fakeGSM(t, []map[string]any{ // GSManager has Java 17 and 21, not 11
		{"id": "g5", "name": "mc", "workingDirectory": "mcj", "startCommand": "x", "stopCommand": "stop", "instanceType": "minecraft-java", "javaVersion": "java11"},
	})
	data := t.TempDir()
	var report bytes.Buffer
	if _, err := Import(Options{GSMDir: gsm, DataDir: data, Report: &report}); err != nil {
		t.Fatal(err)
	}
	if runtimeOf(t, data, "mc") == "" || !strings.Contains(report.String(), "使用系统中的") {
		t.Errorf("system Java 11 not used:\n%s", report.String())
	}
	if _, err := os.Stat(filepath.Join(data, "runtimes", "java", "gsmanager-java11")); !os.IsNotExist(err) {
		t.Error("copied a JDK that GSManager doesn't have")
	}
}

// Start scripts as found on a real GSManager server: Forge's one-line
// run.sh and a hand-written multi-line exec, both calling GSManager's JDK.
const (
	forgeRunSh = `#!/usr/bin/env sh
# Forge requires a configured set of both JVM and program arguments.
/opt/gsmanager3/data/environment/Java/java21/jdk-21/bin/java @user_jvm_args.txt @libraries/net/minecraftforge/forge/1.20.1-47.4.16/unix_args.txt "$@"
`
	forgeRunShRewritten = `#!/usr/bin/env sh
# Forge requires a configured set of both JVM and program arguments.
java @user_jvm_args.txt @libraries/net/minecraftforge/forge/1.20.1-47.4.16/unix_args.txt "$@"
`
	execRunSh = `#!/bin/bash
exec /opt/gsmanager3/data/environment/Java/java21/jdk-21/bin/java \
  @user_jvm_args.txt \
  -XX:+UseG1GC \
  -jar server.jar nogui
`
	execRunShRewritten = `#!/bin/bash
exec java \
  @user_jvm_args.txt \
  -XX:+UseG1GC \
  -jar server.jar nogui
`
	homeRunSh = `#!/bin/sh
export JAVA_HOME=/opt/gsmanager3/data/environment/Java/java21/jdk-21
"/opt/gsmanager3/data/environment/Java/java21/jdk-21/bin/java" -jar server.jar
`
)

func scriptFixture(t *testing.T) (gsm, games string) {
	t.Helper()
	gsm, games = fakeGSM(t, []map[string]any{
		{"id": "s1", "name": "forge", "workingDirectory": "forge", "startCommand": "./run.sh", "stopCommand": "stop"},
		{"id": "s2", "name": "exec", "workingDirectory": "exec", "startCommand": "x", "stopCommand": "stop", "instanceType": "minecraft-java", "javaVersion": "java21"},
		{"id": "s3", "name": "mismatch", "workingDirectory": "mismatch", "startCommand": "bash run.sh", "stopCommand": "stop", "javaVersion": "java17"},
		{"id": "s4", "name": "home", "workingDirectory": "home", "startCommand": "run.sh", "stopCommand": "stop"},
	})
	for dir, body := range map[string]string{"forge": forgeRunSh, "exec": execRunSh, "mismatch": forgeRunSh, "home": homeRunSh} {
		os.MkdirAll(filepath.Join(games, dir), 0o755)
		if err := os.WriteFile(filepath.Join(games, dir, "run.sh"), []byte(body), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return gsm, games
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestScriptJavaRewritten(t *testing.T) {
	gsm, games := scriptFixture(t)
	data := t.TempDir()
	var report bytes.Buffer
	if _, err := Import(Options{GSMDir: gsm, DataDir: data, RewriteJava: true, Report: &report}); err != nil {
		t.Fatal(err)
	}
	t.Log(report.String())

	run := func(dir string) string { return filepath.Join(games, dir, "run.sh") }
	if got := readFile(t, run("forge")); got != forgeRunShRewritten {
		t.Errorf("forge run.sh rewritten to:\n%s", got)
	}
	if got := readFile(t, run("exec")); got != execRunShRewritten {
		t.Errorf("multi-line run.sh rewritten to:\n%s", got)
	}
	if readFile(t, run("forge")+backupSuffix) != forgeRunSh {
		t.Error("original not backed up")
	}
	// "bash run.sh" needs no chmod, so the rewrite alone must keep the mode.
	if st, _ := os.Stat(run("mismatch")); st.Mode().Perm() != 0o750 {
		t.Errorf("mode changed to %v", st.Mode().Perm())
	}

	// The instance without a Java version gets the one its script used,
	// the same runtime as the instance that asked for java21.
	java21 := runtimeOf(t, data, "exec")
	if java21 == "" || runtimeOf(t, data, "forge") != java21 || runtimeOf(t, data, "home") != java21 {
		t.Errorf("runtimes: forge %q exec %q home %q", runtimeOf(t, data, "forge"), java21, runtimeOf(t, data, "home"))
	}
	// The script wins over the instance's own (stale) setting.
	if runtimeOf(t, data, "mismatch") != java21 {
		t.Error("mismatch: script's Java 21 not used")
	}
	for _, w := range []string{"实例设置的是 java17，但脚本实际使用 java21", "仍有其他地方引用 GSManager 的 Java 目录"} {
		if !strings.Contains(report.String(), w) {
			t.Errorf("report lacks %q", w)
		}
	}
	// "home" keeps its JAVA_HOME line (flagged above) but the call itself
	// was replaced, quotes included.
	if got := readFile(t, run("home")); !strings.Contains(got, `"java" -jar server.jar`) || !strings.Contains(got, "JAVA_HOME=/opt/") {
		t.Errorf("home run.sh:\n%s", got)
	}

	// A second rewrite keeps the original backup.
	if _, err := rewriteJava(run("forge")); err != nil {
		t.Fatal(err)
	}
	if readFile(t, run("forge")+backupSuffix) != forgeRunSh {
		t.Error("backup overwritten by a second run")
	}
}

func TestScriptJavaWithoutRewrite(t *testing.T) {
	for _, dry := range []bool{false, true} {
		gsm, games := scriptFixture(t)
		data := t.TempDir()
		var report bytes.Buffer
		if _, err := Import(Options{GSMDir: gsm, DataDir: data, DryRun: dry, RewriteJava: dry, Report: &report}); err != nil {
			t.Fatal(err)
		}
		if readFile(t, filepath.Join(games, "forge", "run.sh")) != forgeRunSh {
			t.Errorf("dry=%v: script changed", dry)
		}
		if _, err := os.Stat(filepath.Join(games, "forge", "run.sh"+backupSuffix)); !os.IsNotExist(err) {
			t.Errorf("dry=%v: backup written", dry)
		}
		want := "-rewrite-java" // plain import: explain how to fix it
		if dry {
			want = "将把脚本中的 GSManager Java 路径改为 java"
		}
		if !strings.Contains(report.String(), "直接调用 GSManager 的 Java") || !strings.Contains(report.String(), want) {
			t.Errorf("dry=%v report:\n%s", dry, report.String())
		}
		if !dry && runtimeOf(t, data, "forge") == "" {
			t.Error("runtime not selected without rewriting")
		}
	}
}

func TestGSMJavaPattern(t *testing.T) {
	match := []string{
		"/opt/gsmanager3/data/environment/Java/java21/jdk-21/bin/java",
		"/srv/gsm/data/environment/Java/java8/java-se-8u44-ri/bin/java",
		"/opt/gsmanager3/data/environment/Java/java25/bin/java",
	}
	noMatch := []string{
		"/opt/gsmanager3/data/environment/Java/java21/jdk-21/bin/javac",
		"/usr/lib/jvm/java-21-openjdk-amd64/bin/java",
		"/opt/gsmanager3/data/environment/Java/java21/jdk-21",
	}
	for _, s := range match {
		if !gsmJavaPath.MatchString(s) {
			t.Errorf("should match: %s", s)
		}
	}
	for _, s := range noMatch {
		if gsmJavaPath.MatchString(s) {
			t.Errorf("should not match: %s", s)
		}
	}
}
