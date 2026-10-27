package dnsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestZoneFor(t *testing.T) {
	zones := []string{"example.com", "sub.example.com", "other.net."}
	cases := map[string][2]string{
		"_acme-challenge.example.com":       {"example.com", "_acme-challenge"},
		"_acme-challenge.www.example.com":   {"example.com", "_acme-challenge.www"},
		"_acme-challenge.a.sub.example.com": {"sub.example.com", "_acme-challenge.a"},
		"_acme-challenge.Other.NET.":        {"other.net", "_acme-challenge"},
	}
	for name, want := range cases {
		z, rr, err := zoneFor(name, zones)
		if err != nil || z != want[0] || rr != want[1] {
			t.Errorf("zoneFor(%s) = %s, %s, %v; want %v", name, z, rr, err, want)
		}
	}
	if _, _, err := zoneFor("_acme-challenge.notexample.com", zones); err == nil {
		t.Error("matched a zone that is only a string suffix")
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	if _, err := New("aliyun", map[string]string{"accessKeyId": "x"}); err == nil {
		t.Error("missing secret accepted")
	}
	if _, err := New("nope", nil); err == nil {
		t.Error("unknown provider accepted")
	}
	if _, err := New("cloudflare", map[string]string{"apiToken": " t "}); err != nil {
		t.Error(err)
	}
}

func TestAliyunFlow(t *testing.T) {
	var actions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := map[string]string{}
		for k, v := range r.URL.Query() {
			q[k] = v[0]
		}
		sig := q["Signature"]
		delete(q, "Signature")
		if sig != aliyunSign(q, "sec") || q["AccessKeyId"] != "id" {
			w.WriteHeader(400)
			io.WriteString(w, `{"Code":"SignatureDoesNotMatch","Message":"bad"}`)
			return
		}
		actions = append(actions, q["Action"])
		switch q["Action"] {
		case "DescribeDomains":
			io.WriteString(w, `{"TotalCount":2,"Domains":{"Domain":[{"DomainName":"example.com"},{"DomainName":"foo.cn"}]}}`)
		case "AddDomainRecord":
			if q["DomainName"] != "example.com" || q["RR"] != "_acme-challenge.www" || q["Type"] != "TXT" || q["Value"] != "v" {
				t.Errorf("AddDomainRecord params %v", q)
			}
			io.WriteString(w, `{"RecordId":"42"}`)
		case "DeleteDomainRecord":
			if q["RecordId"] != "42" {
				t.Errorf("delete %v", q)
			}
			io.WriteString(w, `{}`)
		}
	}))
	defer srv.Close()
	a := &Aliyun{KeyID: "id", Secret: "sec", Endpoint: srv.URL + "/"}
	r, err := a.AddTXT(context.Background(), "_acme-challenge.www.example.com", "v")
	if err != nil || r.ID != "42" {
		t.Fatalf("AddTXT = %+v, %v", r, err)
	}
	if err := a.DeleteTXT(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if strings.Join(actions, ",") != "DescribeDomains,AddDomainRecord,DeleteDomainRecord" {
		t.Fatalf("actions %v", actions)
	}
	bad := &Aliyun{KeyID: "id", Secret: "wrong", Endpoint: srv.URL + "/"}
	if _, err := bad.AddTXT(context.Background(), "_acme-challenge.example.com", "v"); err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("want provider error, got %v", err)
	}
}

func TestDNSPodFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "TC3-HMAC-SHA256 Credential=sid/") || r.Header.Get("X-TC-Version") != "2021-03-23" {
			t.Errorf("headers %v", r.Header)
		}
		var p map[string]any
		json.Unmarshal(body, &p)
		switch r.Header.Get("X-TC-Action") {
		case "DescribeDomainList":
			io.WriteString(w, `{"Response":{"DomainCountInfo":{"AllTotal":1},"DomainList":[{"Name":"example.cn"}]}}`)
		case "CreateRecord":
			if p["Domain"] != "example.cn" || p["SubDomain"] != "_acme-challenge" || p["RecordLine"] != "默认" {
				t.Errorf("CreateRecord %v", p)
			}
			io.WriteString(w, `{"Response":{"RecordId":7}}`)
		case "DeleteRecord":
			if p["RecordId"] != float64(7) {
				t.Errorf("DeleteRecord %v", p)
			}
			io.WriteString(w, `{"Response":{"Error":{"Code":"InvalidParameter","Message":"oops"}}}`)
		}
	}))
	defer srv.Close()
	d := &DNSPod{SecretID: "sid", SecretKey: "skey", Endpoint: srv.URL}
	r, err := d.AddTXT(context.Background(), "_acme-challenge.example.cn", "v")
	if err != nil || r.ID != "7" || r.Zone != "example.cn" {
		t.Fatalf("AddTXT = %+v, %v", r, err)
	}
	// Errors are reported inside a 200 response.
	if err := d.DeleteTXT(context.Background(), r); err == nil || !strings.Contains(err.Error(), "InvalidParameter") {
		t.Fatalf("want provider error, got %v", err)
	}
}

func TestCloudflareFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(403)
			io.WriteString(w, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			io.WriteString(w, `{"success":true,"result":[{"id":"z1","name":"example.org"}]}`)
		case r.Method == "POST" && r.URL.Path == "/zones/z1/dns_records":
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			if p["type"] != "TXT" || p["name"] != "_acme-challenge.example.org" || p["content"] != "v" {
				t.Errorf("create %v", p)
			}
			io.WriteString(w, `{"success":true,"result":{"id":"r9"}}`)
		case r.Method == "DELETE" && r.URL.Path == "/zones/z1/dns_records/r9":
			io.WriteString(w, `{"success":true,"result":{"id":"r9"}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	c := &Cloudflare{Token: "tok", Endpoint: srv.URL}
	r, err := c.AddTXT(context.Background(), "_acme-challenge.example.org", "v")
	if err != nil || r != (Record{Zone: "z1", ID: "r9"}) {
		t.Fatalf("AddTXT = %+v, %v", r, err)
	}
	if err := c.DeleteTXT(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	bad := &Cloudflare{Token: "x", Endpoint: srv.URL}
	if _, err := bad.AddTXT(context.Background(), "_acme-challenge.example.org", "v"); err == nil || !strings.Contains(err.Error(), "Authentication error") {
		t.Fatalf("want auth error, got %v", err)
	}
}
