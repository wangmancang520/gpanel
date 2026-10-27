package certs

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"

	"gpanel/internal/certs/dnsapi"
)

// Issuer obtains certificates from an ACME CA (Let's Encrypt by default),
// installs them into the Manager and renews them before they expire.
//
// Validation methods:
//   - http:       HTTP-01; a temporary listener on HTTPPort (80) answers the
//     challenge. The panel's own port answers it too, for reverse proxies.
//   - dns-api:    DNS-01 via a provider API (package dnsapi); wildcards OK.
//   - dns-manual: DNS-01; the job pauses and shows the TXT records to add.
//     Can't renew automatically.
type Issuer struct {
	m        *Manager
	resolver string // optional "host:port" DNS server for propagation checks

	mu     sync.Mutex
	cfg    ACMEConfig
	job    *acmeJob
	tokens map[string]string // HTTP-01 token -> key authorization

	// Test hooks; nil means the real thing.
	httpClient  *http.Client
	lookupTXT   func(ctx context.Context, name string) ([]string, error)
	newProvider func(name string, creds map[string]string) (dnsapi.Provider, error)
}

type ACMEConfig struct {
	Email       string            `json:"email"`
	Directory   string            `json:"directory"` // "letsencrypt", "letsencrypt-staging" or a URL
	Domains     []string          `json:"domains"`
	Method      string            `json:"method"` // "http", "dns-api", "dns-manual"
	Provider    string            `json:"provider"`
	Credentials map[string]string `json:"credentials"`
	HTTPPort    int               `json:"httpPort"`
	AutoRenew   bool              `json:"autoRenew"`
	AutoEnable  bool              `json:"autoEnable"` // turn HTTPS on after issuing

	// Maintained by the issuer.
	AccountDirectory  string    `json:"accountDirectory,omitempty"` // CA the account key is registered with
	AccountURL        string    `json:"accountUrl,omitempty"`       // its account URL there (the JWS key ID)
	IssuedFingerprint string    `json:"issuedFingerprint,omitempty"`
	LastIssued        time.Time `json:"lastIssued,omitzero"`
	LastError         string    `json:"lastError,omitempty"`
	LastAttempt       time.Time `json:"lastAttempt,omitzero"`
}

var directories = map[string]string{
	"letsencrypt":         "https://acme-v02.api.letsencrypt.org/directory",
	"letsencrypt-staging": "https://acme-staging-v02.api.letsencrypt.org/directory",
}

const (
	renewBefore   = 30 * 24 * time.Hour
	renewInterval = 12 * time.Hour
	jobTimeout    = 15 * time.Minute
	manualTimeout = 2 * time.Hour // time to add TXT records by hand
	propagation   = 2 * time.Minute
	callTimeout   = 2 * time.Minute // one ACME request, including the client's retries
	validTimeout  = 5 * time.Minute // waiting for the CA to validate a challenge
)

type ACMEStep struct {
	Time  time.Time `json:"time"`
	Text  string    `json:"text"`
	Level string    `json:"level"` // info, ok, warn, error
}

type TXTRecord struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	Found  *bool  `json:"found,omitempty"` // last DNS check
}

type ACMEJob struct {
	ID         string      `json:"id"`
	State      string      `json:"state"` // running, waiting, done, failed, cancelled
	Renewal    bool        `json:"renewal"`
	Steps      []ACMEStep  `json:"steps"`
	Records    []TXTRecord `json:"records,omitempty"`
	Error      string      `json:"error,omitempty"`
	StartedAt  time.Time   `json:"startedAt"`
	FinishedAt *time.Time  `json:"finishedAt,omitempty"`
}

type acmeJob struct {
	ACMEJob // guarded by Issuer.mu
	cancel  context.CancelFunc
	proceed chan struct{}
}

