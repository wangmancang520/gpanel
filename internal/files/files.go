// Package files is a sandboxed file manager.
//
// Every operation goes through os.Root, which refuses any path (including
// via ".." or symlinks) that resolves outside the root directory.
package files

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const MaxEditSize = 5 << 20 // files larger than this can't be opened in the editor

type Entry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"isDir"`
	Symlink bool      `json:"symlink,omitempty"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"modTime"`
}

// FS is a file manager rooted at one directory.
type FS struct{ root *os.Root }

func Open(dir string) (*FS, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &FS{root: r}, nil
}

func (f *FS) Close() error { return f.root.Close() }

// clean turns a client path ("/a/b", "a/b", "") into a root-relative OS path.
func clean(p string) string {
	p = path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	if p == "/" {
		return "."
	}
	return filepath.FromSlash(strings.TrimPrefix(p, "/"))
}

func (f *FS) List(dir string) ([]Entry, error) {
	d, err := f.root.Open(clean(dir))
	if err != nil {
		return nil, err
	}
	defer d.Close()
	des, err := d.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(des))
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			continue
		}
		e := Entry{Name: de.Name(), IsDir: de.IsDir(), Size: info.Size(), Mode: info.Mode().String(), ModTime: info.ModTime()}
		if info.Mode()&fs.ModeSymlink != 0 {
			e.Symlink = true
			if st, err := f.root.Stat(filepath.Join(clean(dir), de.Name())); err == nil {
				e.IsDir = st.IsDir()
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].IsDir != out[b].IsDir {
			return out[a].IsDir
		}
		return strings.ToLower(out[a].Name) < strings.ToLower(out[b].Name)
	})
	return out, nil
}

func (f *FS) ReadText(p string) ([]byte, error) {
	st, err := f.root.Stat(clean(p))
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, errors.New("不能编辑目录")
	}
	if st.Size() > MaxEditSize {
		return nil, fmt.Errorf("文件超过 %d MB，请下载后编辑", MaxEditSize>>20)
	}
	return f.root.ReadFile(clean(p))
}

func (f *FS) WriteText(p string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := f.root.Stat(clean(p)); err == nil {
		mode = st.Mode().Perm()
	}
	return f.root.WriteFile(clean(p), data, mode)
}

// OpenFile opens a regular file for download.
func (f *FS) OpenFile(p string) (*os.File, fs.FileInfo, error) {
	file, err := f.root.Open(clean(p))
	if err != nil {
		return nil, nil, err
	}
	st, err := file.Stat()
	if err != nil || st.IsDir() {
		file.Close()
		return nil, nil, errors.New("只能下载文件")
	}
	return file, st, nil
}

// Save streams r into dir/name, replacing any existing file.
func (f *FS) Save(dir, name string, r io.Reader) error {
	name = filepath.Base(filepath.FromSlash(name))
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return errors.New("无效文件名")
	}
	out, err := f.root.OpenFile(filepath.Join(clean(dir), name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func (f *FS) Mkdir(p string) error { return f.root.MkdirAll(clean(p), 0o755) }

func (f *FS) Rename(from, to string) error {
	if clean(from) == "." || clean(to) == "." {
		return errors.New("不能重命名根目录")
	}
	return f.root.Rename(clean(from), clean(to))
}

// Move moves each of paths into destDir, keeping its name. Moving an item
// onto itself is a no-op; an existing target or moving a directory into
// itself is an error.
func (f *FS) Move(paths []string, destDir string) error {
	dest, err := f.dirOf(destDir)
	if err != nil {
		return err
	}
	for _, p := range paths {
		src := clean(p)
		if err := checkSource(src, dest); err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.Base(src))
		if target == src {
			continue
		}
		if _, err := f.root.Lstat(target); err == nil {
			return fmt.Errorf("目标已存在：%s", filepath.Base(src))
		}
		if err := f.moveNoReplace(src, target); err != nil {
			return err
		}
	}
	return nil
}

// moveNoReplace renames src to target without replacing a target that
// appeared after Move's check (a game server writing files, another tab).
// Non-directories go through a hard link, which fails atomically if target
// exists; rename(2) can only replace an empty directory with a directory, so
// directories lose nothing either way. Filesystems without hard links fall
// back to a plain rename.
func (f *FS) moveNoReplace(src, target string) error {
	st, err := f.root.Lstat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		err := f.root.Link(src, target)
		if err == nil {
			return f.root.Remove(src)
		}
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("目标已存在：%s", filepath.Base(target))
		}
	}
	return f.root.Rename(src, target)
}

// Copy copies each of paths (recursively) into destDir. Name clashes get a
// " - 副本" suffix, so copying into the same directory duplicates. Symlinks
// are skipped, as in Zip. Returns the names created in destDir.
func (f *FS) Copy(paths []string, destDir string) ([]string, error) {
	dest, err := f.dirOf(destDir)
	if err != nil {
		return nil, err
	}
	var created []string
	for _, p := range paths {
		src := clean(p)
		if err := checkSource(src, dest); err != nil {
			return created, err
		}
		name := f.freeName(dest, filepath.Base(src))
		if err := f.copyTree(src, filepath.Join(dest, name)); err != nil {
			return created, err
		}
		created = append(created, name)
	}
	return created, nil
}

// dirOf cleans p and checks that it is an existing directory.
func (f *FS) dirOf(p string) (string, error) {
	d := clean(p)
	st, err := f.root.Stat(d)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", errors.New("目标不是文件夹")
	}
	return d, nil
}

func checkSource(src, dest string) error {
	if src == "." {
		return errors.New("不能移动或复制根目录")
	}
	if dest == src || strings.HasPrefix(dest, src+string(filepath.Separator)) {
		return fmt.Errorf("不能把 %s 放进它自己里面", filepath.Base(src))
	}
	return nil
}

// freeName returns name, or "base - 副本[ (n)].ext" if name is taken in dir.
func (f *FS) freeName(dir, name string) string {
	taken := func(n string) bool {
		_, err := f.root.Lstat(filepath.Join(dir, n))
		return err == nil
	}
	if !taken(name) {
		return name
	}
	ext := filepath.Ext(name)
	if ext == name { // ".env": treat the whole name as the base
		ext = ""
	}
	base := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		n := base + " - 副本" + ext
		if i > 1 {
			n = fmt.Sprintf("%s - 副本 (%d)%s", base, i, ext)
		}
		if !taken(n) {
			return n
		}
	}
}

func (f *FS) copyTree(src, dst string) error {
	st, err := f.root.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		return nil
	case st.IsDir():
		if err := f.root.Mkdir(dst, st.Mode().Perm()|0o700); err != nil {
			return err
		}
		d, err := f.root.Open(src)
		if err != nil {
			return err
		}
		names, err := d.Readdirnames(-1)
		d.Close()
		if err != nil {
			return err
		}
		for _, n := range names {
			if err := f.copyTree(filepath.Join(src, n), filepath.Join(dst, n)); err != nil {
				return err
			}
		}
		return nil
	case !st.Mode().IsRegular():
		return nil // devices, sockets, ...
	}
	in, err := f.root.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := f.root.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func (f *FS) Remove(p string) error {
	if clean(p) == "." {
		return errors.New("不能删除根目录")
	}
	return f.root.RemoveAll(clean(p))
}

// Zip packs paths (files or directories) into the archive at dest.
func (f *FS) Zip(paths []string, dest string) error {
	out, err := f.root.OpenFile(clean(dest), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	fsys := f.root.FS()
	destRel := filepath.ToSlash(clean(dest))
	for _, p := range paths {
		rel := filepath.ToSlash(clean(p))
		base := path.Dir(rel)
		err := fs.WalkDir(fsys, rel, func(name string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if name == destRel || d.Type()&fs.ModeSymlink != 0 {
				return nil // skip the archive itself and symlinks
			}
			entryName := strings.TrimPrefix(strings.TrimPrefix(name, base), "/")
			if base == "." {
				entryName = name
			}
			if d.IsDir() {
				_, err := zw.Create(entryName + "/")
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			hdr, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			hdr.Name, hdr.Method = entryName, zip.Deflate
			w, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			src, err := fsys.Open(name)
			if err != nil {
				return err
			}
			defer src.Close()
			_, err = io.Copy(w, src)
			return err
		})
		if err != nil {
			zw.Close()
			out.Close()
			f.root.Remove(clean(dest))
			return err
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Unzip extracts archive into destDir. os.Root blocks zip-slip entries.
func (f *FS) Unzip(archive, destDir string) error {
	file, err := f.root.Open(clean(archive))
	if err != nil {
		return err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(file, st.Size())
	if err != nil {
		return err
	}
	dest := clean(destDir)
	for _, zf := range zr.File {
		target := filepath.Join(dest, clean(zf.Name))
		mode := zf.Mode()
		switch {
		case mode&fs.ModeSymlink != 0:
			continue
		case mode.IsDir():
			if err := f.root.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := f.root.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := f.extract(zf, target); err != nil {
			return fmt.Errorf("%s: %w", zf.Name, err)
		}
	}
	return nil
}

func (f *FS) extract(zf *zip.File, target string) error {
	src, err := zf.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	perm := zf.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := f.root.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
