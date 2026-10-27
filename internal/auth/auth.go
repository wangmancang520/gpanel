// Package auth implements panel login with server-side sessions.
//
// Users are stored in a collection so multi-user / per-instance permissions
// can be layered on later; today the first run creates a single "admin".
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
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

type User struct {
	Username     string    `json:"username"`
	PasswordHash []byte    `json:"passwordHash"`
	CreatedAt    time.Time `json:"createdAt"`
}

type session struct {
	username string
	expires  time.Time
}

type ctxKey struct{}

type Service struct {
	users *store.Collection[User]

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
		if err := s.setPassword("admin", pw); err != nil {
			return nil, err
		}
		log.Printf("首次启动：已创建管理员账号 admin，初始密码 %s（请登录后修改）", pw)
	}
	return s, nil
}

func (s *Service) setPassword(username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	u, ok := s.users.Get(username)
	if !ok {
		u = User{Username: username, CreatedAt: time.Now()}
	}
	u.PasswordHash = hash
	return s.users.Put(username, u)
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
	u, _ := ctx.Value(ctxKey{}).(string)
	return u
}

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
		if r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/me" {
			next.ServeHTTP(w, r)
			return
		}
		user, ok := s.userFor(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "未登录")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, user)))
	})
}

func (s *Service) Register(mux *http.ServeMux) {
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
		httpx.JSON(w, http.StatusOK, map[string]string{"username": req.Username})
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
		user, ok := s.userFor(r)
		if !ok {
			httpx.Error(w, http.StatusUnauthorized, "未登录")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"username": user})
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