// NewIssuer loads data/tls/acme.json. resolver, if set ("host:port"), is
// queried for TXT propagation instead of public resolvers.
func NewIssuer(m *Manager, resolver string) (*Issuer, error) {
	is := &Issuer{m: m, resolver: resolver, tokens: map[string]string{}}
	b, err := os.ReadFile(m.path("acme.json"))
	if err == nil {
		err = json.Unmarshal(b, &is.cfg)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取证书申请设置失败: %w", err)
	}
	return is, nil
}

// ---- configuration

// PublicConfig is the config as sent to the browser: secrets are never
// returned, only whether each one is set.
type PublicConfig struct {
	ACMEConfig
	CredentialsSet map[string]bool `json:"credentialsSet"`
}

func (is *Issuer) publicLocked() PublicConfig {
	c := is.cfg
	set := map[string]bool{}
	pub := map[string]string{}
	if info, ok := dnsapi.Lookup(c.Provider); ok {
		for _, f := range info.Fields {
			v := c.Credentials[f.Key]
			set[f.Key] = v != ""
			if !f.Secret {
				pub[f.Key] = v
			}
		}
	}
	c.Credentials = pub
	c.Domains = append([]string{}, c.Domains...)
	return PublicConfig{ACMEConfig: c, CredentialsSet: set}
}

// SaveConfig validates and stores the user-editable fields. An empty secret
// keeps the stored one, so the browser never needs to see it.
func (is *Issuer) SaveConfig(in ACMEConfig) error {
	in.Email = strings.TrimSpace(in.Email)
	if in.Email != "" && !strings.Contains(in.Email, "@") {
		return errors.New("邮箱格式不正确")
	}
	if in.Directory == "" {
		in.Directory = "letsencrypt"
	}
	if _, ok := directories[in.Directory]; !ok && !strings.HasPrefix(in.Directory, "https://") && !isLoopbackURL(in.Directory) {
		return errors.New("ACME 服务地址需以 https:// 开头")
	}
	var domains []string
	for _, d := range in.Domains {
		d = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
		if d == "" || slices.Contains(domains, d) {
			continue
		}
		if err := checkDomain(d); err != nil {
			return err
		}
		if strings.HasPrefix(d, "*.") && in.Method == "http" {
			return errors.New("通配符域名只能用 DNS 验证")
		}
		domains = append(domains, d)
	}
	if len(domains) == 0 {
		return errors.New("请至少填写一个域名")
	}
	if len(domains) > 50 {
		return errors.New("一张证书最多 50 个域名")
	}
	in.Domains = domains
	switch in.Method {
	case "http":
		if in.HTTPPort == 0 {
			in.HTTPPort = 80
		}
		if in.HTTPPort < 1 || in.HTTPPort > 65535 {
			return errors.New("端口不正确")
		}
	case "dns-manual":
		in.AutoRenew = false // nobody to add the records
	case "dns-api":
		info, ok := dnsapi.Lookup(in.Provider)
		if !ok {
			return errors.New("请选择 DNS 服务商")
		}
		is.mu.Lock()
		old := is.cfg
		is.mu.Unlock()
		creds := map[string]string{}
		for _, f := range info.Fields {
			v := strings.TrimSpace(in.Credentials[f.Key])
			if v == "" && old.Provider == in.Provider {
				v = old.Credentials[f.Key]
			}
			if v == "" {
				return fmt.Errorf("请填写 %s", f.Label)
			}
			creds[f.Key] = v
		}
		in.Credentials = creds
	default:
		return errors.New("请选择验证方式")
	}
	if in.Method != "dns-api" {
		in.Provider, in.Credentials = "", nil
	}

	is.mu.Lock()
	defer is.mu.Unlock()
	in.AccountDirectory, in.AccountURL, in.IssuedFingerprint, in.LastIssued, in.LastError, in.LastAttempt =
		is.cfg.AccountDirectory, is.cfg.AccountURL, is.cfg.IssuedFingerprint, is.cfg.LastIssued, is.cfg.LastError, is.cfg.LastAttempt
	if err := writeJSON(is.m.path("acme.json"), in); err != nil {
		return err
	}
	is.cfg = in
	return nil
}

