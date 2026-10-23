// Package web embeds the frontend so the panel ships as a single binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var static embed.FS

// Handler serves the frontend from dir if set (development), else the embedded copy.
func Handler(dir string) http.Handler {
	var h http.Handler
	if dir != "" {
		h = http.FileServer(http.Dir(dir))
	} else {
		sub, _ := fs.Sub(static, "static")
		h = http.FileServer(http.FS(sub))
	}
	// Embedded files carry no modtime; make browsers revalidate so a panel
	// upgrade never runs against a stale frontend.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}
