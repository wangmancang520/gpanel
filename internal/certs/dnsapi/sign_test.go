package dnsapi

import "testing"

// Example from Alibaba Cloud's Alidns signature documentation.
func TestAliyunSignatureVector(t *testing.T) {
	q := map[string]string{
		"Format": "XML", "AccessKeyId": "testid", "Action": "DescribeDomainRecords",
		"SignatureMethod": "HMAC-SHA1", "DomainName": "example.com",
		"SignatureNonce": "f59ed6a9-83fc-473b-9cc6-99c95df3856e", "SignatureVersion": "1.0",
		"Version": "2015-01-09", "Timestamp": "2016-03-24T16:41:54Z",
	}
	if got, want := aliyunSign(q, "testsecret"), "uRpHwaSEt3J+6KQD//svCh/x+pI="; got != want {
		t.Fatalf("signature = %s, want %s", got, want)
	}
}

// Authorization header produced by Tencent Cloud's official Go SDK
// (tencentcloud-sdk-go v1.3.200) for the same request.
func TestTC3MatchesOfficialSDK(t *testing.T) {
	body := []byte(`{"Domain":"example.com","RecordType":"TXT","RecordLine":"默认","Value":"abc","SubDomain":"_acme-challenge"}`)
	got := tc3Auth("AKIDtest", "secretKEYtest", "127.0.0.1:33611", "CreateRecord", body, 1793071375)
	want := "TC3-HMAC-SHA256 Credential=AKIDtest/2026-10-27/dnspod/tc3_request, SignedHeaders=content-type;host, Signature=38fb4e619db8a0bfe6a9a250f99d8615ac50e2bf300c1323f69b4ecdacb12e4b"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