// isLoopbackURL allows plain-HTTP ACME directories on this machine (test CAs).
func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	ip := net.ParseIP(h)
	return h == "localhost" || (ip != nil && ip.IsLoopback())
}

func checkDomain(d string) error {
	host := strings.TrimPrefix(d, "*.")
	if net.ParseIP(host) != nil {
		return fmt.Errorf("%s 是 IP 地址；Let's Encrypt 只为域名签发证书", d)
	}
	for _, r := range host {
		if r > 127 {
			return fmt.Errorf("%s 含非 ASCII 字符，中文域名请填写 Punycode 形式（xn--…）", d)
		}
	}
	if !strings.Contains(host, ".") || strings.Contains(host, "*") || strings.ContainsAny(host, " /:@") {
		return fmt.Errorf("域名格式不正确：%s", d)
	}
	return nil
}

func (is *Issuer) persistLocked() {
	if err := writeJSON(is.m.path("acme.json"), is.cfg); err != nil {
		log.Printf("保存证书申请状态失败: %v", err)
	}
}

// ---- jobs

func (is *Issuer) Job() *ACMEJob {
	is.mu.Lock()
	defer is.mu.Unlock()
	if is.job == nil {
		return nil
	}
	j := is.job.ACMEJob
	j.Steps = slices.Clone(j.Steps)
	j.Records = slices.Clone(j.Records)
	return &j
}

func (is *Issuer) Start(renewal bool) error {
	is.mu.Lock()
	defer is.mu.Unlock()
	if is.job != nil && is.job.FinishedAt == nil {
		return errors.New("已有申请正在进行")
	}
	if len(is.cfg.Domains) == 0 {
		return errors.New("请先填写并保存申请设置")
	}
	timeout := jobTimeout
	if is.cfg.Method == "dns-manual" {
		timeout = manualTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	j := &acmeJob{
		ACMEJob: ACMEJob{ID: randID(), State: "running", Renewal: renewal, StartedAt: time.Now(), Steps: []ACMEStep{}},
		cancel:  cancel, proceed: make(chan struct{}),
	}
	is.job = j
	cfg := is.cfg
	cfg.Credentials = maps(cfg.Credentials)
	cfg.Domains = slices.Clone(cfg.Domains)
	go is.run(ctx, j, cfg)
	return nil
}

// Continue resumes a manual DNS job once the records have been added.
func (is *Issuer) Continue() error {
	is.mu.Lock()
	defer is.mu.Unlock()
	if is.job == nil || is.job.State != "waiting" {
		return errors.New("当前没有等待验证的申请")
	}
	is.job.State = "running"
	close(is.job.proceed)
	return nil
}

func (is *Issuer) Cancel() error {
	is.mu.Lock()
	defer is.mu.Unlock()
	if is.job == nil || is.job.FinishedAt != nil {
		return errors.New("当前没有进行中的申请")
	}
	is.job.cancel()
	return nil
}

func (is *Issuer) step(j *acmeJob, level, format string, args ...any) {
	is.mu.Lock()
	j.Steps = append(j.Steps, ACMEStep{Time: time.Now(), Text: fmt.Sprintf(format, args...), Level: level})
	is.mu.Unlock()
}

func (is *Issuer) run(ctx context.Context, j *acmeJob, cfg ACMEConfig) {
	defer j.cancel()
	fp, err := is.issue(ctx, j, cfg)
	now := time.Now()
	is.mu.Lock()
	defer is.mu.Unlock()
	j.FinishedAt = &now
	is.cfg.LastAttempt = now
	switch {
	case err == nil:
		j.State = "done"
		is.cfg.IssuedFingerprint, is.cfg.LastIssued, is.cfg.LastError = fp, now, ""
	case errors.Is(ctx.Err(), context.Canceled):
		j.State, j.Error = "cancelled", "已取消"
	default:
		j.State, j.Error = "failed", describe(err)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			j.Error = "超时：" + j.Error
		}
		is.cfg.LastError = j.Error
		j.Steps = append(j.Steps, ACMEStep{Time: now, Text: j.Error, Level: "error"})
		log.Printf("证书申请失败（%s）: %s", strings.Join(cfg.Domains, ", "), j.Error)
	}
	is.persistLocked()
}

