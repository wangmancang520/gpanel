package runtimes

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Java finds JDK/JRE installs in well-known system locations, JAVA_HOME,
// PATH, and the panel-managed directory (<data>/runtimes/java/<any>/).
// Unpacking a JDK archive into the managed directory is enough to register it;
// Releases makes Temurin builds installable from the panel.
type Java struct {
	ManagedDir string
	Adoptium   *Adoptium
}

func (j *Java) Kind() string { return "java" }

func (j *Java) InstallDir() string { return j.ManagedDir }

func (j *Java) Releases(ctx context.Context) ([]Release, error) {
	return j.Adoptium.Releases(ctx)
}

func (j *Java) Defaults() map[string]string { return map[string]string{"java": "java"} }

func (j *Java) Launch(rt Runtime) (map[string]string, map[string]string, []string) {
	return map[string]string{"java": rt.Executable},
		map[string]string{"JAVA_HOME": rt.Home},
		[]string{filepath.Join(rt.Home, "bin")}
}

func (j *Java) Detect(ctx context.Context) []Runtime {
	type cand struct {
		home    string
		managed bool
	}
	var cands []cand
	add := func(managed bool, patterns ...string) {
		for _, pat := range patterns {
			matches, _ := filepath.Glob(pat)
			for _, m := range matches {
				cands = append(cands, cand{m, managed})
			}
		}
	}

	if j.ManagedDir != "" {
		add(true, filepath.Join(j.ManagedDir, "*"), filepath.Join(j.ManagedDir, "*", "Contents", "Home"))
	}
	if h := os.Getenv("JAVA_HOME"); h != "" {
		cands = append(cands, cand{h, false})
	}
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		add(false, "/Library/Java/JavaVirtualMachines/*/Contents/Home",
			filepath.Join(home, "Library/Java/JavaVirtualMachines/*/Contents/Home"),
			"/opt/homebrew/opt/openjdk*/libexec/openjdk.jdk/Contents/Home")
	case "windows":
		add(false, `C:\Program Files\Java\*`, `C:\Program Files\Eclipse Adoptium\*`,
			`C:\Program Files\Microsoft\jdk-*`, `C:\Program Files\Zulu\*`)
	default:
		add(false, "/usr/lib/jvm/*", "/usr/java/*", "/opt/java/*", "/opt/jdk*")
	}
	if p, err := exec.LookPath(javaBin()); err == nil {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			cands = append(cands, cand{filepath.Dir(filepath.Dir(real)), false})
		}
	}

	seen := map[string]bool{}
	var out []Runtime
	for _, c := range cands {
		home, err := filepath.EvalSymlinks(c.home)
		if err != nil || seen[home] {
			continue
		}
		seen[home] = true
		if rt, ok := j.inspect(ctx, home, c.managed); ok {
			out = append(out, rt)
		}
	}
	return out
}

func (j *Java) inspect(ctx context.Context, home string, managed bool) (Runtime, bool) {
	exe := filepath.Join(home, "bin", javaBin())
	if st, err := os.Stat(exe); err != nil || st.IsDir() {
		return Runtime{}, false
	}
	// macOS ships a /usr/bin/java stub that pops an install dialog; never run it.
	if runtime.GOOS == "darwin" && home == "/usr" {
		return Runtime{}, false
	}
	version, vendor := readRelease(home)
	if version == "" {
		version = execVersion(ctx, exe)
	}
	if version == "" {
		return Runtime{}, false
	}
	major := majorVersion(version)
	name := "Java " + version
	if vendor != "" {
		name += " (" + vendor + ")"
	}
	return Runtime{
		ID: runtimeID("java", home), Kind: "java", Name: name,
		Version: version, Major: major, Vendor: vendor,
		Home: home, Executable: exe, Managed: managed,
	}, true
}

// readRelease parses the JDK's "release" file, which avoids spawning java.
func readRelease(home string) (version, vendor string) {
	f, err := os.Open(filepath.Join(home, "release"))
	if err != nil {
		return "", ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "JAVA_VERSION":
			version = v
		case "IMPLEMENTOR":
			vendor = v
		}
	}
	return version, vendor
}

var versionRe = regexp.MustCompile(`version "([^"]+)"`)

func execVersion(ctx context.Context, exe string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "-version").CombinedOutput()
	if err != nil {
		return ""
	}
	if m := versionRe.FindSubmatch(out); m != nil {
		return string(bytes.TrimSpace(m[1]))
	}
	return ""
}

// majorVersion handles both "1.8.0_392" (-> 8) and "21.0.2" (-> 21).
func majorVersion(v string) int {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == '+' })
	if len(parts) == 0 {
		return 0
	}
	n, _ := strconv.Atoi(parts[0])
	if n == 1 && len(parts) > 1 {
		n, _ = strconv.Atoi(parts[1])
	}
	return n
}

func javaBin() string {
	if runtime.GOOS == "windows" {
		return "java.exe"
	}
	return "java"
}
