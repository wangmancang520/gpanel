package files

import (
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"gpanel/internal/httpx"
)

// RootFunc maps a scope ID (e.g. an instance ID) to the directory it may access.
type RootFunc func(id string) (string, error)

// Handler serves the file manager under prefix + "/{id}/files...".
// Mounting it for another kind of root (backups, runtimes, ...) is just
// another Handler with a different prefix and RootFunc.
type Handler struct {
	prefix string
	roots  RootFunc
}

func NewHandler(prefix string, roots RootFunc) *Handler {
	return &Handler{prefix: prefix, roots: roots}
}

func (h *Handler) Register(mux httpx.Router) {
	p := h.prefix + "/{id}/files"
	mux.HandleFunc("GET "+p, h.with(h.list))
	mux.HandleFunc("GET "+p+"/content", h.with(h.read))
	mux.HandleFunc("PUT "+p+"/content", h.with(h.write))
	mux.HandleFunc("GET "+p+"/download", h.with(h.download))
	mux.HandleFunc("POST "+p+"/upload", h.with(h.upload))
	mux.HandleFunc("POST "+p+"/mkdir", h.with(h.mkdir))
	mux.HandleFunc("POST "+p+"/rename", h.with(h.rename))
	mux.HandleFunc("POST "+p+"/move", h.with(h.move))
	mux.HandleFunc("POST "+p+"/copy", h.with(h.copy))
	mux.HandleFunc("POST "+p+"/delete", h.with(h.remove))
	mux.HandleFunc("POST "+p+"/zip", h.with(h.zip))
	mux.HandleFunc("POST "+p+"/unzip", h.with(h.unzip))
}

type opFunc func(fsys *FS, w http.ResponseWriter, r *http.Request) error

// with opens the sandbox for the request's scope and maps errors to responses.
func (h *Handler) with(fn opFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dir, err := h.roots(r.PathValue("id"))
		if err != nil {
			httpx.Error(w, http.StatusNotFound, err.Error())
			return
		}
		fsys, err := Open(dir)
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer fsys.Close()
		if err := fn(fsys, w, r); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, fs.ErrNotExist) {
				status = http.StatusNotFound
			}
			httpx.Error(w, status, err.Error())
		}
	}
}

func (h *Handler) list(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	entries, err := fsys.List(r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, entries)
	return nil
}

func (h *Handler) read(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	data, err := fsys.ReadText(r.URL.Query().Get("path"))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, err = w.Write(data)
	return err
}

func (h *Handler) write(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxEditSize))
	if err != nil {
		return err
	}
	if err := fsys.WriteText(r.URL.Query().Get("path"), data); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

// previewTypes may be served inline for the image viewer. SVG and HTML are
// deliberately absent: served from the panel's origin they could run script.
var previewTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".bmp": "image/bmp", ".ico": "image/x-icon",
}

func (h *Handler) download(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	p := r.URL.Query().Get("path")
	file, st, err := fsys.OpenFile(p)
	if err != nil {
		return err
	}
	defer file.Close()
	disposition := "attachment"
	if ct, ok := previewTypes[strings.ToLower(path.Ext(p))]; ok && r.URL.Query().Get("inline") == "1" {
		disposition = "inline"
		w.Header().Set("Content-Type", ct)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": path.Base(p)}))
	http.ServeContent(w, r, st.Name(), st.ModTime(), file)
	return nil
}

// upload streams multipart parts straight to disk, so file size isn't
// bounded by memory. Large uploads may later move to resumable chunks.
func (h *Handler) upload(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	mr, err := r.MultipartReader()
	if err != nil {
		return err
	}
	dir := r.URL.Query().Get("path")
	var saved []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if part.FileName() == "" {
			continue
		}
		if err := fsys.Save(dir, part.FileName(), part); err != nil {
			return err
		}
		saved = append(saved, part.FileName())
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"saved": saved})
	return nil
}

func (h *Handler) mkdir(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	var req struct{ Path string }
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if err := fsys.Mkdir(req.Path); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (h *Handler) rename(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	var req struct{ From, To string }
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if err := fsys.Rename(req.From, req.To); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (h *Handler) move(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Paths []string
		Dest  string
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if err := fsys.Move(req.Paths, req.Dest); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (h *Handler) copy(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Paths []string
		Dest  string
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	created, err := fsys.Copy(req.Paths, req.Dest)
	if err != nil {
		return err
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"created": created})
	return nil
}

func (h *Handler) remove(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	var req struct{ Paths []string }
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	for _, p := range req.Paths {
		if err := fsys.Remove(p); err != nil {
			return err
		}
	}
	httpx.OK(w)
	return nil
}

func (h *Handler) zip(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Paths []string
		Dest  string
	}
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if err := fsys.Zip(req.Paths, req.Dest); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}

func (h *Handler) unzip(fsys *FS, w http.ResponseWriter, r *http.Request) error {
	var req struct{ Path, Dest string }
	if err := httpx.Decode(r, &req); err != nil {
		return err
	}
	if err := fsys.Unzip(req.Path, req.Dest); err != nil {
		return err
	}
	httpx.OK(w)
	return nil
}
