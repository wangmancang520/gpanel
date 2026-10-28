package auth

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"gpanel/internal/httpx"
)

// Role decides what a panel account may do. Instance access for plain users
// is granted per instance (User.Instances).
//
// This is access control inside the panel, not isolation: every game server
// runs as the same OS user, so anyone who can change an instance's files and
// start it can run code with the panel's privileges. Grant Files only to
// people you would trust with the whole server.
type Role string

const (
	RoleSuper Role = "superadmin" // the first account; manages everyone, owns panel security settings
	RoleAdmin Role = "admin"      // all instances, runtimes; manages plain users
	RoleUser  Role = "user"       // only the instances granted to them
)

func (r Role) valid() bool { return r == RoleSuper || r == RoleAdmin || r == RoleUser }

// rank orders roles for "may act on" comparisons.
func (r Role) rank() int {
	switch r {
	case RoleSuper:
		return 2
	case RoleAdmin:
		return 1
	}
	return 0
}

// Perms are what a plain user may do with one granted instance. Instance
// settings (command, Java, environment, work dir) stay admin-only.
type Perms struct {
	Console bool `json:"console"` // see the console, type into it, start/stop/restart/kill
	Files   bool `json:"files"`   // browse, edit, upload, delete files
}

func (p Perms) any() bool { return p.Console || p.Files }

type User struct {
	Username     string    `json:"username"`
	PasswordHash []byte    `json:"passwordHash"`
	CreatedAt    time.Time `json:"createdAt"`

	Role               Role             `json:"role"`
	Disabled           bool             `json:"disabled,omitempty"`
	MustChangePassword bool             `json:"mustChangePassword,omitempty"` // set when someone else chose the password
	Instances          map[string]Perms `json:"instances,omitempty"`          // grants, by instance ID (plain users)
	LastLogin          *time.Time       `json:"lastLogin,omitempty"`
}

// PublicUser is a User as shown to other admins: no password hash.
type PublicUser struct {
	Username           string           `json:"username"`
	Role               Role             `json:"role"`
	Disabled           bool             `json:"disabled"`
	MustChangePassword bool             `json:"mustChangePassword"`
	Instances          map[string]Perms `json:"instances"`
	CreatedAt          time.Time        `json:"createdAt"`
	LastLogin          *time.Time       `json:"lastLogin,omitempty"`
	Manageable         bool             `json:"manageable"` // the viewer may edit this account
}

func (u User) public(viewer User) PublicUser {
	inst := maps.Clone(u.Instances)
	if inst == nil {
		inst = map[string]Perms{}
	}
	return PublicUser{
		Username: u.Username, Role: u.Role, Disabled: u.Disabled, MustChangePassword: u.MustChangePassword,
		Instances: inst, CreatedAt: u.CreatedAt, LastLogin: u.LastLogin,
		Manageable: canManage(viewer, u) == nil,
	}
}

// forbidden is a refusal for lack of rights (HTTP 403), as opposed to bad input.
type forbidden string

func (f forbidden) Error() string { return string(f) }

