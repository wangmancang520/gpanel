// Package dnsapi creates and deletes the TXT records used for ACME DNS-01
// validation, through DNS hosting providers' APIs. Each provider finds the
// zone that holds a name by listing the account's domains and picking the
// longest matching suffix.
//
// Add a provider: implement Provider, describe its credential fields in
// Providers, and add a case to New.
package dnsapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Provider interface {
	// AddTXT creates a TXT record name=value (name is a full domain name
	// without trailing dot) and returns a handle for DeleteTXT.
	AddTXT(ctx context.Context, name, value string) (Record, error)
	DeleteTXT(ctx context.Context, r Record) error
}

// Record identifies a created record: the provider's zone key and record ID.
type Record struct {
	Zone string `json:"zone"`
	ID   string `json:"id"`
}

type Field struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Secret bool   `json:"secret"`
}

type Info struct {
	Name   string  `json:"name"`
	Label  string  `json:"label"`
	Fields []Field `json:"fields"`
	Help   string  `json:"help"` // where to create a key, with least privilege
}

var Providers = []Info{
	{
		Name: "aliyun", Label: "阿里云 DNS",
		Fields: []Field{{"accessKeyId", "AccessKey ID", false}, {"accessKeySecret", "AccessKey Secret", true}},
		Help:   "在阿里云 RAM 控制台创建子用户和 AccessKey，只授予 AliyunDNSFullAccess 权限。",
	},
	{
		Name: "dnspod", Label: "腾讯云 DNSPod",
		Fields: []Field{{"secretId", "SecretId", false}, {"secretKey", "SecretKey", true}},
		Help:   "在腾讯云访问管理（CAM）创建子用户和 API 密钥，只授予 QcloudDNSPodFullAccess 权限。",
	},
	{
		Name: "cloudflare", Label: "Cloudflare",
		Fields: []Field{{"apiToken", "API Token", true}},
		Help:   "在 Cloudflare「My Profile → API Tokens」用「Edit zone DNS」模板创建 Token，权限 Zone.DNS:Edit。",
	},
}

func Lookup(name string) (Info, bool) {
	for _, p := range Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Info{}, false
}

// New builds a provider from its name and credential fields.
func New(name string, creds map[string]string) (Provider, error) {
	info, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("不支持的 DNS 服务商：%s", name)
	}
	for _, f := range info.Fields {
		if strings.TrimSpace(creds[f.Key]) == "" {
			return nil, fmt.Errorf("请填写 %s 的 %s", info.Label, f.Label)
		}
	}
	c := func(k string) string { return strings.TrimSpace(creds[k]) }
	switch name {
	case "aliyun":
		return &Aliyun{KeyID: c("accessKeyId"), Secret: c("accessKeySecret")}, nil
	case "dnspod":
		return &DNSPod{SecretID: c("secretId"), SecretKey: c("secretKey")}, nil
	case "cloudflare":
		return &Cloudflare{Token: c("apiToken")}, nil
	}
	return nil, fmt.Errorf("不支持的 DNS 服务商：%s", name)
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// zoneFor picks the longest zone that name belongs to and returns the record
// name relative to it ("_acme-challenge.www" in "example.com").
func zoneFor(name string, zones []string) (zone, rr string, err error) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	for _, z := range zones {
		z = strings.ToLower(strings.TrimSuffix(z, "."))
		if (name == z || strings.HasSuffix(name, "."+z)) && len(z) > len(zone) {
			zone = z
		}
	}
	if zone == "" {
		return "", "", fmt.Errorf("账号下没有找到 %s 所属的域名，请确认域名托管在该服务商且密钥有权限", strings.TrimPrefix(name, "_acme-challenge."))
	}
	rr = strings.TrimSuffix(strings.TrimSuffix(name, zone), ".")
	if rr == "" {
		rr = "@"
	}
	return zone, rr, nil
}

// do sends req and decodes a JSON body into out; non-2xx responses are
// returned as errors via errBody, which extracts the provider's message.
func do(req *http.Request, out any, errBody func([]byte) string) error {
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if msg := errBody(body); msg != "" || resp.StatusCode/100 != 2 {
		if msg == "" {
			msg = resp.Status
		}
		return errors.New(msg)
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}
