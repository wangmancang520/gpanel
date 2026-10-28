package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManageAndAssignRules(t *testing.T) {
	super := User{Username: "admin", Role: RoleSuper}
	admin := User{Username: "a", Role: RoleAdmin}
	admin2 := User{Username: "a2", Role: RoleAdmin}
	user := User{Username: "u", Role: RoleUser}
	user2 := User{Username: "u2", Role: RoleUser}

	manage := []struct {
		actor, target User
		ok            bool
	}{
		{super, admin, true}, {super, user, true},
		{super, super, false}, // not themselves
		{admin, user, true},
		{admin, admin2, false}, {admin, super, false}, {admin, admin, false},
		{user, user2, false}, {user, admin, false},
	}
	for _, c := range manage {
		if err := canManage(c.actor, c.target); (err == nil) != c.ok {
			t.Errorf("canManage(%s→%s) = %v, want ok=%v", c.actor.Role, c.target.Role, err, c.ok)
		}
	}
	assign := []struct {
		actor User
		role  Role
		ok    bool
	}{
		{super, RoleAdmin, true}, {super, RoleUser, true}, {super, RoleSuper, false},
		{admin, RoleUser, true}, {admin, RoleAdmin, false}, {admin, RoleSuper, false},
		{user, RoleUser, false}, {super, Role("root"), false},
	}
	for _, c := range assign {
		if err := canAssign(c.actor, c.role); (err == nil) != c.ok {
			t.Errorf("canAssign(%s, %s) = %v, want ok=%v", c.actor.Role, c.role, err, c.ok)
		}
	}
}

func TestMigrateLegacyUsers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	legacy := `{
	  "bob":   {"username": "bob",   "passwordHash": "eA==", "createdAt": "2024-01-01T00:00:00Z"},
	  "admin": {"username": "admin", "passwordHash": "eA==", "createdAt": "2025-01-01T00:00:00Z"}
	}`
	os.WriteFile(path, []byte(legacy), 0o600)
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.users.Get("admin")
	b, _ := s.users.Get("bob")
	if a.Role != RoleSuper || b.Role != RoleAdmin {
		t.Fatalf("roles after migration: admin=%s bob=%s", a.Role, b.Role)
	}

	// Without an "admin" account, the oldest one becomes superadmin.
	path2 := filepath.Join(t.TempDir(), "users.json")
	os.WriteFile(path2, []byte(strings.ReplaceAll(legacy, `"admin"`, `"carol"`)), 0o600)
	s2, _ := New(path2)
	if u, _ := s2.users.Get("bob"); u.Role != RoleSuper {
		t.Fatalf("oldest account should be superadmin, got %s", u.Role)
	}
}

// panel builds a Service with one account per role and a router whose test
// routes stand for each rule.
type panel struct {
	s   *Service
	srv *httptest.Server
	pw  map[string]string
}

func newPanel(t *testing.T) *panel {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	p := &panel{s: s, pw: map[string]string{}}
	add := func(name string, role Role, grants map[string]Perms) {
		u := User{Username: name, Role: role, CreatedAt: time.Now(), Instances: grants}
		if err := s.putPassword(&u, name+"-password"); err != nil {
			t.Fatal(err)
		}
		p.pw[name] = name + "-password"
	}
	add("admin", RoleSuper, nil) // replaces the generated one
	add("ann", RoleAdmin, nil)
	add("uma", RoleUser, map[string]Perms{"i1": {Console: true}, "i2": {Files: true}})

	mux := http.NewServeMux()
	rt := NewRouter(mux)
	ok := func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }
	s.Register(rt)
	for _, pat := range []string{
		"GET /api/instances", "GET /api/instances/{id}", "POST /api/instances/{id}/{action}",
		"GET /api/instances/{id}/files", "PUT /api/instances/{id}", "PUT /api/system/tls",
		"GET /api/system/tls", "GET /api/not-in-policy",
	} {
		rt.HandleFunc(pat, ok)
	}
	if got := rt.Unruled(); len(got) != 1 || got[0] != "GET /api/not-in-policy" {
		t.Fatalf("Unruled = %v", got)
	}
	p.srv = httptest.NewServer(s.Middleware(mux))
	t.Cleanup(p.srv.Close)
	return p
}

// client logs in as name and returns a function issuing requests in that session.
func (p *panel) client(t *testing.T, name string) func(method, path, body string) (int, string) {
	t.Helper()
	var cookie string
	do := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, p.srv.URL+path, strings.NewReader(body))
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if c := resp.Header.Get("Set-Cookie"); c != "" {
			cookie = strings.Split(c, ";")[0]
		}
		return resp.StatusCode, string(b)
	}
	if code, body := do("POST", "/api/auth/login", `{"username":"`+name+`","password":"`+p.pw[name]+`"}`); code != 200 {
		t.Fatalf("login %s: %d %s", name, code, body)
	}
	return do
}

