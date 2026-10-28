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
	fakeSystemJDK(t, "21.0.4")
	gsm, _ := fakeGSM(t, []map[string]any{ // GSManager has only Java 17 installed
		{"id": "g5", "name": "mc", "workingDirectory": "mcj", "startCommand": "x", "stopCommand": "stop", "instanceType": "minecraft-java", "javaVersion": "java21"},
	})
	data := t.TempDir()
	var report bytes.Buffer
	if _, err := Import(Options{GSMDir: gsm, DataDir: data, Report: &report}); err != nil {
		t.Fatal(err)
	}
	if runtimeOf(t, data, "mc") == "" || !strings.Contains(report.String(), "使用系统中的") {
		t.Errorf("system Java 21 not used:\n%s", report.String())
	}
	if _, err := os.Stat(filepath.Join(data, "runtimes", "java", "gsmanager-java21")); !os.IsNotExist(err) {
		t.Error("copied a JDK that GSManager doesn't have")
	}
}
