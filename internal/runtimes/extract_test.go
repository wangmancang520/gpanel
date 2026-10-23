package runtimes

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeLink(t *testing.T) {
	cases := []struct {
		name, target string
		ok           bool
	}{
		{"jdk/Contents/MacOS/libjli.dylib", "../Home/lib/libjli.dylib", true},
		{"jdk/legal/java.sql/LICENSE", "../java.base/LICENSE", true},
		{"jdk/bin/evil", "../../../etc/passwd", false},
		{"jdk/bin/evil", "/etc/passwd", false},
		{"top", "..", false},
	}
	for _, c := range cases {
		if got := safeLink(c.name, c.target); got != c.ok {
			t.Errorf("safeLink(%q, %q) = %v, want %v", c.name, c.target, got, c.ok)
		}
	}
}

func TestExtractTarGz(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	add(&tar.Header{Name: "jdk/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(&tar.Header{Name: "jdk/bin/java", Typeflag: tar.TypeReg, Mode: 0o755}, "#!/bin/sh\n")
	add(&tar.Header{Name: "jdk/release", Typeflag: tar.TypeReg, Mode: 0o644}, `JAVA_VERSION="21"`)
	add(&tar.Header{Name: "jdk/bin/java2", Typeflag: tar.TypeSymlink, Linkname: "java"}, "")
	add(&tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644}, "x") // skipped
	tw.Close()
	gz.Close()

	dir := t.TempDir()
	f := filepath.Join(t.TempDir(), "a.tar.gz")
	os.WriteFile(f, buf.Bytes(), 0o644)
	fh, _ := os.Open(f)
	defer fh.Close()
	if err := extractArchive(fh, "a.tar.gz", dir); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "jdk/bin/java"))
	if err != nil || st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("bin/java missing or not executable: %v %v", st, err)
	}
	if target, _ := os.Readlink(filepath.Join(dir, "jdk/bin/java2")); target != "java" {
		t.Fatalf("symlink target = %q", target)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); err == nil {
		t.Fatal("entry escaped the destination")
	}

	// An archive with an escaping symlink must be rejected.
	buf.Reset()
	gz = gzip.NewWriter(&buf)
	tw = tar.NewWriter(gz)
	add(&tar.Header{Name: "jdk/evil", Typeflag: tar.TypeSymlink, Linkname: "../../etc"}, "")
	tw.Close()
	gz.Close()
	os.WriteFile(f, buf.Bytes(), 0o644)
	fh2, _ := os.Open(f)
	defer fh2.Close()
	if err := extractArchive(fh2, "a.tar.gz", t.TempDir()); err == nil {
		t.Fatal("expected escaping symlink to be rejected")
	}
}