func TestPolicyByRole(t *testing.T) {
	p := newPanel(t)
	cases := []struct {
		method, path       string
		super, admin, user int
	}{
		{"GET", "/api/instances", 200, 200, 200},
		{"GET", "/api/instances/i1", 200, 200, 200},
		{"GET", "/api/instances/i3", 200, 200, 403},        // not granted
		{"POST", "/api/instances/i1/start", 200, 200, 200}, // console on i1
		{"POST", "/api/instances/i2/start", 200, 200, 403}, // files only on i2
		{"GET", "/api/instances/i2/files", 200, 200, 200},  // files on i2
		{"GET", "/api/instances/i1/files", 200, 200, 403},  // console only on i1
		{"PUT", "/api/instances/i1", 200, 200, 403},        // settings: admins
		{"GET", "/api/system/tls", 200, 200, 403},          // admins may look
		{"PUT", "/api/system/tls", 200, 403, 403},          // only the superadmin changes it
		{"GET", "/api/not-in-policy", 200, 200, 403},       // default: admin-only
		{"GET", "/api/users", 200, 200, 403},
	}
	for _, who := range []string{"admin", "ann", "uma"} {
		do := p.client(t, who)
		for _, c := range cases {
			want := map[string]int{"admin": c.super, "ann": c.admin, "uma": c.user}[who]
			if code, body := do(c.method, c.path, "{}"); code != want {
				t.Errorf("%s %s %s = %d (%s), want %d", who, c.method, c.path, code, strings.TrimSpace(body), want)
			}
		}
	}
	// No session at all.
	resp, _ := http.Get(p.srv.URL + "/api/instances")
	if resp.StatusCode != 401 {
		t.Errorf("anonymous = %d, want 401", resp.StatusCode)
	}
}

func TestUserManagementFlow(t *testing.T) {
	p := newPanel(t)
	super, ann := p.client(t, "admin"), p.client(t, "ann")

	// An admin creates a plain user (generated password, must change it)...
	code, body := ann("POST", "/api/users", `{"username":"newbie","role":"user","instances":{"i1":{"console":true},"x":{}}}`)
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	var created struct {
		User     PublicUser
		Password string
	}
	json.Unmarshal([]byte(body), &created)
	if created.Password == "" || !created.User.MustChangePassword || len(created.User.Instances) != 1 {
		t.Fatalf("created = %+v (empty grants should be dropped)", created)
	}
	// ...but may not create or manage admins.
	if code, _ := ann("POST", "/api/users", `{"username":"boss","role":"admin"}`); code != 403 {
		t.Errorf("admin created an admin: %d", code)
	}
	if code, _ := ann("PUT", "/api/users/admin", `{"disabled":true}`); code != 403 {
		t.Errorf("admin disabled the superadmin: %d", code)
	}
	if code, _ := super("PUT", "/api/users/admin", `{"role":"user"}`); code != 403 {
		t.Errorf("superadmin demoted themselves: %d", code)
	}

	// The new user must change the password before doing anything else.
	p.pw["newbie"] = created.Password
	nb := p.client(t, "newbie")
	if code, body := nb("GET", "/api/instances", ""); code != 403 || !strings.Contains(body, "mustChangePassword") {
		t.Fatalf("before changing password: %d %s", code, body)
	}
	if code, body := nb("POST", "/api/auth/password", `{"oldPassword":"`+created.Password+`","newPassword":"newbie-password"}`); code != 200 {
		t.Fatalf("change password: %d %s", code, body)
	}
	if code, _ := nb("GET", "/api/instances/i1", ""); code != 200 {
		t.Errorf("after changing password: %d", code)
	}

	// Grants changed by an admin apply to the open session at once.
	ann("PUT", "/api/users/newbie", `{"instances":{}}`)
	if code, _ := nb("GET", "/api/instances/i1", ""); code != 403 {
		t.Errorf("revoked grant still works: %d", code)
	}
	// Disabling ends the session and blocks login.
	ann("PUT", "/api/users/newbie", `{"disabled":true}`)
	if code, _ := nb("GET", "/api/auth/me", ""); code != 401 {
		t.Errorf("disabled user's session still valid: %d", code)
	}
	if _, err := p.s.login("1.2.3.4", "newbie", "newbie-password"); err == nil {
		t.Error("disabled user could log in")
	}

	// The superadmin promotes, an admin can no longer touch the account.
	if code, _ := super("PUT", "/api/users/newbie", `{"role":"admin","disabled":false}`); code != 200 {
		t.Fatal("promote failed")
	}
	if code, _ := ann("DELETE", "/api/users/newbie", ""); code != 403 {
		t.Errorf("admin deleted an admin: %d", code)
	}
	if u, _ := p.s.users.Get("newbie"); u.Instances != nil {
		t.Error("grants kept on an admin account")
	}

	// Reset password: returned once, sessions end, must change again.
	code, body = super("POST", "/api/users/ann/reset-password", "")
	if code != 200 || !strings.Contains(body, `"password"`) {
		t.Fatalf("reset: %d %s", code, body)
	}
	if code, _ := ann("GET", "/api/users", ""); code != 401 {
		t.Errorf("session survived a password reset: %d", code)
	}
	if u, _ := p.s.users.Get("ann"); !u.MustChangePassword {
		t.Error("reset password should have to be changed")
	}

	// Delete.
	if code, _ := super("DELETE", "/api/users/newbie", ""); code != 200 {
		t.Fatal("delete failed")
	}
	if _, ok := p.s.users.Get("newbie"); ok {
		t.Error("user still exists")
	}
}

func TestUserListHidesHashes(t *testing.T) {
	p := newPanel(t)
	_, body := p.client(t, "ann")("GET", "/api/users", "")
	if strings.Contains(body, "passwordHash") || !strings.Contains(body, `"username":"uma"`) {
		t.Fatalf("list = %s", body)
	}
	var list []PublicUser
	json.Unmarshal([]byte(body), &list)
	for _, u := range list {
		if want := u.Role == RoleUser; u.Manageable != want {
			t.Errorf("%s manageable=%v for an admin viewer", u.Username, u.Manageable)
		}
	}
}

func TestForgetInstance(t *testing.T) {
	p := newPanel(t)
	p.s.ForgetInstance("i1")
	u, _ := p.s.users.Get("uma")
	if _, ok := u.Instances["i1"]; ok || len(u.Instances) != 1 {
		t.Fatalf("grants after forgetting i1: %v", u.Instances)
	}
}
