package certs

import (
	"net/http"

	"gpanel/internal/certs/dnsapi"
	"gpanel/internal/httpx"
)

type acmeState struct {
	Config    PublicConfig  `json:"config"`
	Job       *ACMEJob      `json:"job"`
	Providers []dnsapi.Info `json:"providers"`
	Managed   bool          `json:"managed"` // the installed certificate came from this issuer
}

func (is *Issuer) state() acmeState {
	is.mu.Lock()
	cfg := is.publicLocked()
	is.mu.Unlock()
	st := is.m.Status()
	return acmeState{
		Config: cfg, Job: is.Job(), Providers: dnsapi.Providers,
		Managed: st.Cert != nil && cfg.IssuedFingerprint == st.Cert.Fingerprint,
	}
}

func (is *Issuer) Register(mux httpx.Router) {
	reply := func(w http.ResponseWriter, err error) {
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.JSON(w, http.StatusOK, is.state())
	}
	mux.HandleFunc("GET /api/system/tls/acme", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, is.state())
	})
	mux.HandleFunc("PUT /api/system/tls/acme", func(w http.ResponseWriter, r *http.Request) {
		var cfg ACMEConfig
		if err := httpx.Decode(r, &cfg); err != nil {
			httpx.Error(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		reply(w, is.SaveConfig(cfg))
	})
	mux.HandleFunc("POST /api/system/tls/acme/issue", func(w http.ResponseWriter, r *http.Request) {
		reply(w, is.Start(false))
	})
	mux.HandleFunc("POST /api/system/tls/acme/continue", func(w http.ResponseWriter, r *http.Request) {
		reply(w, is.Continue())
	})
	mux.HandleFunc("POST /api/system/tls/acme/check-dns", func(w http.ResponseWriter, r *http.Request) {
		_, err := is.CheckJobDNS(r.Context())
		reply(w, err)
	})
	mux.HandleFunc("DELETE /api/system/tls/acme/job", func(w http.ResponseWriter, r *http.Request) {
		reply(w, is.Cancel())
	})
}
