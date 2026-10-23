// Package httpx holds small helpers shared by every HTTP module.
package httpx

import (
	"encoding/json"
	"net/http"
)

// Module is implemented by every feature package that exposes HTTP routes.
// Adding a feature to the panel = write a Module and register it in main.
type Module interface {
	Register(mux *http.ServeMux)
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
