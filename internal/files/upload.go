package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Resumable uploads. A file goes up in chunks, appended to a hidden partial
// file in its target directory, and is renamed into place once complete
// (same directory, so no extra space and no copy).
//
// An upload's ID is a hash of where it goes and what it is (directory,
// name, size, modification time), so the server keeps no state: sending
// the same file again after a dropped connection, a page reload or a panel
// restart continues from what the partial file already holds.

const (
	partialPrefix = ".gpanel-upload-"
	MaxChunk      = 16 << 20 // largest chunk accepted per request
	partialTTL    = 7 * 24 * time.Hour
)

// UploadSpec identifies one upload.
type UploadSpec struct {
	Dir      string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"` // the browser's File.lastModified, ms
}

func (u UploadSpec) ID() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%d", filepath.ToSlash(clean(u.Dir)), u.Name, u.Size, u.Modified)))
	return hex.EncodeToString(h[:12])
}

func (u UploadSpec) validate() error {
	if u.Name == "" || u.Name != filepath.Base(filepath.FromSlash(u.Name)) || u.Name == "." || u.Name == ".." || strings.HasPrefix(u.Name, partialPrefix) {
		return errors.New("无效文件名")
	}
	if u.Size < 0 {
		return errors.New("无效文件大小")
	}
	return nil
}

func (u UploadSpec) partial() string { return filepath.Join(clean(u.Dir), partialPrefix+u.ID()) }
func (u UploadSpec) target() string  { return filepath.Join(clean(u.Dir), u.Name) }

// OffsetError says a chunk didn't start where the partial file ends; the
// client resumes from Have.
type OffsetError struct{ Have int64 }

func (e *OffsetError) Error() string {
	return fmt.Sprintf("上传位置不一致，服务器已有 %d 字节", e.Have)
}

// ErrUploadGone means the partial file is missing (cancelled or cleaned up).
var ErrUploadGone = errors.New("上传已失效，请重新开始")

// uploadLocks serializes chunks of one upload (FS values are per request).
var uploadLocks sync.Map // root dir + partial path -> *sync.Mutex

func (f *FS) lock(u UploadSpec) func() {
	m, _ := uploadLocks.LoadOrStore(f.root.Name()+"\x00"+u.partial(), &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// BeginUpload prepares an upload and returns how many bytes the server
// already has (0 for a new upload).
func (f *FS) BeginUpload(u UploadSpec) (int64, error) {
	if err := u.validate(); err != nil {
		return 0, err
	}
	dir, err := f.dirOf(u.Dir)
	if err != nil {
		return 0, err
	}
	f.sweepPartials(dir)
	defer f.lock(u)()
	if st, err := f.root.Stat(u.partial()); err == nil {
		if st.Size() <= u.Size {
			return st.Size(), nil
		}
		f.root.Remove(u.partial()) // can't be ours: start over
	}
	out, err := f.root.OpenFile(u.partial(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	return 0, out.Close()
}

// WriteChunk appends r at offset, which must be where the partial file
// ends. It returns the new offset, and done once the whole file has arrived
// and been moved into place (replacing an existing file of that name).
// A connection cut mid-chunk keeps what arrived; the client asks again.
func (f *FS) WriteChunk(u UploadSpec, offset int64, r io.Reader) (int64, bool, error) {
	if err := u.validate(); err != nil {
		return 0, false, err
	}
	defer f.lock(u)()
	st, err := f.root.Stat(u.partial())
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, ErrUploadGone
	}
	if err != nil {
		return 0, false, err
	}
	if st.Size() != offset {
		return st.Size(), false, &OffsetError{Have: st.Size()}
	}
	out, err := f.root.OpenFile(u.partial(), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return offset, false, err
	}
	n, copyErr := io.Copy(out, io.LimitReader(r, u.Size-offset+1))
	cur := offset + n
	if cur > u.Size {
		out.Truncate(offset)
		out.Close()
		return offset, false, errors.New("数据超出了文件大小")
	}
	if err := out.Close(); err != nil {
		return offset, false, err
	}
	if copyErr != nil {
		return cur, false, copyErr
	}
	if cur < u.Size {
		return cur, false, nil
	}
	if err := f.root.Rename(u.partial(), u.target()); err != nil {
		return cur, false, err
	}
	return cur, true, nil
}

// CancelUpload discards a partial upload.
func (f *FS) CancelUpload(u UploadSpec) error {
	if err := u.validate(); err != nil {
		return err
	}
	defer f.lock(u)()
	if err := f.root.Remove(u.partial()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// sweepPartials removes uploads in dir that nobody resumed for a week.
func (f *FS) sweepPartials(dir string) {
	d, err := f.root.Open(dir)
	if err != nil {
		return
	}
	des, _ := d.ReadDir(-1)
	d.Close()
	for _, de := range des {
		if !strings.HasPrefix(de.Name(), partialPrefix) {
			continue
		}
		if info, err := de.Info(); err == nil && time.Since(info.ModTime()) > partialTTL {
			f.root.Remove(filepath.Join(dir, de.Name()))
		}
	}
}
