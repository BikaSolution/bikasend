package sigv4

import (
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Test vectors from the AWS Signature Version 4 documentation.
func TestSigningKey(t *testing.T) {
	got := hex.EncodeToString(SigningKey("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20120215", "us-east-1", "iam"))
	if want := "f4780e2d9f65fa895f9c67b32ce1baf0b0d8a43505a000a1a9e090d414db404d"; got != want {
		t.Errorf("signing key = %s, want %s", got, want)
	}
}

func TestSign(t *testing.T) {
	creds := Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}
	now := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

	req, err := http.NewRequest(http.MethodGet, "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	Sign(req, nil, creds, "us-east-1", "iam", now)

	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-date, " +
		"Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("authorization =\n%s\nwant\n%s", got, want)
	}
	if req.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Errorf("x-amz-date = %q", req.Header.Get("X-Amz-Date"))
	}

	withToken, _ := http.NewRequest(http.MethodPost, "https://abc.execute-api.eu-west-1.amazonaws.com/prod/items/a b", strings.NewReader("{}"))
	Sign(withToken, []byte("{}"), Credentials{AccessKeyID: "A", SecretAccessKey: "S", SessionToken: "T"}, "eu-west-1", "execute-api", now)
	if withToken.Header.Get("X-Amz-Security-Token") != "T" || !strings.Contains(withToken.Header.Get("Authorization"), "x-amz-security-token") {
		t.Errorf("session token not signed: %v", withToken.Header)
	}
}

func TestCanonicalURI(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/a b/c", nil)
	if got := canonicalURI(req, "execute-api"); got != "/a%2520b/c" {
		t.Errorf("double-encoded uri = %q", got)
	}
	if got := canonicalURI(req, "s3"); got != "/a%20b/c" {
		t.Errorf("s3 uri = %q", got)
	}
}