const errForbidden = forbidden("没有权限")

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{2,32}$`)

// canManage reports whether actor may change target's role, grants,
// password or status, or delete it. Nobody manages themselves this way (own
// password goes through /api/auth/password); the superadmin manages
// everyone else, an admin manages plain users only.
func canManage(actor, target User) error {
	switch {
	case actor.Username == target.Username:
		return forbidden("不能在这里修改自己的账号")
	case actor.Role.rank() < RoleAdmin.rank():
		return errForbidden
	case target.Role == RoleSuper:
		return forbidden("超级管理员不能被修改")
	case actor.Role == RoleAdmin && target.Role != RoleUser:
		return forbidden("管理员只能管理普通用户")
	}
	return nil
}

// canAssign reports whether actor may give an account role r. The
// superadmin role is never handed out: there is exactly one.
func canAssign(actor User, r Role) error {
	switch {
	case !r.valid():
		return errors.New("未知的角色")
	case r == RoleSuper:
		return forbidden("超级管理员只有一个，不能指定")
	case actor.Role == RoleSuper:
		return nil
	case actor.Role == RoleAdmin && r == RoleUser:
		return nil
	case actor.Role == RoleAdmin:
		return forbidden("只有超级管理员可以任命管理员")
	}
	return errForbidden
}

// migrateUsers brings accounts from versions without roles up to date: one
// superadmin ("admin" if present, else the oldest account), and every other
// role-less account an admin, since they all had full access before.
func (s *Service) migrateUsers() error {
	all := s.users.List()
	if !slices.ContainsFunc(all, func(u User) bool { return u.Role == RoleSuper }) && len(all) > 0 {
		super := all[0]
		for _, u := range all {
			if u.Username == "admin" {
				super = u
				break
			}
			if u.CreatedAt.Before(super.CreatedAt) {
				super = u
			}
		}
		super.Role, super.Disabled = RoleSuper, false
		if err := s.users.Put(super.Username, super); err != nil {
			return err
		}
	}
	for _, u := range s.users.List() {
		if !u.Role.valid() {
			u.Role = RoleAdmin
			if err := s.users.Put(u.Username, u); err != nil {
				return err
			}
		}
	}
	return nil
}

// ForgetInstance drops grants for a deleted instance from every account.
func (s *Service) ForgetInstance(id string) {
	s.userMu.Lock()
	defer s.userMu.Unlock()
	for _, u := range s.users.List() {
		if _, ok := u.Instances[id]; ok {
			delete(u.Instances, id)
			_ = s.users.Put(u.Username, u)
		}
	}
}

// ---- /api/users

type userInput struct {
	Username  string            `json:"username"`
	Password  string            `json:"password"`
	Role      *Role             `json:"role"`
	Disabled  *bool             `json:"disabled"`
	Instances *map[string]Perms `json:"instances"`
}

func (s *Service) registerUsers(mux httpx.Router) {
	mux.HandleFunc("GET /api/users", func(w http.ResponseWriter, r *http.Request) {
		me := s.current(r)
		out := []PublicUser{}
		for _, u := range s.users.List() {
			out = append(out, u.public(me))
		}
		httpx.JSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		var in userInput
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		u, pw, err := s.createUser(s.current(r), in)
		if err != nil {
			httpx.Error(w, statusFor(err), err.Error())
			return
		}
		httpx.JSON(w, http.StatusCreated, map[string]any{"user": u, "password": pw})
	})

	mux.HandleFunc("PUT /api/users/{name}", func(w http.ResponseWriter, r *http.Request) {
		var in userInput
		if err := httpx.Decode(r, &in); err != nil {
			httpx.Error(w, http.StatusBadRequest, "请求格式错误")
			return
		}
		u, err := s.updateUser(s.current(r), r.PathValue("name"), in)
		if err != nil {
			httpx.Error(w, statusFor(err), err.Error())
			return
		}
		httpx.JSON(w, http.StatusOK, u)
	})

	mux.HandleFunc("POST /api/users/{name}/reset-password", func(w http.ResponseWriter, r *http.Request) {
		pw, err := s.adminResetPassword(s.current(r), r.PathValue("name"))
		if err != nil {
			httpx.Error(w, statusFor(err), err.Error())
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"password": pw})
	})

	mux.HandleFunc("DELETE /api/users/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.deleteUser(s.current(r), r.PathValue("name")); err != nil {
			httpx.Error(w, statusFor(err), err.Error())
			return
		}
		httpx.OK(w)
	})
}

var errNoUser = errors.New("用户不存在")

func statusFor(err error) int {
	switch {
	case errors.Is(err, errNoUser):
		return http.StatusNotFound
	case errors.As(err, new(forbidden)):
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}

func cleanGrants(in map[string]Perms) map[string]Perms {
	out := map[string]Perms{}
	for id, p := range in {
		if id != "" && p.any() {
			out[id] = p
		}
	}
	return out
}

func (s *Service) createUser(actor User, in userInput) (PublicUser, string, error) {
	in.Username = strings.TrimSpace(in.Username)
	if !usernamePattern.MatchString(in.Username) {
		return PublicUser{}, "", errors.New("用户名需为 2–32 位字母、数字或 _ . -")
	}
	role := RoleUser
	if in.Role != nil {
		role = *in.Role
	}
	if err := canAssign(actor, role); err != nil {
		return PublicUser{}, "", err
	}
	pw := in.Password
	generated := pw == ""
	if generated {
		pw = randomHex(8)
	} else if len(pw) < 8 {
		return PublicUser{}, "", errors.New("密码至少 8 位")
	}
	s.userMu.Lock()
	defer s.userMu.Unlock()
	if _, ok := s.users.Get(in.Username); ok {
		return PublicUser{}, "", fmt.Errorf("用户 %s 已存在", in.Username)
	}
	u := User{Username: in.Username, Role: role, CreatedAt: time.Now(), MustChangePassword: true}
	if in.Instances != nil && role == RoleUser {
		u.Instances = cleanGrants(*in.Instances)
	}
	if err := s.putPassword(&u, pw); err != nil {
		return PublicUser{}, "", err
	}
	if !generated {
		pw = "" // the admin typed it; don't echo it back
	}
	return u.public(actor), pw, nil
}

func (s *Service) updateUser(actor User, name string, in userInput) (PublicUser, error) {
	s.userMu.Lock()
	defer s.userMu.Unlock()
	u, ok := s.users.Get(name)
	if !ok {
		return PublicUser{}, errNoUser
	}
	if err := canManage(actor, u); err != nil {
		return PublicUser{}, err
	}
	logout := false
	if in.Role != nil && *in.Role != u.Role {
		if err := canAssign(actor, *in.Role); err != nil {
			return PublicUser{}, err
		}
		u.Role = *in.Role
	}
	if in.Disabled != nil && *in.Disabled != u.Disabled {
		u.Disabled = *in.Disabled
		logout = u.Disabled
	}
	if in.Instances != nil {
		u.Instances = cleanGrants(*in.Instances)
	}
	if u.Role != RoleUser {
		u.Instances = nil // admins see everything; stale grants would resurface on demotion
	}
	if err := s.users.Put(u.Username, u); err != nil {
		return PublicUser{}, err
	}
	if logout {
		s.dropSessions(u.Username, "")
	}
	return u.public(actor), nil
}

func (s *Service) adminResetPassword(actor User, name string) (string, error) {
	s.userMu.Lock()
	defer s.userMu.Unlock()
	u, ok := s.users.Get(name)
	if !ok {
		return "", errNoUser
	}
	if err := canManage(actor, u); err != nil {
		return "", err
	}
	pw := randomHex(8)
	u.MustChangePassword = true
	if err := s.putPassword(&u, pw); err != nil {
		return "", err
	}
	s.dropSessions(u.Username, "")
	return pw, nil
}

func (s *Service) deleteUser(actor User, name string) error {
	s.userMu.Lock()
	defer s.userMu.Unlock()
	u, ok := s.users.Get(name)
	if !ok {
		return errNoUser
	}
	if err := canManage(actor, u); err != nil {
		return err
	}
	if err := s.users.Delete(u.Username); err != nil {
		return err
	}
	s.dropSessions(u.Username, "")
	return nil
}
