package dnsapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---- Aliyun DNS (Alidns RPC API, signature version 1.0 / HMAC-SHA1)

type Aliyun struct {
	KeyID, Secret string
	Endpoint      string // default https://alidns.aliyuncs.com/
	now           func() time.Time
}

func (a *Aliyun) call(ctx context.Context, action string, params map[string]string, out any) error {
	endpoint := a.Endpoint
	if endpoint == "" {
		endpoint = "https://alidns.aliyuncs.com/"
	}
	now := time.Now
	if a.now != nil {
		now = a.now
	}
	q := map[string]string{
		"Action": action, "Format": "JSON", "Version": "2015-01-09", "AccessKeyId": a.KeyID,
		"SignatureMethod": "HMAC-SHA1", "SignatureVersion": "1.0", "SignatureNonce": nonce(),
		"Timestamp": now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	for k, v := range params {
		q[k] = v
	}
	q["Signature"] = aliyunSign(q, a.Secret)
	vals := url.Values{}
	for k, v := range q {
		vals.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+vals.Encode(), nil)
	if err != nil {
		return err
	}
	err = do(req, out, func(b []byte) string {
		var e struct{ Code, Message string }
		if json.Unmarshal(b, &e) == nil && e.Code != "" {
			return "阿里云：" + e.Code + " " + e.Message
		}
		return ""
	})
	return err
}

// aliyunSign implements the RPC signature: sorted, percent-encoded query,
// "GET&%2F&" + encode(query), HMAC-SHA1 with secret + "&", base64.
func aliyunSign(q map[string]string, secret string) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = aliyunEscape(k) + "=" + aliyunEscape(q[k])
	}
	toSign := "GET&%2F&" + aliyunEscape(strings.Join(parts, "&"))
	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(toSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func aliyunEscape(s string) string {
	s = url.QueryEscape(s)
	return strings.NewReplacer("+", "%20", "*", "%2A", "%7E", "~").Replace(s)
}

func (a *Aliyun) zones(ctx context.Context) ([]string, error) {
	var zones []string
	for page := 1; ; page++ {
		var r struct {
			TotalCount int
			Domains    struct{ Domain []struct{ DomainName string } }
		}
		if err := a.call(ctx, "DescribeDomains", map[string]string{"PageNumber": strconv.Itoa(page), "PageSize": "100"}, &r); err != nil {
			return nil, err
		}
		for _, d := range r.Domains.Domain {
			zones = append(zones, d.DomainName)
		}
		if len(r.Domains.Domain) == 0 || len(zones) >= r.TotalCount {
			return zones, nil
		}
	}
}

func (a *Aliyun) AddTXT(ctx context.Context, name, value string) (Record, error) {
	zones, err := a.zones(ctx)
	if err != nil {
		return Record{}, err
	}
	zone, rr, err := zoneFor(name, zones)
	if err != nil {
		return Record{}, err
	}
	var r struct{ RecordId string }
	err = a.call(ctx, "AddDomainRecord", map[string]string{"DomainName": zone, "RR": rr, "Type": "TXT", "Value": value, "TTL": "600"}, &r)
	return Record{Zone: zone, ID: r.RecordId}, err
}

func (a *Aliyun) DeleteTXT(ctx context.Context, r Record) error {
	return a.call(ctx, "DeleteDomainRecord", map[string]string{"RecordId": r.ID}, nil)
}

// ---- Tencent Cloud DNSPod (API 3.0, TC3-HMAC-SHA256)

type DNSPod struct {
	SecretID, SecretKey string
	Endpoint            string // default https://dnspod.tencentcloudapi.com
	now                 func() time.Time
}

func (d *DNSPod) call(ctx context.Context, action string, params any, out any) error {
	endpoint := d.Endpoint
	if endpoint == "" {
		endpoint = "https://dnspod.tencentcloudapi.com"
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	now := time.Now
	if d.now != nil {
		now = d.now
	}
	payload, _ := json.Marshal(params)
	ts := now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", "2021-03-23")
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("Authorization", tc3Auth(d.SecretID, d.SecretKey, u.Host, action, payload, ts))
	var wrap struct {
		Response json.RawMessage
	}
	if err := do(req, &wrap, func(b []byte) string {
		var e struct {
			Response struct {
				Error *struct{ Code, Message string }
			}
		}
		if json.Unmarshal(b, &e) == nil && e.Response.Error != nil {
			return "DNSPod：" + e.Response.Error.Code + " " + e.Response.Error.Message
		}
		return ""
	}); err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(wrap.Response, out)
	}
	return nil
}

// tc3Auth builds the TC3-HMAC-SHA256 Authorization header for service
// "dnspod" (https://cloud.tencent.com/document/api/1427/56189), signing the
// same headers as Tencent's own Go SDK. action is unused by the signature.
func tc3Auth(id, key, host, action string, payload []byte, ts int64) string {
	const service = "dnspod"
	date := time.Unix(ts, 0).UTC().Format("2006-01-02")
	canonical := strings.Join([]string{
		"POST", "/", "",
		"content-type:application/json\nhost:" + host + "\n",
		"content-type;host",
		sha256hex(payload),
	}, "\n")
	scope := date + "/" + service + "/tc3_request"
	toSign := "TC3-HMAC-SHA256\n" + strconv.FormatInt(ts, 10) + "\n" + scope + "\n" + sha256hex([]byte(canonical))
	k := hmacSHA256([]byte("TC3"+key), date)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "tc3_request")
	sig := hex.EncodeToString(hmacSHA256(k, toSign))
	return fmt.Sprintf("TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=content-type;host, Signature=%s", id, scope, sig)
}

