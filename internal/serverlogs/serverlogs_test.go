package serverlogs

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = `[10:00:00] [main/INFO] [loader]: Loading 200 mods
[10:00:01] [main/WARN] [mixin]: Mixin config x.json does not specify "minVersion"
[10:00:02] [Server thread/ERROR] [minecraft/Main]: Failed to load registry
java.lang.IllegalStateException: boom
	at net.minecraft.Foo.bar(Foo.java:42)
[10:00:03] [Server thread/INFO] [minecraft/DedicatedServer]: Done (4.4s)! For help, type "help"
[10:00:04] [Server thread/INFO]: Steve joined the game
`

func setup(t *testing.T) (string, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "logs", "nested"), 0o755)
	os.MkdirAll(filepath.Join(dir, "crash-reports"), 0o755)
	os.WriteFile(filepath.Join(dir, "logs", "latest.log"), []byte(sample), 0o644)
	os.WriteFile(filepath.Join(dir, "logs", "nested", "x.log"), []byte("no"), 0o644)
	os.WriteFile(filepath.Join(dir, "logs", "notes.json"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(dir, "crash-reports", "crash-2026-10-27.txt"), []byte("---- Minecraft Crash Report ----\n"), 0o644)
	f, _ := os.Create(filepath.Join(dir, "logs", "2026-10-27-1.log.gz"))
	gz := gzip.NewWriter(f)
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(gz, "[old] line %d\n", i)
	}
	gz.Close()
	f.Close()
	old := time.Now().Add(-48 * time.Hour)
	os.Chtimes(filepath.Join(dir, "logs", "2026-10-27-1.log.gz"), old, old)
	os.Symlink("/etc/hosts", filepath.Join(dir, "logs", "escape.log"))

	mux := http.NewServeMux()
	NewHandler(func(string) (string, error) { return dir, nil }).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return dir, srv
}

func get(t *testing.T, srv *httptest.Server, path string, out any) int {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestList(t *testing.T) {
	_, srv := setup(t)
	var files []File
	get(t, srv, "/api/instances/i/logs", &files)
	var names []string
	for _, f := range files {
		names = append(names, f.Path)
	}
	// Regular log files only (no subdirs, no .json, no symlinks), newest first.
	got := strings.Join(names, ",")
	if !strings.HasSuffix(got, "logs/2026-10-27-1.log.gz") || !strings.Contains(got, "logs/latest.log") ||
		!strings.Contains(got, "crash-reports/crash-2026-10-27.txt") || len(files) != 3 {
		t.Fatalf("files = %s", got)
	}
}

func TestViewFilterAndPaging(t *testing.T) {
	_, srv := setup(t)
	var p Page
	get(t, srv, "/api/instances/i/logs/view?file=logs/latest.log", &p)
	if p.Total != 7 || len(p.Lines) != 7 || p.Lines[0].N != 1 || p.More {
		t.Fatalf("all: %+v", p)
	}
	get(t, srv, "/api/instances/i/logs/view?file=logs/latest.log&level=error", &p)
	if p.Matched != 3 { // ERROR line, exception, stack frame
		t.Errorf("errors matched %d: %+v", p.Matched, p.Lines)
	}
	get(t, srv, "/api/instances/i/logs/view?file=logs/latest.log&level=warn", &p)
	if p.Matched != 4 {
		t.Errorf("warn+error matched %d", p.Matched)
	}
	get(t, srv, "/api/instances/i/logs/view?file=logs/latest.log&q=STEVE", &p)
	if p.Matched != 1 || p.Lines[0].N != 7 {
		t.Errorf("search: %+v", p)
	}

	// Gzipped, paged from the end: 50 lines, 20 at a time.
	get(t, srv, "/api/instances/i/logs/view?file=logs/2026-10-27-1.log.gz&limit=20", &p)
	if p.Total != 50 || len(p.Lines) != 20 || p.Lines[0].N != 31 || !p.More {
		t.Fatalf("gz last page: total %d, first %d, more %v", p.Total, p.Lines[0].N, p.More)
	}
	get(t, srv, "/api/instances/i/logs/view?file=logs/2026-10-27-1.log.gz&limit=20&before=11", &p)
	if len(p.Lines) != 10 || p.Lines[0].N != 1 || p.Lines[9].N != 10 || p.More {
		t.Fatalf("gz first page: %d lines from %d, more %v", len(p.Lines), p.Lines[0].N, p.More)
	}
}

func TestViewRefusesOtherFiles(t *testing.T) {
	_, srv := setup(t)
	for _, bad := range []string{
		"server.properties", "logs/../server.properties", "../../etc/passwd", "logs/nested/x.log",
		"logs/notes.json", "world/level.dat", "logs/escape.log", "logs/",
	} {
		if code := get(t, srv, "/api/instances/i/logs/view?file="+bad, nil); code == 200 {
			t.Errorf("served %q", bad)
		}
		if code := get(t, srv, "/api/instances/i/logs/download?file="+bad, nil); code == 200 {
			t.Errorf("downloaded %q", bad)
		}
	}
	if code := get(t, srv, "/api/instances/i/logs/download?file=logs/2026-10-27-1.log.gz", nil); code != 200 {
		t.Errorf("download = %d", code)
	}
}

func TestLongLinesTruncated(t *testing.T) {
	p, err := read(strings.NewReader(strings.Repeat("x", 10000)+"\nshort\n"), Filter{}, 10, 0)
	if err != nil || len(p.Lines) != 2 || len(p.Lines[0].Text) > maxLineLen+10 {
		t.Fatalf("%v %d", err, len(p.Lines[0].Text))
	}
}
