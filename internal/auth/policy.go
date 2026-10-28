package auth

import (
	"context"
	"net/http"
	"slices"
	"sync"

	"gpanel/internal/httpx"
)

// Rule is what a request needs before its handler runs.
type Rule int

const (
	Public          Rule = iota // no login (login, me)
	LoggedIn                    // any account; the handler filters what it returns
	InstanceView                // admin, or any grant on the {id} instance
	InstanceConsole             // admin, or Console on {id}
	InstanceFiles               // admin, or Files on {id}
	Admin                       // admin or superadmin
	Super                       // superadmin only
)

// rules is the whole access policy in one place. A route missing here is
// admin-only (Router.Unruled lists them at startup), so a new endpoint is
// never accidentally open to plain users.
var rules = map[string]Rule{
	"POST /api/auth/login":    Public,
	"GET /api/auth/me":        Public,
	"POST /api/auth/logout":   LoggedIn,
	"POST /api/auth/password": LoggedIn,

	"GET /api/system":    LoggedIn, // instance usage filtered to the caller's
	"GET /api/instances": LoggedIn, // filtered to the caller's instances
	"GET /api/status":    LoggedIn, // players online, filtered likewise

	"GET /api/instances/{id}":               InstanceView,
	"GET /api/instances/{id}/status":        InstanceView,
	"POST /api/instances/{id}/{action}":     InstanceConsole, // start, stop, restart, kill, command
	"GET /api/instances/{id}/terminal":      InstanceConsole,
	"GET /api/instances/{id}/logs":          InstanceConsole, // log files are console output
	"GET /api/instances/{id}/logs/view":     InstanceConsole,
	"GET /api/instances/{id}/logs/download": InstanceConsole,

	"GET /api/instances/{id}/files":                     InstanceFiles,
	"GET /api/instances/{id}/files/content":             InstanceFiles,
	"PUT /api/instances/{id}/files/content":             InstanceFiles,
	"GET /api/instances/{id}/files/download":            InstanceFiles,
	"POST /api/instances/{id}/files/upload":             InstanceFiles,
	"POST /api/instances/{id}/files/uploads":            InstanceFiles, // resumable upload: begin
	"PUT /api/instances/{id}/files/uploads/{upload}":    InstanceFiles, // ...chunk
	"DELETE /api/instances/{id}/files/uploads/{upload}": InstanceFiles, // ...cancel
	"POST /api/instances/{id}/files/mkdir":              InstanceFiles,
	"POST /api/instances/{id}/files/rename":             InstanceFiles,
	"POST /api/instances/{id}/files/move":               InstanceFiles,
	"POST /api/instances/{id}/files/copy":               InstanceFiles,
	"POST /api/instances/{id}/files/delete":             InstanceFiles,
	"POST /api/instances/{id}/files/zip":                InstanceFiles,
	"POST /api/instances/{id}/files/unzip":              InstanceFiles,

	// Creating, configuring and deleting instances, and runtimes: admins.
	"POST /api/instances":            Admin,
	"PUT /api/instances/{id}":        Admin,
	"DELETE /api/instances/{id}":     Admin,
	"GET /api/runtimes":              Admin,
	"POST /api/runtimes/scan":        Admin,
	"DELETE /api/runtimes/{id}":      Admin,
	"GET /api/runtimes/releases":     Admin,
	"POST /api/runtimes/install":     Admin,
	"GET /api/runtimes/jobs":         Admin,
	"DELETE /api/runtimes/jobs/{id}": Admin,

	// Accounts: admins; who may change whom is checked per request.
	"GET /api/users":                        Admin,
	"POST /api/users":                       Admin,
	"PUT /api/users/{name}":                 Admin,
	"POST /api/users/{name}/reset-password": Admin,
	"DELETE /api/users/{name}":              Admin,

	// The panel's own HTTPS: admins may look, only the superadmin changes it.
	"GET /api/system/tls":                 Admin,
	"PUT /api/system/tls":                 Super,
	"POST /api/system/tls/certificate":    Super,
	"POST /api/system/tls/self-signed":    Super,
	"DELETE /api/system/tls/certificate":  Super,
	"GET /api/system/tls/acme":            Admin,
	"PUT /api/system/tls/acme":            Super,
	"POST /api/system/tls/acme/issue":     Super,
	"POST /api/system/tls/acme/continue":  Super,
	"POST /api/system/tls/acme/check-dns": Super,
	"DELETE /api/system/tls/acme/job":     Super,
}

// Principal is the logged-in account as seen by handlers.
type Principal struct {
	Username  string
	Role      Role
	Instances map[string]Perms
}

func (p *Principal) IsAdmin() bool { return p != nil && p.Role.rank() >= RoleAdmin.rank() }
func (p *Principal) IsSuper() bool { return p != nil && p.Role == RoleSuper }

// Grant is what p may do with instance id (everything, for admins).
func (p *Principal) Grant(id string) Perms {
	if p.IsAdmin() {
		return Perms{Console: true, Files: true}
	}
	if p == nil {
		return Perms{}
	}
	return p.Instances[id]
}

// CanSee reports whether p may see instance id at all.
func (p *Principal) CanSee(id string) bool { return p.Grant(id).any() }

type principalKey struct{}

// From returns the Principal that Middleware put on the request context.
func From(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

func (p *Principal) allows(rule Rule, r *http.Request) bool {
	switch rule {
	case Public, LoggedIn:
		return true
	case InstanceView:
		return p.CanSee(r.PathValue("id"))
	case InstanceConsole:
		return p.Grant(r.PathValue("id")).Console
	case InstanceFiles:
		return p.Grant(r.PathValue("id")).Files
	case Admin:
		return p.IsAdmin()
	case Super:
		return p.IsSuper()
	}
	return false
}

// Router registers handlers behind the rule for their pattern.
type Router struct {
	mux *http.ServeMux

	mu      sync.Mutex
	unruled []string
}

func NewRouter(mux *http.ServeMux) *Router { return &Router{mux: mux} }

func (rt *Router) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	rule, ok := rules[pattern]
	if !ok {
		rule = Admin
		rt.mu.Lock()
		rt.unruled = append(rt.unruled, pattern)
		rt.mu.Unlock()
	}
	rt.mux.Handle(pattern, guard(rule, http.HandlerFunc(h)))
}

// Unruled lists registered patterns that fell back to admin-only.
func (rt *Router) Unruled() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return slices.Clone(rt.unruled)
}

func guard(rule Rule, h http.Handler) http.Handler {
	if rule == Public {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := From(r.Context())
		if p == nil {
			httpx.Error(w, http.StatusUnauthorized, "未登录")
			return
		}
		if !p.allows(rule, r) {
			httpx.Error(w, http.StatusForbidden, "没有权限")
			return
		}
		h.ServeHTTP(w, r)
	})
}
