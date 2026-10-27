package certs

import (
	"net/http"

	"gpanel/internal/httpx"
)

func (m *Manager) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/system/tls", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, m.Status())
	})
	mux.HandleFunc("PUT /api/system/tls", func(w http.ResponseWriter, r *http.Request) {
		var s Settings
		if err := httpx.Decode(r, &s); err != nil {
			httpx.Error(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		m.respond(w, m.SetSettings(s))
	})
	mux.HandleFunc("POST /api/system/tls/certificate", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Cert, Key string }
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		m.respond(w, m.SetCertificate([]byte(req.Cert), []byte(req.Key)))
	})
	mux.HandleFunc("POST /api/system/tls/self-signed", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Hosts []string
			Days  int
		}
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		m.respond(w, m.GenerateSelfSigned(req.Hosts, req.Days))
	})
	mux.HandleFunc("DELETE /api/system/tls/certificate", func(w http.ResponseWriter, r *http.Request) {
		m.respond(w, m.RemoveCertificate())
	})
}

func (m *Manager) respond(w http.ResponseWriter, err error) {
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, m.Status())
}

// Redirect sends plain-HTTP requests to HTTPS while "force HTTPS" is on.
// 307, not 308: browsers cache permanent redirects, which would keep forcing
// HTTPS after the setting is turned off.
func (m *Manager) Redirect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil && m.forcing() {
			http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusTemporaryRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
}
