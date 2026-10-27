package certs

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/acme"

	"gpanel/internal/certs/dnsapi"
)

// fakeDNS is the TXT view shared by the test CA and the issuer's
// propagation check.
type fakeDNS struct {
	ca  *CAServer
	mu  sync.Mutex
	txt map[string][]string
}

func (d *fakeDNS) add(name, value string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.txt[name] = append(d.txt[name], value)
	d.ca.SetTXT(name, d.txt[name]...)
}

func (d *fakeDNS) remove(name, value string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.txt[name] = slices.DeleteFunc(d.txt[name], func(v string) bool { return v == value })
	d.ca.SetTXT(name, d.txt[name]...)
}

func (d *fakeDNS) lookup(_ context.Context, name string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.txt[name]), nil
}

// fakeProvider stands in for a DNS hosting API.
type fakeProvider struct {
	dns     *fakeDNS
	mu      sync.Mutex
	live    map[string][2]string // id -> name, value
	created int
}

func (p *fakeProvider) AddTXT(_ context.Context, name, value string) (dnsapi.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.created++
	id := fmt.Sprint(p.created)
	p.live[id] = [2]string{name, value}
	p.dns.add(name, value)
	return dnsapi.Record{Zone: "z", ID: id}, nil
}

func (p *fakeProvider) DeleteTXT(_ context.Context, r dnsapi.Record) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, ok := p.live[r.ID]
	if !ok {
		return errors.New("no such record")
	}
	delete(p.live, r.ID)
	p.dns.remove(rec[0], rec[1])
	return nil
}

func newTestIssuer(t *testing.T, challenges ...string) (*Issuer, *CAServer, *fakeDNS) {
	t.Helper()
	ca := NewCAServer(t).ChallengeTypes(challenges...).Start()
	m, err := New(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	is, err := NewIssuer(m, "")
	if err != nil {
		t.Fatal(err)
	}
	dns := &fakeDNS{ca: ca, txt: map[string][]string{}}
	is.httpClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: ca.Roots()}}}
	is.lookupTXT = dns.lookup
	return is, ca, dns
}

