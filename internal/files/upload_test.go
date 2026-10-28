package files

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResumableUpload(t *testing.T) {
	fsys, dir := setup(t)
	data := bytes.Repeat([]byte("0123456789"), 1000) // 10 KB
	u := UploadSpec{Dir: "/b", Name: "big.zip", Size: int64(len(data)), Modified: 1700000000000}

	off, err := fsys.BeginUpload(u)
	if err != nil || off != 0 {
		t.Fatalf("begin = %d, %v", off, err)
	}
	// First chunk.
	if cur, done, err := fsys.WriteChunk(u, 0, bytes.NewReader(data[:4000])); err != nil || cur != 4000 || done {
		t.Fatalf("chunk 1 = %d %v %v", cur, done, err)
	}
	// The connection drops mid-chunk: what arrived is kept.
	cur, _, err := fsys.WriteChunk(u, 4000, io.MultiReader(bytes.NewReader(data[4000:5500]), errReader{}))
	if err == nil || cur != 5500 {
		t.Fatalf("interrupted chunk = %d, %v", cur, err)
	}
	// Not visible in listings while in progress.
	entries, _ := fsys.List("/b")
	for _, e := range entries {
		if strings.HasPrefix(e.Name, partialPrefix) || e.Name == "big.zip" {
			t.Fatalf("partial upload listed: %s", e.Name)
		}
	}
	// "Reload the page": begin again resumes from what the server has.
	if off, _ := fsys.BeginUpload(u); off != 5500 {
		t.Fatalf("resume offset = %d, want 5500", off)
	}
	// A chunk at the wrong offset is refused with the right one.
	var oe *OffsetError
	if _, _, err := fsys.WriteChunk(u, 4000, bytes.NewReader(data[4000:])); !errors.As(err, &oe) || oe.Have != 5500 {
		t.Fatalf("wrong offset: %v", err)
	}
	// Finish.
	if cur, done, err := fsys.WriteChunk(u, 5500, bytes.NewReader(data[5500:])); err != nil || !done || cur != u.Size {
		t.Fatalf("last chunk = %d %v %v", cur, done, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "b", "big.zip")); !bytes.Equal(got, data) {
		t.Fatal("assembled file differs")
	}
	if _, err := os.Stat(filepath.Join(dir, "b", partialPrefix+u.ID())); !os.IsNotExist(err) {
		t.Fatal("partial left behind")
	}
	// The partial is gone, so another chunk can't reopen it.
	if _, _, err := fsys.WriteChunk(u, u.Size, strings.NewReader("")); !errors.Is(err, ErrUploadGone) {
		t.Fatalf("chunk after completion: %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestUploadEdgeCases(t *testing.T) {
	fsys, dir := setup(t)

	// Replaces an existing file once complete (the browser asked first).
	u := UploadSpec{Dir: "/", Name: "z.txt", Size: 3, Modified: 1}
	fsys.BeginUpload(u)
	if _, done, err := fsys.WriteChunk(u, 0, strings.NewReader("new")); err != nil || !done {
		t.Fatal(err)
	}
	if read(t, dir, "z.txt") != "new" {
		t.Fatal("existing file not replaced")
	}

	// Empty files complete with an empty chunk.
	e := UploadSpec{Dir: "/", Name: "empty", Size: 0, Modified: 1}
	fsys.BeginUpload(e)
	if _, done, err := fsys.WriteChunk(e, 0, strings.NewReader("")); err != nil || !done {
		t.Fatalf("empty file: %v", err)
	}

	// More data than declared is refused and rolled back.
	o := UploadSpec{Dir: "/", Name: "o.bin", Size: 4, Modified: 1}
	fsys.BeginUpload(o)
	if _, _, err := fsys.WriteChunk(o, 0, strings.NewReader("too long")); err == nil {
		t.Fatal("oversized data accepted")
	}
	if off, _ := fsys.BeginUpload(o); off != 0 {
		t.Fatalf("oversized chunk left %d bytes", off)
	}

	// Cancel discards the partial.
	fsys.WriteChunk(o, 0, strings.NewReader("ab"))
	if err := fsys.CancelUpload(o); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fsys.WriteChunk(o, 2, strings.NewReader("cd")); !errors.Is(err, ErrUploadGone) {
		t.Fatalf("chunk after cancel: %v", err)
	}

	// Bad names, a different file under the same name, escaping the root.
	for _, bad := range []UploadSpec{
		{Dir: "/", Name: "../x"}, {Dir: "/", Name: "a/b"}, {Dir: "/", Name: ""},
		{Dir: "/", Name: partialPrefix + "x"}, {Dir: "/z.txt", Name: "f", Size: 1},
	} {
		if _, err := fsys.BeginUpload(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if (UploadSpec{Dir: "/", Name: "a", Size: 1, Modified: 1}).ID() == (UploadSpec{Dir: "/", Name: "a", Size: 1, Modified: 2}).ID() {
		t.Error("a changed file must not resume the old upload")
	}

	// Week-old partials are swept when a new upload starts in that dir.
	stale := filepath.Join(dir, partialPrefix+"abandoned")
	os.WriteFile(stale, []byte("x"), 0o644)
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(stale, old, old)
	fsys.BeginUpload(UploadSpec{Dir: "/", Name: "n", Size: 1, Modified: 1})
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale partial not swept")
	}
}

func TestUnzipLimits(t *testing.T) {
	fsys, dir := setup(t)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("big.bin")
	w.Write(bytes.Repeat([]byte{0}, 1<<20)) // 1 MiB, compresses to almost nothing
	zw.Close()
	os.WriteFile(filepath.Join(dir, "bomb.zip"), buf.Bytes(), 0o644)

	// Pretend only 512 MiB is free: 1 MiB + the 1 GiB reserve doesn't fit.
	freeSpace = func(string) (uint64, bool) { return 512 << 20, true }
	t.Cleanup(func() { freeSpace = diskFree })
	if err := fsys.Unzip("/bomb.zip", "/out"); err == nil || !strings.Contains(err.Error(), "磁盘剩余") {
		t.Fatalf("unzip on a full disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "big.bin")); !os.IsNotExist(err) {
		t.Fatal("extracted despite the refusal")
	}
	// Plenty of space: fine.
	freeSpace = func(string) (uint64, bool) { return 10 << 30, true }
	if err := fsys.Unzip("/bomb.zip", "/out"); err != nil {
		t.Fatal(err)
	}
}
