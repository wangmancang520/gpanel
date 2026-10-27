package files

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func setup(t *testing.T) (*FS, string) {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"a/sub", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"a/x.txt": "x", "a/sub/y.txt": "y", "z.txt": "z"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fsys, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fsys.Close() })
	return fsys, dir
}

func read(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMove(t *testing.T) {
	fsys, dir := setup(t)
	if err := fsys.Move([]string{"/z.txt", "/a"}, "/b"); err != nil {
		t.Fatal(err)
	}
	if read(t, dir, "b/z.txt") != "z" || read(t, dir, "b/a/sub/y.txt") != "y" {
		t.Fatal("moved content missing")
	}
	if _, err := os.Stat(filepath.Join(dir, "z.txt")); !os.IsNotExist(err) {
		t.Fatal("source still exists")
	}
	// Moving within the same directory is a no-op.
	if err := fsys.Move([]string{"/b/z.txt"}, "/b"); err != nil {
		t.Fatal(err)
	}
}

func TestMoveRejects(t *testing.T) {
	fsys, dir := setup(t)
	os.WriteFile(filepath.Join(dir, "b", "z.txt"), []byte("other"), 0o644)
	cases := map[string]struct {
		paths []string
		dest  string
	}{
		"existing target": {[]string{"/z.txt"}, "/b"},
		"into itself":     {[]string{"/a"}, "/a"},
		"into child":      {[]string{"/a"}, "/a/sub"},
		"root":            {[]string{"/"}, "/b"},
		"dest is a file":  {[]string{"/a"}, "/z.txt"},
		"escape":          {[]string{"/z.txt"}, "/../.."},
	}
	for name, c := range cases {
		if name == "escape" {
			// clean() pins ".." to the root, so this is a same-dir move; it must
			// stay inside the sandbox rather than fail.
			if err := fsys.Move(c.paths, c.dest); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err := fsys.Move(c.paths, c.dest); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if read(t, dir, "b/z.txt") != "other" {
		t.Fatal("existing file was overwritten")
	}
}

func TestCopy(t *testing.T) {
	fsys, dir := setup(t)
	created, err := fsys.Copy([]string{"/a", "/z.txt"}, "/b")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(created, []string{"a", "z.txt"}) {
		t.Fatalf("created = %v", created)
	}
	if read(t, dir, "b/a/sub/y.txt") != "y" || read(t, dir, "a/x.txt") != "x" {
		t.Fatal("copy incomplete or source changed")
	}

	// Copying into the same directory makes numbered duplicates.
	for _, want := range []string{"z - 副本.txt", "z - 副本 (2).txt"} {
		created, err := fsys.Copy([]string{"/z.txt"}, "/")
		if err != nil || len(created) != 1 || created[0] != want {
			t.Fatalf("Copy = %v, %v; want %s", created, err, want)
		}
	}
	if _, err := fsys.Copy([]string{"/a"}, "/a/sub"); err == nil {
		t.Fatal("copy into own subdirectory accepted")
	}
}

func TestCopySkipsSymlinks(t *testing.T) {
	fsys, dir := setup(t)
	if err := os.Symlink("/etc", filepath.Join(dir, "a", "link")); err != nil {
		t.Skip(err)
	}
	if _, err := fsys.Copy([]string{"/a"}, "/b"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "b", "a", "link")); !os.IsNotExist(err) {
		t.Fatal("symlink was copied")
	}
}
