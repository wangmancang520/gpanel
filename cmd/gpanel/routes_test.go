package main

import (
	"net/http"
	"path/filepath"
	"testing"

	"gpanel/internal/auth"
	"gpanel/internal/certs"
	"gpanel/internal/events"
	"gpanel/internal/files"
	"gpanel/internal/httpx"
	"gpanel/internal/instance"
	"gpanel/internal/runtimes"
	"gpanel/internal/system"
)

// Every route the panel registers must have an explicit rule in
// auth/policy.go; falling back to admin-only is a safety net, not a policy.
func TestEveryRouteHasAnAccessRule(t *testing.T) {
	data := t.TempDir()
	authSvc, err := auth.New(filepath.Join(data, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	instances, err := instance.NewManager(data, events.NewBus())
	if err != nil {
		t.Fatal(err)
	}
	tlsMgr, err := certs.New(filepath.Join(data, "tls"), false)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := certs.NewIssuer(tlsMgr, "")
	if err != nil {
		t.Fatal(err)
	}
	rts := runtimes.NewRegistry(&runtimes.Java{ManagedDir: filepath.Join(data, "java")})
	modules := []httpx.Module{
		authSvc,
		instance.NewHandler(instances),
		files.NewHandler("/api/instances", instances.WorkDir),
		runtimes.NewHandler(rts),
		system.NewMonitor(data, "test", func() map[string]int { return nil }),
		tlsMgr,
		issuer,
	}
	router := auth.NewRouter(http.NewServeMux())
	for _, m := range modules {
		m.Register(router)
	}
	if unruled := router.Unruled(); len(unruled) > 0 {
		t.Fatalf("routes without a rule in auth/policy.go: %v", unruled)
	}
}
