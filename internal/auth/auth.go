// Package auth implements panel accounts: login with server-side sessions,
// roles (superadmin, admin, user), per-instance grants for plain users, and
// the access policy every API route goes through (policy.go). The first run
// creates the superadmin "admin".
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gpanel/internal/httpx"
	"gpanel/internal/store"
)

const (
	cookieName  = "gpanel_session"
	sessionTTL  = 7 * 24 * time.Hour
	maxFailures = 10
	failWindow  = 10 * time.Minute
)

var ErrInvalidLogin = errors.New("用户名或密码错误")

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy"), bcrypt.DefaultCost)

type session struct {
	username string
	expires  time.Time
}

type Service struct {
	users  *store.Collection[User]
	userMu sync.Mutex // serializes read-modify-write of accounts

	mu       sync.Mutex
	sessions map[string]session
	failures map[string][]time.Time // client IP -> recent failed logins
}

func New(usersPath string) (*Service, error) {
	users, err := store.Open[User](usersPath)
	if err != nil {
		return nil, err
	}
	s := &Service{users: users, sessions: map[string]session{}, failures: map[string][]time.Time{}}
	if users.Len() == 0 {
		pw := randomHex(8)
		u := User{Username: "admin", Role: RoleSuper, CreatedAt: time.Now()}
		if err := s.putPassword(&u, pw); err != nil {
			return nil, err
		}
		log.Printf("首次启动：已创建管理员账号 admin，初始密码 %s（请登录后修改）", pw)
	}
	if err := s.migrateUsers(); err != nil {
		return nil, err
	}
	return s, nil
}

// ResetPassword sets a new random password for username in the users file
// at usersPath (creating the account if it is missing) and returns it. Used by
// "gpanel -reset-password"; a running panel notices the change on its next
// request and ends all sessions.
func ResetPassword(usersPath, username string) (string, error) {
	users, err := store.Open[User](usersPath)
	if err != nil {
		return "", err
	}
	s := &Service{users: users}
	u, ok := users.Get(username)
	if !ok {
		// Only the superadmin can be recreated this way, and only if gone.
		if username != "admin" || slices.ContainsFunc(users.List(), func(u User) bool { return u.Role == RoleSuper }) {
			return "", fmt.Errorf("用户 %s 不存在", username)
		}
		u = User{Username: username, Role: RoleSuper, CreatedAt: time.Now()}
	}
	pw := randomHex(8)
	u.MustChangePassword = false // whoever runs this as root is the owner
	return pw, s.putPassword(&u, pw)
}

// refreshUsers reloads the users file if it was edited outside this process
// (a password reset from the command line) and then ends every session, so
// whoever was logged in with the old password is out.
func (s *Service) refreshUsers() {
	changed, err := s.users.Refresh()
	if err != nil {
		log.Printf("重新读取用户文件失败: %v", err)
		return
	}
	if changed {
		s.mu.Lock()
		clear(s.sessions)
		s.mu.Unlock()
		log.Printf("用户文件已在外部修改（如重置密码），已退出所有登录会话")
	}
}

// putPassword hashes password into u and saves u.
func (s *Service) putPassword(u *User, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u.PasswordHash = hash
	return s.users.Put(u.Username, *u)
}

// setPassword changes an existing user's own password.
func (s *Service) setPassword(username, password string) error {
	s.userMu.Lock()
	defer s.userMu.Unlock()
	u, ok := s.users.Get(username)
	if !ok {
		return errNoUser
	}
	u.MustChangePassword = false
	return s.putPassword(&u, password)
}

func (s *Service) checkPassword(username, password string) bool {
	u, ok := s.users.Get(username)
	if !ok {
		// Burn comparable time so usernames can't be probed by timing.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword(u.PasswordHash, []byte(password)) == nil
}

func (s *Service) login(ip, username, password string) (string, error) {
	s.refreshUsers()
	s.mu.Lock()
	recent := s.failures[ip][:0]
	for _, t := range s.failures[ip] {
		if time.Since(t) < failWindow {
			recent = append(recent, t)
		}
	}
	s.failures[ip] = recent
	blocked := len(recent) >= maxFailures
	s.mu.Unlock()
	if blocked {
		return "", errors.New("登录失败次数过多，请稍后再试")
	}

	if !s.checkPassword(username, password) {
		s.mu.Lock()
		s.failures[ip] = append(s.failures[ip], time.Now())
		s.mu.Unlock()
		return "", ErrInvalidLogin
	}
	s.userMu.Lock()
	u, _ := s.users.Get(username)
	if !u.Disabled {
		now := time.Now()
		u.LastLogin = &now
		_ = s.users.Put(u.Username, u)
	}
	s.userMu.Unlock()
	if u.Disabled {
		return "", errors.New("账号已停用，请联系管理员")
	}

	token := randomHex(32)
	s.mu.Lock()
	delete(s.failures, ip)
	s.sessions[token] = session{username: username, expires: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	return token, nil
}

func (s *Service) userFor(r *http.Request) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return "", false
	}
	s.refreshUsers() // one stat per request
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[c.Value]
	if !ok || time.Now().After(sess.expires) {
		delete(s.sessions, c.Value)
		return "", false
	}
	return sess.username, true
}

