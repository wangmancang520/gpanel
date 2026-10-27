package auth

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestResetPasswordWhileRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	s.setPassword("admin", "old-password")
	token, err := s.login("1.2.3.4", "admin", "old-password")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	if _, ok := s.userFor(req); !ok {
		t.Fatal("session not valid before reset")
	}

	time.Sleep(10 * time.Millisecond) // distinct mtime
	pw, err := ResetPassword(path, "admin")
	if err != nil || len(pw) != 16 {
		t.Fatalf("ResetPassword = %q, %v", pw, err)
	}
	// The running service sees the new file without a restart...
	if _, ok := s.userFor(req); ok {
		t.Error("old session survived a password reset")
	}
	if _, err := s.login("1.2.3.4", "admin", "old-password"); err == nil {
		t.Error("old password still accepted")
	}
	if _, err := s.login("1.2.3.4", "admin", pw); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
	// ...and its own later writes don't count as external edits.
	tok2, _ := s.login("1.2.3.4", "admin", pw)
	s.setPassword("admin", "another-one")
	req2 := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req2.AddCookie(&http.Cookie{Name: cookieName, Value: tok2})
	if _, ok := s.userFor(req2); !ok {
		t.Error("own write was treated as an external change")
	}
}

func TestResetPasswordUnknownUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	if _, err := ResetPassword(path, "bob"); err == nil {
		t.Fatal("created an unknown user")
	}
	if _, err := ResetPassword(path, "admin"); err != nil {
		t.Fatalf("admin must always be recoverable: %v", err)
	}
}