func waitJob(t *testing.T, is *Issuer, states ...string) *ACMEJob {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if j := is.Job(); j != nil && slices.Contains(states, j.State) {
			return j
		}
		time.Sleep(20 * time.Millisecond)
	}
	j := is.Job()
	t.Fatalf("job never reached %v: %+v", states, j)
	return nil
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestIssueHTTP01(t *testing.T) {
	is, ca, _ := newTestIssuer(t, "http-01")
	port := freePort(t)
	ca.Resolve("panel.example.com", fmt.Sprintf("127.0.0.1:%d", port))
	err := is.SaveConfig(ACMEConfig{Directory: ca.URL(), Domains: []string{"Panel.Example.com."}, Method: "http", HTTPPort: port, AutoEnable: true, AutoRenew: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := is.Start(false); err != nil {
		t.Fatal(err)
	}
	if err := is.Start(false); err == nil {
		t.Error("a second job started while one was running")
	}
	j := waitJob(t, is, "done", "failed")
	if j.State != "done" {
		t.Fatalf("job failed: %s\n%+v", j.Error, j.Steps)
	}
	st := is.m.Status()
	if !st.Active || st.Cert == nil || !slices.Equal(st.Cert.DNSNames, []string{"panel.example.com"}) || st.Cert.SelfSigned {
		t.Fatalf("certificate not installed/enabled: %+v %+v", st, st.Cert)
	}
	if !is.state().Managed {
		t.Error("issued certificate not marked as managed")
	}
	// Tokens are only served while a job runs.
	is.mu.Lock()
	left := len(is.tokens)
	is.mu.Unlock()
	if left != 0 {
		t.Errorf("%d challenge tokens left behind", left)
	}
}

func TestIssueDNSManualWildcard(t *testing.T) {
	is, ca, dns := newTestIssuer(t, "dns-01")
	err := is.SaveConfig(ACMEConfig{Directory: ca.URL(), Domains: []string{"example.test", "*.example.test"}, Method: "dns-manual", AutoRenew: true})
	if err != nil {
		t.Fatal(err)
	}
	if is.cfg.AutoRenew {
		t.Error("manual DNS can't auto-renew")
	}
	is.Start(false)
	j := waitJob(t, is, "waiting", "failed")
	if j.State != "waiting" || len(j.Records) != 2 {
		t.Fatalf("expected 2 TXT records to add: %+v", j)
	}
	for _, r := range j.Records {
		if r.Name != "_acme-challenge.example.test" || len(r.Value) != 43 {
			t.Fatalf("bad record %+v", r)
		}
	}
	// The check button sees nothing until the records exist.
	if recs, _ := is.CheckJobDNS(context.Background()); *recs[0].Found {
		t.Fatal("record reported before it was added")
	}
	for _, r := range j.Records {
		dns.add(r.Name, r.Value)
	}
	if recs, _ := is.CheckJobDNS(context.Background()); !*recs[0].Found || !*recs[1].Found {
		t.Fatal("records not found after adding")
	}
	if err := is.Continue(); err != nil {
		t.Fatal(err)
	}
	j = waitJob(t, is, "done", "failed")
	if j.State != "done" {
		t.Fatalf("job failed: %s", j.Error)
	}
	names := is.m.Status().Cert.DNSNames
	if !slices.Contains(names, "*.example.test") || !slices.Contains(names, "example.test") {
		t.Fatalf("names = %v", names)
	}
}

func TestIssueDNSAPICleansUp(t *testing.T) {
	is, ca, dns := newTestIssuer(t, "dns-01")
	prov := &fakeProvider{dns: dns, live: map[string][2]string{}}
	var gotCreds map[string]string
	is.newProvider = func(name string, creds map[string]string) (dnsapi.Provider, error) {
		gotCreds = creds
		return prov, nil
	}
	err := is.SaveConfig(ACMEConfig{Directory: ca.URL(), Domains: []string{"a.example.test", "b.example.test"},
		Method: "dns-api", Provider: "cloudflare", Credentials: map[string]string{"apiToken": "secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	is.Start(false)
	j := waitJob(t, is, "done", "failed")
	if j.State != "done" {
		t.Fatalf("job failed: %s", j.Error)
	}
	if gotCreds["apiToken"] != "secret-token" || prov.created != 2 || len(prov.live) != 0 {
		t.Fatalf("creds %v, created %d, left %d", gotCreds, prov.created, len(prov.live))
	}
}

func TestIssueFailureIsReported(t *testing.T) {
	is, ca, _ := newTestIssuer(t, "http-01")
	// No resolution for the domain: the CA can't reach us.
	is.SaveConfig(ACMEConfig{Directory: ca.URL(), Domains: []string{"unreachable.example.com"}, Method: "http", HTTPPort: freePort(t)})
	is.Start(false)
	j := waitJob(t, is, "done", "failed")
	if j.State != "failed" || !strings.Contains(j.Error, "unreachable.example.com") || is.cfg.LastError == "" {
		t.Fatalf("want a reported failure, got %+v", j)
	}
	if is.m.Status().Cert != nil {
		t.Fatal("a certificate was installed despite the failure")
	}
}

func TestSaveConfigValidation(t *testing.T) {
	m, _ := New(t.TempDir(), false)
	is, _ := NewIssuer(m, "")
	bad := map[string]ACMEConfig{
		"wildcard over http": {Domains: []string{"*.a.com"}, Method: "http"},
		"IP address":         {Domains: []string{"1.2.3.4"}, Method: "http"},
		"no dot":             {Domains: []string{"localhost"}, Method: "http"},
		"non-ASCII":          {Domains: []string{"例子.中国"}, Method: "http"},
		"no domains":         {Method: "http"},
		"no method":          {Domains: []string{"a.com"}},
		"missing secret":     {Domains: []string{"a.com"}, Method: "dns-api", Provider: "aliyun", Credentials: map[string]string{"accessKeyId": "id"}},
		"http CA URL":        {Domains: []string{"a.com"}, Method: "http", Directory: "http://ca"},
	}
	for name, c := range bad {
		if err := is.SaveConfig(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	ok := ACMEConfig{Domains: []string{"a.com"}, Method: "dns-api", Provider: "aliyun",
		Credentials: map[string]string{"accessKeyId": "id", "accessKeySecret": "s3cret"}}
	if err := is.SaveConfig(ok); err != nil {
		t.Fatal(err)
	}
	pub := is.state().Config
	if pub.Credentials["accessKeySecret"] != "" || pub.Credentials["accessKeyId"] != "id" || !pub.CredentialsSet["accessKeySecret"] {
		t.Fatalf("public config leaks or loses secrets: %+v", pub)
	}
	// Saving again without the secret keeps the stored one.
	ok.Credentials = map[string]string{"accessKeyId": "id2"}
	if err := is.SaveConfig(ok); err != nil {
		t.Fatal(err)
	}
	if is.cfg.Credentials["accessKeySecret"] != "s3cret" || is.cfg.Credentials["accessKeyId"] != "id2" {
		t.Fatalf("creds = %v", is.cfg.Credentials)
	}
	if again, _ := NewIssuer(m, ""); again.cfg.Credentials["accessKeySecret"] != "s3cret" {
		t.Fatal("config not persisted")
	}
}

func TestRenewalDue(t *testing.T) {
	m, _ := New(t.TempDir(), false)
	is, _ := NewIssuer(m, "")
	m.GenerateSelfSigned([]string{"a.com"}, 10) // expires in 10 days
	fp := m.Status().Cert.Fingerprint
	now := time.Now()
	set := func(c ACMEConfig) { is.mu.Lock(); is.cfg = c; is.mu.Unlock() }

	set(ACMEConfig{Method: "http", AutoRenew: true, IssuedFingerprint: fp})
	if !is.dueForRenewal(now) {
		t.Error("expiring managed certificate not due")
	}
	set(ACMEConfig{Method: "http", AutoRenew: false, IssuedFingerprint: fp})
	if is.dueForRenewal(now) {
		t.Error("due with auto-renew off")
	}
	set(ACMEConfig{Method: "http", AutoRenew: true, IssuedFingerprint: "other"})
	if is.dueForRenewal(now) {
		t.Error("would replace a certificate the user uploaded")
	}
	set(ACMEConfig{Method: "http", AutoRenew: true, IssuedFingerprint: fp, LastError: "x", LastAttempt: now.Add(-time.Hour)})
	if is.dueForRenewal(now) {
		t.Error("retrying too soon after a failure")
	}
	set(ACMEConfig{Method: "http", AutoRenew: true, IssuedFingerprint: fp})
	if is.dueForRenewal(now.Add(-30 * 24 * time.Hour)) {
		t.Error("due 40 days before expiry")
	}
}

func TestDescribeProblems(t *testing.T) {
	err := fmt.Errorf("a.com 验证失败：%w", &acme.Error{StatusCode: 429, ProblemType: "urn:ietf:params:acme:error:rateLimited", Detail: "too many certificates"})
	got := describe(err)
	if !strings.HasPrefix(got, "a.com 验证失败：too many certificates（rateLimited）") || !strings.Contains(got, "测试环境") {
		t.Fatalf("describe = %q", got)
	}
}

// The test CA rejects a second account registration, like a CA would
// reject needless duplicates; renewals must reuse the registered account.
func TestReissueReusesAccount(t *testing.T) {
	is, ca, _ := newTestIssuer(t, "http-01")
	port := freePort(t)
	ca.Resolve("panel.example.com", fmt.Sprintf("127.0.0.1:%d", port))
	is.SaveConfig(ACMEConfig{Directory: ca.URL(), Domains: []string{"panel.example.com"}, Method: "http", HTTPPort: port})
	var fps []string
	for i := range 2 {
		if err := is.Start(i == 1); err != nil {
			t.Fatal(err)
		}
		if j := waitJob(t, is, "done", "failed"); j.State != "done" {
			t.Fatalf("issue %d failed: %s", i+1, j.Error)
		}
		fps = append(fps, is.m.Status().Cert.Fingerprint)
	}
	if fps[0] == fps[1] {
		t.Fatal("second run did not install a new certificate")
	}
	if again, _ := NewIssuer(is.m, ""); again.cfg.AccountDirectory != ca.URL() {
		t.Fatal("account registration not persisted")
	}
}
