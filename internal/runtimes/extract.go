package runtimes

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// extractArchive unpacks a .tar.gz or .zip into dest. Writes go through
// os.Root so entries can't escape dest; symlink targets are checked too,
// because os.Root only guards our own accesses, not the JVM's later ones.
func extractArchive(f *os.File, name, dest string) error {
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	switch {
	case strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".tgz"):
		return extractTarGz(f, root)
	case strings.HasSuffix(name, ".zip"):
		st, err := f.Stat()
		if err != nil {
			return err
		}
		return extractZip(f, st.Size(), root)
	}
	return fmt.Errorf("不支持的压缩格式: %s", name)
}

func extractTarGz(r io.Reader, root *os.Root) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name, ok := archivePath(hdr.Name)
		if !ok {
			continue
		}
		mode := fs.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(name, 0o755)
		case tar.TypeReg:
			err = writeEntry(root, name, mode, tr)
		case tar.TypeSymlink:
			if !safeLink(name, hdr.Linkname) {
				return fmt.Errorf("不安全的符号链接 %s -> %s", hdr.Name, hdr.Linkname)
			}
			if err = root.MkdirAll(filepath.Dir(name), 0o755); err == nil {
				err = root.Symlink(hdr.Linkname, name)
			}
		case tar.TypeLink:
			target, ok := archivePath(hdr.Linkname)
			if !ok {
				return fmt.Errorf("不安全的硬链接 %s", hdr.Name)
			}
			if err = root.MkdirAll(filepath.Dir(name), 0o755); err == nil {
				err = root.Link(target, name)
			}
		}
		if err != nil {
			return err
		}
	}
}

func extractZip(r io.ReaderAt, size int64, root *os.Root) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return err
	}
	for _, zf := range zr.File {
		name, ok := archivePath(zf.Name)
		if !ok {
			continue
		}
		mode := zf.Mode()
		switch {
		case mode.IsDir():
			err = root.MkdirAll(name, 0o755)
		case mode&fs.ModeSymlink != 0:
			continue // Windows JDK zips have none
		default:
			var rc io.ReadCloser
			if rc, err = zf.Open(); err == nil {
				err = writeEntry(root, name, mode.Perm(), rc)
				rc.Close()
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func writeEntry(root *os.Root, name string, mode fs.FileMode, r io.Reader) error {
	if mode == 0 {
		mode = 0o644
	}
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Chmod(name, mode) // undo umask so bin/java stays executable
}

// archivePath validates an entry name and converts it to an OS path.
func archivePath(p string) (string, bool) {
	p = path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if p == "." || path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return filepath.FromSlash(p), true
}

// safeLink reports whether a relative symlink at name stays inside the archive.
func safeLink(name, target string) bool {
	if target == "" || path.IsAbs(target) || strings.Contains(target, "\\") {
		return false
	}
	joined := path.Join(path.Dir(filepath.ToSlash(name)), target)
	return joined != ".." && !strings.HasPrefix(joined, "../")
}