// issue runs one ACME order end to end and returns the new certificate's
// fingerprint.
func (is *Issuer) issue(ctx context.Context, j *acmeJob, cfg ACMEConfig) (string, error) {
	dirURL := directories[cfg.Directory]
	if dirURL == "" {
		dirURL = cfg.Directory
	}
	key, err := is.accountKey()
	if err != nil {
		return "", err
	}
	client := &acme.Client{Key: key, DirectoryURL: dirURL, UserAgent: "gpanel", HTTPClient: is.httpClient}
	// bounded runs one ACME call with its own deadline, so a CA that keeps
	// failing (the client retries some errors) can't stall the job.
	bounded := func(d time.Duration, f func(context.Context) error) error {
		c, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		return f(c)
	}
	is.step(j, "info", "连接证书颁发机构 %s", dirURL)
	if err := bounded(callTimeout, func(c context.Context) error { _, err := client.Discover(c); return err }); err != nil {
		return "", fmt.Errorf("无法连接 ACME 服务：%w", err)
	}
	register := func() error {
		acct := &acme.Account{}
		if cfg.Email != "" {
			acct.Contact = []string{"mailto:" + cfg.Email}
		}
		var got *acme.Account
		err := bounded(callTimeout, func(c context.Context) (err error) { got, err = client.Register(c, acct, acme.AcceptTOS); return err })
		if err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
			return fmt.Errorf("注册 ACME 账户失败：%w", err)
		}
		is.mu.Lock()
		is.cfg.AccountDirectory, is.cfg.AccountURL = dirURL, ""
		if got != nil {
			is.cfg.AccountURL = got.URI
		}
		is.persistLocked()
		is.mu.Unlock()
		is.step(j, "ok", "已注册 ACME 账户")
		return nil
	}
	is.mu.Lock()
	registered := is.cfg.AccountDirectory == dirURL
	if registered && is.cfg.AccountURL != "" {
		client.KID = acme.KeyID(is.cfg.AccountURL) // skips looking the account up by key
	}
	is.mu.Unlock()
	if !registered {
		if err := register(); err != nil {
			return "", err
		}
	}

	var order *acme.Order
	newOrder := func() error {
		return bounded(callTimeout, func(c context.Context) (err error) {
			order, err = client.AuthorizeOrder(c, acme.DomainIDs(cfg.Domains...))
			return err
		})
	}
	err = newOrder()
	var ae *acme.Error
	if registered && errors.As(err, &ae) && strings.HasSuffix(ae.ProblemType, ":accountDoesNotExist") {
		// The CA forgot the account (e.g. a staging reset): register again.
		client.KID = ""
		if err := register(); err != nil {
			return "", err
		}
		err = newOrder()
	}
	if err != nil {
		return "", fmt.Errorf("创建订单失败：%w", err)
	}
	is.step(j, "ok", "已创建订单：%s", strings.Join(cfg.Domains, "、"))

	var provider dnsapi.Provider
	if cfg.Method == "dns-api" {
		newProvider := is.newProvider
		if newProvider == nil {
			newProvider = dnsapi.New
		}
		if provider, err = newProvider(cfg.Provider, cfg.Credentials); err != nil {
			return "", err
		}
	}
	type pending struct {
		authzURL string
		chal     *acme.Challenge
		domain   string
	}
	var todo []pending
	var records []TXTRecord
	var created []dnsapi.Record
	defer func() {
		for _, r := range created {
			cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := provider.DeleteTXT(cctx, r); err != nil {
				is.step(j, "warn", "清理 TXT 记录失败（可在 DNS 控制台手动删除）：%v", err)
			}
			cancel()
		}
	}()

	chalType := "dns-01"
	if cfg.Method == "http" {
		chalType = "http-01"
		stop := is.serveHTTP01(j, cfg.HTTPPort)
		defer stop()
	}
	for _, u := range order.AuthzURLs {
		authz, err := client.GetAuthorization(ctx, u)
		if err != nil {
			return "", err
		}
		domain := authz.Identifier.Value
		if authz.Wildcard {
			domain = "*." + domain
		}
		if authz.Status == acme.StatusValid {
			is.step(j, "ok", "%s 近期已验证过，跳过", domain)
			continue
		}
		var chal *acme.Challenge
		for _, c := range authz.Challenges {
			if c.Type == chalType {
				chal = c
			}
		}
		if chal == nil {
			return "", fmt.Errorf("证书颁发机构不支持对 %s 使用 %s 验证", domain, chalType)
		}
		switch chalType {
		case "http-01":
			resp, err := client.HTTP01ChallengeResponse(chal.Token)
			if err != nil {
				return "", err
			}
			is.mu.Lock()
			is.tokens[chal.Token] = resp
			is.mu.Unlock()
			defer func(tok string) { is.mu.Lock(); delete(is.tokens, tok); is.mu.Unlock() }(chal.Token)
		case "dns-01":
			value, err := client.DNS01ChallengeRecord(chal.Token)
			if err != nil {
				return "", err
			}
			name := "_acme-challenge." + authz.Identifier.Value
			records = append(records, TXTRecord{Domain: domain, Name: name, Value: value})
			if provider != nil {
				r, err := provider.AddTXT(ctx, name, value)
				if err != nil {
					return "", fmt.Errorf("添加 TXT 记录 %s 失败：%w", name, err)
				}
				created = append(created, r)
				is.step(j, "ok", "已通过 %s 添加 TXT 记录 %s", cfg.Provider, name)
			}
		}
		todo = append(todo, pending{u, chal, domain})
	}

	if len(todo) > 0 && chalType == "dns-01" {
		if cfg.Method == "dns-manual" {
			is.mu.Lock()
			j.Records, j.State = records, "waiting"
			is.mu.Unlock()
			is.step(j, "warn", "请到域名的 DNS 控制台添加下列 %d 条 TXT 记录，然后点击「开始验证」", len(records))
			select {
			case <-j.proceed:
			case <-ctx.Done():
				return "", errors.New("等待添加 TXT 记录超时")
			}
		}
		is.waitPropagation(ctx, j, records)
	}

	for _, p := range todo {
		if _, err := client.Accept(ctx, p.chal); err != nil {
			return "", fmt.Errorf("提交 %s 的验证失败：%w", p.domain, err)
		}
		is.step(j, "info", "正在验证 %s…", p.domain)
		if err := bounded(validTimeout, func(c context.Context) error { _, err := client.WaitAuthorization(c, p.authzURL); return err }); err != nil {
			return "", fmt.Errorf("%s 验证失败：%w", p.domain, err)
		}
		is.step(j, "ok", "%s 验证通过", p.domain)
	}

	is.step(j, "info", "正在签发证书…")
	certKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: strings.TrimPrefix(cfg.Domains[0], "*.")}, DNSNames: cfg.Domains,
	}, certKey)
	if err != nil {
		return "", err
	}
	var ready *acme.Order
	if err := bounded(callTimeout, func(c context.Context) (err error) { ready, err = client.WaitOrder(c, order.URI); return err }); err != nil {
		return "", fmt.Errorf("订单未就绪：%w", err)
	}
	var der [][]byte
	if err := bounded(validTimeout, func(c context.Context) (err error) {
		der, _, err = client.CreateOrderCert(c, ready.FinalizeURL, csr, true)
		return err
	}); err != nil {
		return "", fmt.Errorf("签发失败：%w", err)
	}
	var certPEM []byte
	for _, b := range der {
		certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b})...)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(certKey)
	if err != nil {
		return "", err
	}
	if err := is.m.SetCertificate(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return "", fmt.Errorf("安装证书失败：%w", err)
	}
	st := is.m.Status()
	is.step(j, "ok", "证书已签发并安装，有效期至 %s", st.Cert.NotAfter.Local().Format("2006-01-02"))
	if cfg.AutoEnable && !st.Enabled {
		if err := is.m.SetSettings(Settings{Enabled: true}); err == nil {
			is.step(j, "ok", "已启用 HTTPS")
		}
	}
	return st.Cert.Fingerprint, nil
}

