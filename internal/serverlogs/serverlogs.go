// Package serverlogs lets the panel browse an instance's log files
// (logs/ and crash-reports/, including rotated .log.gz), search them and
// page through them, for anyone allowed to see that instance's console.
// The live console keeps only recent output; this covers the rest.
package serverlogs

import (
	"bufio"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gpanel/internal/httpx"
)

// Dirs are the directories (relative to the work dir) whose files are logs.
var Dirs = []string{"logs", "crash-reports"}

const (
	defaultLimit = 500
	maxLimit     = 5000
	maxLineLen   = 4096
)

type File struct {
	Path    string    `json:"path"` // e.g. "logs/2026-10-28-3.log.gz"
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

type Line struct {
	N    int    `json:"n"` // 1-based line number in the file
	Text string `json:"text"`
}

type Page struct {
	Lines   []Line `json:"lines"`
	Total   int    `json:"total"`   // lines in the file
	Matched int    `json:"matched"` // lines matching the filter (all of them if none)
	More    bool   `json:"more"`    // earlier matching lines exist
}

// list returns the log files of the instance at root, newest first.
func list(root *os.Root) []File {
	out := []File{}
	for _, dir := range Dirs {
		d, err := root.Open(dir)
		if err != nil {
			continue
		}
		des, _ := d.ReadDir(-1)
		d.Close()
		for _, de := range des {
			if !de.Type().IsRegular() || !isLogName(de.Name()) {
				continue
			}
			if info, err := de.Info(); err == nil {
				out = append(out, File{Path: dir + "/" + de.Name(), Size: info.Size(), ModTime: info.ModTime()})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out
}

func isLogName(name string) bool {
	for _, ext := range []string{".log", ".log.gz", ".txt", ".txt.gz"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// checkPath accepts only "<one of Dirs>/<log file name>".
func checkPath(p string) error {
	dir, name, ok := strings.Cut(p, "/")
	if !ok || !contains(Dirs, dir) || name == "" || strings.ContainsAny(name, `/\`) || name == ".." || !isLogName(name) {
		return errors.New("只能查看 logs/ 和 crash-reports/ 下的日志文件")
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Filter selects lines.
type Filter struct {
	Query string // case-insensitive substring
	Level string // "", "warn" (warnings and errors) or "error"
}

var (
	errorLine = regexp.MustCompile(`(?i)/(ERROR|FATAL)\]|\b(ERROR|FATAL|SEVERE)\b|Exception\b|^\s+at [\w.$]+\(`)
	warnLine  = regexp.MustCompile(`(?i)/WARN\]|\bWARN(ING)?\b`)
)

func (f Filter) match(line string) bool {
	switch f.Level {
	case "error":
		if !errorLine.MatchString(line) {
			return false
		}
	case "warn":
		if !errorLine.MatchString(line) && !warnLine.MatchString(line) {
			return false
		}
	}
	return f.Query == "" || strings.Contains(strings.ToLower(line), strings.ToLower(f.Query))
}

// read returns up to limit matching lines that come before line `before`
// (0: the end of the file), oldest first.
func read(r io.Reader, f Filter, limit, before int) (Page, error) {
	page := Page{Lines: []Line{}}
	ring := make([]Line, 0, limit)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	n := 0
	older := 0 // matches pushed out of the ring
	for sc.Scan() {
		n++
		text := sc.Text()
		if !f.match(text) {
			continue
		}
		page.Matched++
		if before > 0 && n >= before {
			continue
		}
		if len(text) > maxLineLen {
			text = text[:maxLineLen] + " …"
		}
		if len(ring) == limit {
			ring = ring[1:]
			older++
		}
		ring = append(ring, Line{N: n, Text: text})
	}
	if err := sc.Err(); err != nil {
		return page, err
	}
	page.Lines, page.Total, page.More = ring, n, older > 0
	return page, nil
}

// open opens a log file, decompressing .gz.
func open(root *os.Root, p string) (io.ReadCloser, error) {
	f, err := root.Open(p)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(p, ".gz") {
		return f, nil
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, errors.New("压缩日志已损坏")
	}
	return struct {
		io.Reader
		io.Closer
	}{gz, f}, nil
}

// ---- HTTP

type Handler struct {
	workDir func(id string) (string, error)
}

func NewHandler(workDir func(id string) (string, error)) *Handler {
	return &Handler{workDir: workDir}
}

func (h *Handler) Register(mux httpx.Router) {
	mux.HandleFunc("GET /api/instances/{id}/logs", h.with(func(root *os.Root, w http.ResponseWriter, r *http.Request) error {
		httpx.JSON(w, http.StatusOK, list(root))
		return nil
	}))
	mux.HandleFunc("GET /api/instances/{id}/logs/view", h.with(func(root *os.Root, w http.ResponseWriter, r *http.Request) error {
		q := r.URL.Query()
		p := q.Get("file")
		if err := checkPath(p); err != nil {
			return err
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit <= 0 {
			limit = defaultLimit
		}
		limit = min(limit, maxLimit)
		before, _ := strconv.Atoi(q.Get("before"))
		rc, err := open(root, p)
		if err != nil {
			return err
		}
		defer rc.Close()
		page, err := read(rc, Filter{Query: q.Get("q"), Level: q.Get("level")}, limit, before)
		if err != nil {
			return err
		}
		httpx.JSON(w, http.StatusOK, page)
		return nil
	}))
	mux.HandleFunc("GET /api/instances/{id}/logs/download", h.with(func(root *os.Root, w http.ResponseWriter, r *http.Request) error {
		p := r.URL.Query().Get("file")
		if err := checkPath(p); err != nil {
			return err
		}
		f, err := root.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return err
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(p)}))
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, st.Name(), st.ModTime(), f)
		return nil
	}))
}

func (h *Handler) with(fn func(*os.Root, http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dir, err := h.workDir(r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusNotFound, err.Error())
			return
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			httpx.Error(w, http.StatusNotFound, "实例目录不存在")
			return
		}
		defer root.Close()
		if err := fn(root, w, r); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, fs.ErrNotExist) {
				status = http.StatusNotFound
			}
			httpx.Error(w, status, err.Error())
		}
	}
}
