// Package sigv4 signs HTTP requests with AWS Signature Version 4, using the stdlib only.
package sigv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
	"time"
)

type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expires         time.Time
}

// Sign adds X-Amz-Date, the session token if any, and the Authorization header to req.
func Sign(req *http.Request, body []byte, creds Credentials, region, service string, now time.Time) {
	t := now.UTC()
	amzDate := t.Format("20060102T150405Z")
	date := t.Format("20060102")
	payloadHash := hashHex(body)

	req.Header.Set("X-Amz-Date", amzDate)
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}
	if service == "s3" {
		req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	}

	headers, signed := canonicalHeaders(req)
	canonical := strings.Join([]string{
		req.Method,
		canonicalURI(req, service),
		canonicalQuery(req),
		headers,
		signed,
		payloadHash,
	}, "\n")

	scope := date + "/" + region + "/" + service + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hashHex([]byte(canonical))
	signature := hex.EncodeToString(hmacSHA256(SigningKey(creds.SecretAccessKey, date, region, service), toSign))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+creds.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+signature)
}

func SigningKey(secret, date, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	return hmacSHA256(k, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// canonicalURI encodes each path segment; every service but S3 encodes it twice.
func canonicalURI(req *http.Request, service string) string {
	p := req.URL.EscapedPath()
	if p == "" {
		return "/"
	}
	if service == "s3" {
		return p
	}
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = encode(s)
	}
	return strings.Join(segments, "/")
}

func canonicalQuery(req *http.Request) string {
	q := req.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var parts []string
	for _, k := range keys {
		values := slices.Clone(q[k])
		slices.Sort(values)
		for _, v := range values {
			parts = append(parts, encode(k)+"="+encode(v))
		}
	}
	return strings.Join(parts, "&")
}

// canonicalHeaders signs host, content-type and every x-amz-* header.
func canonicalHeaders(req *http.Request) (string, string) {
	values := map[string]string{"host": req.Host}
	if values["host"] == "" {
		values["host"] = req.URL.Host
	}
	for name, v := range req.Header {
		n := strings.ToLower(name)
		if n == "content-type" || strings.HasPrefix(n, "x-amz-") {
			values[n] = strings.Join(strings.Fields(strings.Join(v, ",")), " ")
		}
	}
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n + ":" + values[n] + "\n")
	}
	return b.String(), strings.Join(names, ";")
}

// encode is RFC 3986 percent-encoding: everything but A-Z a-z 0-9 - _ . ~ is escaped.
func encode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}
