// Package httpx holds small helpers shared by every HTTP module.
package httpx

import (
	"encoding/json"
	"net/http"
)

// Router is where modules register routes. In the panel it is auth.Router,
// which puts each route behind its rule in the central access policy.
type Router interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// Module is implemented by every feature package that exposes HTTP routes.
// Adding a feature to the panel = write a Module, register it in main, and
// give its routes a rule in auth/policy.go (otherwise they are admin-only).
type Module interface {
	Register(mux Router)
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

// Decode reads a JSON request body (max 1 MiB) into v.
func Decode(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

func OK(w http.ResponseWriter) {
	JSON(w, http.StatusOK, map[string]bool{"ok": true})
}
