package certs

import (
	"crypto/tls"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSelfSignedPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	m, _ := New(dir, false)
	if err := m.SetSettings(Settings{Enabled: true}); err == nil {
		t.Fatal("enabled HTTPS without a certificate")
	}
	if err := m.GenerateSelfSigned([]string{"panel.example", "127.0.0.1", " "}, 30); err != nil {
		t.Fatal(err)
	}
	if err := m.SetSettings(Settings{Enabled: true, ForceHTTPS: true}); err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if !st.Active || st.Cert == nil || !st.Cert.SelfSigned || st.Cert.Subject != "panel.example" ||
		!slices.Equal(st.Cert.DNSNames, []string{"panel.example"}) || !slices.Equal(st.Cert.IPs, []string{"127.0.0.1"}) {
		t.Fatalf("status = %+v / %+v", st, st.Cert)
	}
	if fi, err := os.Stat(filepath.Join(dir, "key.pem")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key.pem: %v %v", fi, err)
	}

	again, err := New(dir, false)
	if err != nil || !again.Active() || again.Status().Cert.Fingerprint != st.Cert.Fingerprint {
		t.Fatalf("not restored after restart: %v", err)
	}
	off, _ := New(dir, true)
	if off.Active() || !off.Status().Enabled || !off.Status().DisabledByFlag {
		t.Fatal("-disable-tls should serve plain HTTP but keep the settings")
	}

	if err := m.RemoveCertificate(); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.Enabled || st.Cert != nil {
		t.Fatalf("after remove: %+v", st)
	}
}

func TestSetCertificateRejectsBadInput(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	ma, _ := New(a, false)
	mb, _ := New(b, false)
	ma.GenerateSelfSigned([]string{"a"}, 1)
	mb.GenerateSelfSigned([]string{"b"}, 1)
	certA, _ := os.ReadFile(filepath.Join(a, "cert.pem"))
	keyB, _ := os.ReadFile(filepath.Join(b, "key.pem"))
	cases := map[string][2][]byte{
		"mismatched key": {certA, keyB},
		"not PEM":        {[]byte("hello"), keyB},
		"missing key":    {certA, nil},
	}
	for name, c := range cases {
		if err := ma.SetCertificate(c[0], c[1]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if ma.Status().Cert.Subject != "a" {
		t.Fatal("a rejected certificate replaced the current one")
	}
}

// One port serves plain HTTP and HTTPS; HTTPS only while enabled, and plain
// HTTP redirects only while forced.
func TestListenerServesBothProtocols(t *testing.T) {
	m, _ := New(t.TempDir(), false)
	ln, err := m.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: m.Redirect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			io.WriteString(w, "https")
		} else {
			io.WriteString(w, "http")
		}
	}))}
	go srv.Serve(ln)
	defer srv.Close()
	addr := ln.Addr().String()

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	insecure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	get := func(c *http.Client, url string) (int, string) {
		t.Helper()
		resp, err := c.Get(url)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b) + resp.Header.Get("Location")
	}

	if _, body := get(noFollow, "http://"+addr+"/x"); body != "http" {
		t.Fatalf("plain HTTP: %q", body)
	}
	if code, _ := get(insecure, "https://"+addr+"/x"); code != 0 {
		t.Fatal("HTTPS served while disabled")
	}

	m.GenerateSelfSigned([]string{"127.0.0.1"}, 1)
	m.SetSettings(Settings{Enabled: true})
	if _, body := get(insecure, "https://"+addr+"/x"); body != "https" {
		t.Fatalf("HTTPS after enabling: %q", body)
	}
	if _, body := get(noFollow, "http://"+addr+"/x"); body != "http" {
		t.Fatalf("plain HTTP should still work: %q", body)
	}

	m.SetSettings(Settings{Enabled: true, ForceHTTPS: true})
	code, body := get(noFollow, "http://"+addr+"/x?y=1")
	if code != http.StatusTemporaryRedirect || !strings.HasSuffix(body, "https://"+addr+"/x?y=1") {
		t.Fatalf("forced redirect: %d %q", code, body)
	}
}