func (d *DNSPod) zones(ctx context.Context) ([]string, error) {
	var zones []string
	for offset := 0; ; {
		var r struct {
			DomainCountInfo struct{ AllTotal int }
			DomainList      []struct{ Name string }
		}
		if err := d.call(ctx, "DescribeDomainList", map[string]int{"Offset": offset, "Limit": 3000}, &r); err != nil {
			return nil, err
		}
		for _, x := range r.DomainList {
			zones = append(zones, x.Name)
		}
		offset += len(r.DomainList)
		if len(r.DomainList) == 0 || offset >= r.DomainCountInfo.AllTotal {
			return zones, nil
		}
	}
}

func (d *DNSPod) AddTXT(ctx context.Context, name, value string) (Record, error) {
	zones, err := d.zones(ctx)
	if err != nil {
		return Record{}, err
	}
	zone, rr, err := zoneFor(name, zones)
	if err != nil {
		return Record{}, err
	}
	var r struct{ RecordId int64 }
	err = d.call(ctx, "CreateRecord", map[string]any{
		"Domain": zone, "SubDomain": rr, "RecordType": "TXT", "RecordLine": "默认", "Value": value, "TTL": 600,
	}, &r)
	return Record{Zone: zone, ID: strconv.FormatInt(r.RecordId, 10)}, err
}

func (d *DNSPod) DeleteTXT(ctx context.Context, r Record) error {
	id, err := strconv.ParseInt(r.ID, 10, 64)
	if err != nil {
		return err
	}
	return d.call(ctx, "DeleteRecord", map[string]any{"Domain": r.Zone, "RecordId": id}, nil)
}

// ---- Cloudflare (API v4, bearer token)

type Cloudflare struct {
	Token    string
	Endpoint string // default https://api.cloudflare.com/client/v4
}

func (c *Cloudflare) call(ctx context.Context, method, path string, body, out any) error {
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = "https://api.cloudflare.com/client/v4"
	}
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	var wrap struct{ Result json.RawMessage }
	if err := do(req, &wrap, func(b []byte) string {
		var e struct {
			Success bool
			Errors  []struct {
				Code    int
				Message string
			}
		}
		if json.Unmarshal(b, &e) == nil && !e.Success && len(e.Errors) > 0 {
			return fmt.Sprintf("Cloudflare：%d %s", e.Errors[0].Code, e.Errors[0].Message)
		}
		return ""
	}); err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(wrap.Result, out)
	}
	return nil
}

func (c *Cloudflare) AddTXT(ctx context.Context, name, value string) (Record, error) {
	var zs []struct{ ID, Name string }
	if err := c.call(ctx, http.MethodGet, "/zones?per_page=50", nil, &zs); err != nil {
		return Record{}, err
	}
	names := make([]string, len(zs))
	for i, z := range zs {
		names[i] = z.Name
	}
	zone, _, err := zoneFor(name, names)
	if err != nil {
		return Record{}, err
	}
	var zoneID string
	for _, z := range zs {
		if strings.EqualFold(z.Name, zone) {
			zoneID = z.ID
		}
	}
	var r struct{ ID string }
	err = c.call(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records",
		map[string]any{"type": "TXT", "name": name, "content": value, "ttl": 120}, &r)
	return Record{Zone: zoneID, ID: r.ID}, err
}

func (c *Cloudflare) DeleteTXT(ctx context.Context, r Record) error {
	return c.call(ctx, http.MethodDelete, "/zones/"+r.Zone+"/dns_records/"+r.ID, nil, nil)
}

// ---- helpers

func nonce() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func hmacSHA256(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}
