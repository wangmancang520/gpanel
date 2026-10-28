package runtimes

import (
	"net/http"

	"gpanel/internal/httpx"
)

type Handler struct{ r *Registry }

func NewHandler(r *Registry) *Handler { return &Handler{r: r} }

func (h *Handler) Register(mux httpx.Router) {
	mux.HandleFunc("GET /api/runtimes", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, h.r.List(r.URL.Query().Get("kind")))
	})
	mux.HandleFunc("POST /api/runtimes/scan", func(w http.ResponseWriter, r *http.Request) {
		h.r.Scan(r.Context())
		httpx.JSON(w, http.StatusOK, h.r.List(""))
	})
	mux.HandleFunc("DELETE /api/runtimes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := h.r.Uninstall(r.PathValue("id")); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w)
	})

	// Online installation.
	mux.HandleFunc("GET /api/runtimes/releases", func(w http.ResponseWriter, r *http.Request) {
		rels, err := h.r.Releases(r.Context(), kindParam(r))
		if err != nil {
			httpx.Error(w, http.StatusBadGateway, err.Error())
			return
		}
		httpx.JSON(w, http.StatusOK, rels)
	})
	mux.HandleFunc("POST /api/runtimes/install", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Kind, ReleaseID string }
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		if req.Kind == "" {
			req.Kind = "java"
		}
		job, err := h.r.Install(r.Context(), req.Kind, req.ReleaseID)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.JSON(w, http.StatusAccepted, job)
	})
	mux.HandleFunc("GET /api/runtimes/jobs", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, h.r.Jobs())
	})
	mux.HandleFunc("DELETE /api/runtimes/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := h.r.CancelJob(r.PathValue("id")); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.OK(w)
	})
}

func kindParam(r *http.Request) string {
	if k := r.URL.Query().Get("kind"); k != "" {
		return k
	}
	return "java"
}