// serveHTTP01 listens on port for the CA's validation requests until stop is
// called. Failing to bind is only a warning: a reverse proxy may forward
// /.well-known/acme-challenge/ to the panel's own port, which also answers.
func (is *Issuer) serveHTTP01(j *acmeJob, port int) (stop func()) {
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: is.ChallengeHandler(http.NotFoundHandler()), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		is.step(j, "warn", "无法监听 %d 端口（%v）。如果已有反向代理把 /.well-known/acme-challenge/ 转发到面板，可以忽略；否则验证会失败", port, err)
		return func() {}
	}
	is.step(j, "info", "已在 %d 端口临时开启 HTTP 验证服务", port)
	go srv.Serve(ln)
	return func() { srv.Close() }
}

// ChallengeHandler answers HTTP-01 requests for tokens of the running job.
func (is *Issuer) ChallengeHandler(next http.Handler) http.Handler {
	const prefix = "/.well-known/acme-challenge/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := strings.CutPrefix(r.URL.Path, prefix); ok {
			is.mu.Lock()
			resp, found := is.tokens[tok]
			is.mu.Unlock()
			if found {
				w.Header().Set("Content-Type", "text/plain")
				w.Write([]byte(resp))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// waitPropagation polls DNS until every record is visible or the timeout
// passes. The CA uses its own resolvers, so this only lowers the chance of a
// failed validation; it never fails the job.
func (is *Issuer) waitPropagation(ctx context.Context, j *acmeJob, records []TXTRecord) {
	is.step(j, "info", "等待 DNS 记录生效（最多 %d 分钟）…", int(propagation.Minutes()))
	deadline := time.Now().Add(propagation)
	for {
		found := is.CheckDNS(ctx, records)
		missing := 0
		for _, r := range found {
			if !*r.Found {
				missing++
			}
		}
		is.mu.Lock()
		if len(j.Records) > 0 {
			j.Records = found
		}
		is.mu.Unlock()
		if missing == 0 {
			is.step(j, "ok", "DNS 记录已生效")
			return
		}
		if time.Now().After(deadline) {
			is.step(j, "warn", "仍有 %d 条记录在公共 DNS 上查不到，继续尝试验证", missing)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}

// CheckDNS looks the records up and reports which are visible.
func (is *Issuer) CheckDNS(ctx context.Context, records []TXTRecord) []TXTRecord {
	servers := []string{"223.5.5.5:53", "119.29.29.29:53", "1.1.1.1:53", "8.8.8.8:53"}
	if is.resolver != "" {
		servers = []string{is.resolver}
	}
	out := slices.Clone(records)
	for i := range out {
		ok := false
		if is.lookupTXT != nil {
			vals, err := is.lookupTXT(ctx, out[i].Name)
			ok = err == nil && slices.Contains(vals, out[i].Value)
			out[i].Found = &ok
			continue
		}
		for _, s := range servers {
			r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, s)
			}}
			lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			vals, err := r.LookupTXT(lctx, out[i].Name)
			cancel()
			if err == nil && slices.Contains(vals, out[i].Value) {
				ok = true
				break
			}
		}
		out[i].Found = &ok
	}
	return out
}

// CheckJobDNS re-checks the waiting job's records (for the "check" button).
func (is *Issuer) CheckJobDNS(ctx context.Context) ([]TXTRecord, error) {
	is.mu.Lock()
	if is.job == nil || len(is.job.Records) == 0 {
		is.mu.Unlock()
		return nil, errors.New("当前申请没有需要检查的 TXT 记录")
	}
	recs := slices.Clone(is.job.Records)
	is.mu.Unlock()
	found := is.CheckDNS(ctx, recs)
	is.mu.Lock()
	if is.job != nil && is.job.State == "waiting" {
		is.job.Records = found
	}
	is.mu.Unlock()
	return found, nil
}

// ---- renewal

// RenewLoop renews the ACME certificate when it is within renewBefore of
// expiring, if auto-renew is on and the installed certificate is still the
// one the issuer obtained (an uploaded certificate is left alone).
func (is *Issuer) RenewLoop(ctx context.Context) {
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if is.dueForRenewal(time.Now()) {
			log.Printf("证书即将到期，开始自动续期")
			if err := is.Start(true); err != nil {
				log.Printf("自动续期未开始: %v", err)
			}
		}
		t.Reset(renewInterval)
	}
}

