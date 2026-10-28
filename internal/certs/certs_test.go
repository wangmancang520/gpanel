package certs

import (
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
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

// flakyListener fails a few accepts with a non-timeout error (as EMFILE
// would), then hands out conns until closed.
type flakyListener struct {
	net.Listener // nil; only fills out the interface (Addr is never called)
	fails        int
	conns        chan net.Conn
	closeOnce    sync.Once
}

func (f *flakyListener) Accept() (net.Conn, error) {
	if f.fails > 0 {
		f.fails--
		return nil, errors.New("accept: too many open files")
	}
	c, ok := <-f.conns
	if !ok {
		return nil, net.ErrClosed
	}
	return c, nil
}

func (f *flakyListener) Close() error {
	f.closeOnce.Do(func() { close(f.conns) })
	return nil
}

func TestListenerSurvivesTransientAcceptErrors(t *testing.T) {
	m, _ := New(t.TempDir(), false)
	inner := &flakyListener{fails: 5, conns: make(chan net.Conn)}
	l := m.wrap(inner)
	defer l.Close()

	server, client := net.Pipe()
	defer client.Close()
	go func() {
		inner.conns <- server
		client.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	}()

	type result struct {
		c   net.Conn
		err error
	}
	got := make(chan result, 1)
	go func() { c, err := l.Accept(); got <- result{c, err} }()
	select {
	case r := <-got:
		if r.err != nil {
			t.Fatalf("listener gave up after transient errors: %v", r.err)
		}
		r.c.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("no connection accepted after transient errors")
	}

	// Closing still ends Accept with net.ErrClosed.
	l.Close()
	if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close = %v, want net.ErrClosed", err)
	}
}