// dropSessions ends every session of username except keep (the caller's own),
// so a password change locks out anyone holding an old session.
func (s *Service) dropSessions(username, keep string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for token, sess := range s.sessions {
		if sess.username == username && token != keep {
			delete(s.sessions, token)
			n++
		}
	}
	return n
}

// Username returns the logged-in user stored on the request context by Middleware.
func Username(ctx context.Context) string {
	if p := From(ctx); p != nil {
		return p.Username
	}
	return ""
}

// current returns the stored account of the request's principal.
func (s *Service) current(r *http.Request) User {
	u, _ := s.users.Get(Username(r.Context()))
	return u
}

// principal loads the session's account fresh, so role changes, grants and
// disabling apply to sessions that are already open.
func (s *Service) principal(r *http.Request) (*Principal, User, bool) {
	name, ok := s.userFor(r)
	if !ok {
		return nil, User{}, false
	}
	u, ok := s.users.Get(name)
	if !ok || u.Disabled {
		s.dropSessions(name, "")
		return nil, User{}, false
	}
	return &Principal{Username: u.Username, Role: u.Role, Instances: maps.Clone(u.Instances)}, u, true
}

// mustChangeAllowed are the only routes for an account that has to pick a
// new password first.
var mustChangeAllowed = []string{"/api/auth/me", "/api/auth/password", "/api/auth/logout"}

// Middleware requires a valid session for every /api/ route except login/status,
// and rejects cross-origin state-changing requests.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
			httpx.Error(w, http.StatusForbidden, "跨站请求被拒绝")
			return
		}
		if r.URL.Path == "/api/auth/login" {
			next.ServeHTTP(w, r)
			return
		}
		p, u, ok := s.principal(r)
		if !ok {
			if r.URL.Path == "/api/auth/me" {
				next.ServeHTTP(w, r) // answers 401 itself
				return
			}
			httpx.Error(w, http.StatusUnauthorized, "未登录")
			return
		}
		if u.MustChangePassword && !slices.Contains(mustChangeAllowed, r.URL.Path) {
			httpx.JSON(w, http.StatusForbidden, map[string]any{"error": "请先修改密码", "mustChangePassword": true})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

func (s *Service) Register(mux httpx.Router) {
	s.registerUsers(mux)

	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Username, Password string }
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		token, err := s.login(clientIP(r), req.Username, req.Password)
		if err != nil {
			httpx.Error(w, http.StatusUnauthorized, err.Error())
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: token, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
			MaxAge: int(sessionTTL.Seconds()),
		})
		u, _ := s.users.Get(req.Username)
		httpx.JSON(w, http.StatusOK, s.me(u))
	})

	mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(cookieName); err == nil {
			s.mu.Lock()
			delete(s.sessions, c.Value)
			s.mu.Unlock()
		}
		http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1})
		httpx.OK(w)
	})

	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		_, u, ok := s.principal(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "未登录")
			return
		}
		httpx.JSON(w, http.StatusOK, s.me(u))
	})

	mux.HandleFunc("POST /api/auth/password", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ OldPassword, NewPassword string }
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Error(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		user := Username(r.Context())
		if !s.checkPassword(user, req.OldPassword) {
			httpx.Error(w, http.StatusBadRequest, "原密码错误")
			return
		}
		if len(req.NewPassword) < 8 {
			httpx.Error(w, http.StatusBadRequest, "新密码至少 8 位")
			return
		}
		if err := s.setPassword(user, req.NewPassword); err != nil {
			httpx.Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		current := ""
		if c, err := r.Cookie(cookieName); err == nil {
			current = c.Value
		}
		httpx.JSON(w, http.StatusOK, map[string]int{"loggedOut": s.dropSessions(user, current)})
	})
}

// me is what the browser learns about its own account.
func (s *Service) me(u User) map[string]any {
	inst := u.Instances
	if inst == nil {
		inst = map[string]Perms{}
	}
	return map[string]any{
		"username": u.Username, "role": u.Role,
		"mustChangePassword": u.MustChangePassword, "instances": inst,
	}
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients
	}
	u, err := url.Parse(origin)
	return err == nil && subtle.ConstantTimeCompare([]byte(u.Host), []byte(r.Host)) == 1
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