func (is *Issuer) dueForRenewal(now time.Time) bool {
	is.mu.Lock()
	cfg := is.cfg
	is.mu.Unlock()
	if !cfg.AutoRenew || cfg.Method == "dns-manual" || cfg.IssuedFingerprint == "" {
		return false
	}
	// Don't hammer the CA after a failure; retry on a later tick.
	if cfg.LastError != "" && now.Sub(cfg.LastAttempt) < 6*time.Hour {
		return false
	}
	st := is.m.Status()
	return st.Cert != nil && st.Cert.Fingerprint == cfg.IssuedFingerprint && st.Cert.NotAfter.Sub(now) < renewBefore
}

// ---- helpers

func (is *Issuer) accountKey() (crypto.Signer, error) {
	path := is.m.path("acme-account.pem")
	if b, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, errors.New("ACME 账户密钥损坏")
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		if s, ok := k.(crypto.Signer); ok {
			return s, nil
		}
		return nil, errors.New("ACME 账户密钥类型不支持")
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(is.m.dir, 0o700); err != nil {
		return nil, err
	}
	return k, writeFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// describe turns ACME problem documents into readable messages, keeping any
// context prefix that wrapped them ("example.com 验证失败：…").
func describe(err error) string {
	var aze *acme.AuthorizationError
	var ae *acme.Error
	var inner error
	var msg string
	switch {
	case errors.As(err, &aze) && len(aze.Errors) > 0:
		var parts []string
		for _, e := range aze.Errors {
			parts = append(parts, describe(e))
		}
		inner, msg = aze, strings.Join(parts, "；")
	case errors.As(err, &ae):
		kind := ae.ProblemType[strings.LastIndex(ae.ProblemType, ":")+1:]
		msg = ae.Detail
		if kind != "" {
			msg += "（" + kind + "）"
		}
		if hint := problemHints[kind]; hint != "" {
			msg += "。" + hint
		}
		inner = ae
	default:
		return err.Error()
	}
	return strings.TrimSuffix(err.Error(), inner.Error()) + msg
}

var problemHints = map[string]string{
	"rateLimited":        "申请过于频繁，Let's Encrypt 有频率限制，调试时请改用「测试环境」",
	"dns":                "域名解析失败，请检查 DNS 记录",
	"connection":         "证书颁发机构连不上你的服务器，请检查 80 端口是否对公网开放、域名是否解析到本机",
	"unauthorized":       "验证内容不匹配，请检查域名是否解析到本机，或 TXT 记录是否正确",
	"caa":                "域名的 CAA 记录不允许该机构签发",
	"rejectedIdentifier": "证书颁发机构拒绝为该域名签发",
}

func randID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func maps(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
