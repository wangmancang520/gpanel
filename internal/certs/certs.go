// Package certs manages the panel's own HTTPS certificate: upload a PEM
// certificate + key, or generate a self-signed one, and switch HTTPS on or
// off at runtime. The listener in listener.go serves HTTP and HTTPS on the
// same port, so enabling HTTPS never needs a restart and plain HTTP stays
// usable until "force HTTPS" is turned on.
package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Settings struct {
	Enabled    bool `json:"enabled"`    // accept HTTPS connections
	ForceHTTPS bool `json:"forceHttps"` // redirect plain HTTP to HTTPS
}

type CertInfo struct {
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	DNSNames    []string  `json:"dnsNames"`
	IPs         []string  `json:"ips"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	SelfSigned  bool      `json:"selfSigned"`
	Fingerprint string    `json:"fingerprint"` // SHA-256 of the leaf
	KeyType     string    `json:"keyType"`
	ChainLength int       `json:"chainLength"`
}

type Status struct {
	Settings
	DisabledByFlag bool      `json:"disabledByFlag"` // -disable-tls was given
	Active         bool      `json:"active"`         // HTTPS is being served now
	Cert           *CertInfo `json:"cert"`
}

type Manager struct {
	dir      string
	forceOff bool

	mu       sync.RWMutex
	settings Settings
	cert     *tls.Certificate
	info     *CertInfo
}

// New loads settings and certificate from dir. forceOff (the -disable-tls
// flag) serves plain HTTP regardless of settings, to recover from a bad
// certificate. A broken certificate on disk is logged, not fatal.
func New(dir string, forceOff bool) (*Manager, error) {
	m := &Manager{dir: dir, forceOff: forceOff}
	if b, err := os.ReadFile(m.path("settings.json")); err == nil {
		if err := json.Unmarshal(b, &m.settings); err != nil {
			return nil, fmt.Errorf("读取 HTTPS 设置失败: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	certPEM, err1 := os.ReadFile(m.path("cert.pem"))
	keyPEM, err2 := os.ReadFile(m.path("key.pem"))
	if err1 == nil && err2 == nil {
		if cert, info, err := parse(certPEM, keyPEM); err != nil {
			log.Printf("HTTPS 证书无效，已忽略: %v", err)
		} else {
			m.cert, m.info = cert, info
		}
	}
	if m.settings.Enabled && m.cert == nil {
		m.settings = Settings{}
	}
	if forceOff && m.settings.Enabled {
		log.Printf("已通过 -disable-tls 临时关闭 HTTPS（设置保留）")
	}
	return m, nil
}

func (m *Manager) path(name string) string { return filepath.Join(m.dir, name) }

func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Status{Settings: m.settings, DisabledByFlag: m.forceOff, Active: m.activeLocked(), Cert: m.info}
}

func (m *Manager) activeLocked() bool { return m.settings.Enabled && m.cert != nil && !m.forceOff }

// Active reports whether HTTPS connections are accepted.
func (m *Manager) Active() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeLocked()
}

func (m *Manager) forcing() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeLocked() && m.settings.ForceHTTPS
}

// TLSConfig always returns the current certificate, so replacing it takes
// effect for new connections immediately.
func (m *Manager) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"}, // the terminal uses WebSockets over HTTP/1.1
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			m.mu.RLock()
			defer m.mu.RUnlock()
			if m.cert == nil {
				return nil, errors.New("no certificate")
			}
			return m.cert, nil
		},
	}
}

func (m *Manager) SetSettings(s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.Enabled && m.cert == nil {
		return errors.New("请先上传或生成证书")
	}
	if !s.Enabled {
		s.ForceHTTPS = false
	}
	if err := writeJSON(m.path("settings.json"), s); err != nil {
		return err
	}
	m.settings = s
	return nil
}

// SetCertificate validates and installs a PEM certificate chain and key.
func (m *Manager) SetCertificate(certPEM, keyPEM []byte) error {
	cert, info, err := parse(certPEM, keyPEM)
	if err != nil {
		return err
	}
	if time.Now().After(info.NotAfter) {
		return fmt.Errorf("证书已于 %s 过期", info.NotAfter.Local().Format("2006-01-02"))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	if err := writeFile(m.path("key.pem"), keyPEM); err != nil {
		return err
	}
	if err := writeFile(m.path("cert.pem"), certPEM); err != nil {
		return err
	}
	m.cert, m.info = cert, info
	return nil
}

// GenerateSelfSigned creates an ECDSA P-256 certificate for hosts (DNS
// names or IP addresses) valid for the given number of days.
func (m *Manager) GenerateSelfSigned(hosts []string, days int) error {
	var clean []string
	for _, h := range hosts {
		if h = strings.TrimSpace(h); h != "" {
			clean = append(clean, h)
		}
	}
	if len(clean) == 0 {
		return errors.New("至少填写一个域名或 IP")
	}
	if days < 1 || days > 3650 {
		return errors.New("有效期需在 1–3650 天之间")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: clean[0], Organization: []string{"GPanel"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Duration(days) * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range clean {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return m.SetCertificate(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
}

// RemoveCertificate deletes the certificate and turns HTTPS off.
func (m *Manager) RemoveCertificate() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := writeJSON(m.path("settings.json"), Settings{}); err != nil {
		return err
	}
	m.settings = Settings{}
	for _, f := range []string{"cert.pem", "key.pem"} {
		if err := os.Remove(m.path(f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	m.cert, m.info = nil, nil
	return nil
}

func parse(certPEM, keyPEM []byte) (*tls.Certificate, *CertInfo, error) {
	if !strings.Contains(string(certPEM), "-----BEGIN CERTIFICATE-----") {
		return nil, nil, errors.New("证书需为 PEM 格式（以 -----BEGIN CERTIFICATE----- 开头）")
	}
	if !strings.Contains(string(keyPEM), "PRIVATE KEY-----") {
		return nil, nil, errors.New("私钥需为 PEM 格式（以 -----BEGIN ... PRIVATE KEY----- 开头），且不能加密")
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("证书与私钥无效或不匹配: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, nil, err
	}
	cert.Leaf = leaf
	sum := sha256.Sum256(leaf.Raw)
	fp := make([]string, len(sum))
	for i, b := range sum {
		fp[i] = fmt.Sprintf("%02X", b)
	}
	subject := leaf.Subject.CommonName
	if subject == "" && len(leaf.DNSNames) > 0 {
		subject = leaf.DNSNames[0] // Let's Encrypt is dropping the CN
	}
	if subject == "" {
		subject = name(leaf.Subject)
	}
	info := &CertInfo{
		Subject:   subject,
		Issuer:    name(leaf.Issuer),
		DNSNames:  leaf.DNSNames,
		NotBefore: leaf.NotBefore,
		NotAfter:  leaf.NotAfter,
		// Not CheckSignatureFrom: that requires the parent to be a CA.
		SelfSigned:  leaf.Subject.String() == leaf.Issuer.String() && leaf.CheckSignature(leaf.SignatureAlgorithm, leaf.RawTBSCertificate, leaf.Signature) == nil,
		Fingerprint: strings.Join(fp, ":"),
		KeyType:     keyType(leaf),
		ChainLength: len(cert.Certificate),
	}
	for _, ip := range leaf.IPAddresses {
		info.IPs = append(info.IPs, ip.String())
	}
	if info.DNSNames == nil {
		info.DNSNames = []string{}
	}
	if info.IPs == nil {
		info.IPs = []string{}
	}
	return &cert, info, nil
}

func name(n pkix.Name) string {
	if n.CommonName != "" {
		return n.CommonName
	}
	return n.String()
}

func keyType(c *x509.Certificate) string {
	switch k := c.PublicKey.(type) {
	case *ecdsa.PublicKey:
		return fmt.Sprintf("ECDSA %s", k.Curve.Params().Name)
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA %d", k.N.BitLen())
	default:
		return c.PublicKeyAlgorithm.String()
	}
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFile(path, b)
}

// writeFile writes atomically with owner-only permissions (keys live here).
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
