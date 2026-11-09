package core

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

func TestSend(t *testing.T) {
	var tokenCalls atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Got-Key", r.Header.Get("X-API-Key"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/items/42":
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Echo-Key", r.Header.Get("X-API-Key"))
			_, _ = w.Write([]byte(`{"id": 42, "method": "` + r.Method + `", "query": "` + r.URL.RawQuery + `", "body": ` + quoteJSON(string(body)) + `, "auth": "` + r.Header.Get("Authorization") + `"}`))
		case "/v1/form":
			_ = r.ParseMultipartForm(1 << 20)
			_ = r.ParseForm()
			file := ""
			if f, h, err := r.FormFile("file"); err == nil {
				data, _ := io.ReadAll(f)
				file = h.Filename + ":" + string(data)
			}
			_, _ = w.Write([]byte(r.Header.Get("Content-Type")[:9] + "|" + r.FormValue("note") + "|" + file))
		case "/v1/redirect":
			http.Redirect(w, r, other.URL+"/landing", http.StatusFound)
		case "/v1/items/7":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name": 5}`))
		case "/v1/image":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', 0, 1, 2})
		case "/v1/slow":
			time.Sleep(2 * time.Second)
		case "/token":
			tokenCalls.Add(1)
			user, pass, _ := r.BasicAuth()
			if user != "client" || pass != "k-123456" || r.FormValue("grant_type") != "client_credentials" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"tok-abcdef","expires_in":3600}`))
		}
	}))
	defer srv.Close()

	config := strings.ReplaceAll(testConfig, `"hosts": ["127.0.0.1"],`, `"hosts": ["127.0.0.1"], "oauth2": {"tokenUrl": "`+srv.URL+`/token", "clientId": "client", "clientSecret": "{{api_key}}"}, "sigv4": {"service": "execute-api"},`)
	aws := &fakeAWS{signedIn: true, secrets: map[string]string{"app/keys": `{"api":"k-123456"}`, "app/plain": "p"}, params: map[string]string{"/app/url": "u"}}
	s, root := newTestService(t, srv.URL+"/v1", config, aws)
	if _, err := s.TrustFolder(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectEnvironment("staging"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadSecrets(context.Background()); err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(upload, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.AllowUpload(upload)
	opID := "swagger.json GET /items/{id}"

	tests := []struct {
		name    string
		req     bikasend.Request
		wantErr error
		check   func(t *testing.T, r bikasend.Response)
	}{
		{
			name: "variables in url, path params, query, headers and body; secret redacted; spec checked",
			req: bikasend.Request{
				Method: "POST", URL: "{{baseUrl}}/items/{id}", Operation: opID,
				PathParams: []bikasend.KV{{Name: "id", Value: "{{itemId}}"}},
				Query:      []bikasend.KV{{Name: "tenant", Value: "{{tenant}}", Enabled: true}, {Name: "off", Value: "x"}},
				Headers:    "X-API-Key: {{api_key}}\nX-Empty: \nContent-Type: application/json",
				BodyType:   "json", Body: `{"t":"{{tenant}}"}`,
			},
			check: func(t *testing.T, r bikasend.Response) {
				if !strings.Contains(r.Body, `"query": "tenant=demo"`) || !strings.Contains(r.Body, `\"t\":\"demo\"`) {
					t.Errorf("body = %s", r.Body)
				}
				if !r.Redacted || strings.Contains(r.Headers, "k-123456") || !strings.Contains(r.Headers, "X-Echo-Key: [redacted]") {
					t.Errorf("secret echo not redacted: %s", r.Headers)
				}
				if r.Timing.TotalMs < r.Timing.FirstByteMs || len(r.Issues) != 0 {
					t.Errorf("timing %+v issues %v", r.Timing, r.Issues)
				}
			},
		},
		{
			name: "response that breaks the spec is flagged",
			req:  bikasend.Request{Method: "GET", URL: "{{baseUrl}}/items/7", Operation: opID},
			check: func(t *testing.T, r bikasend.Response) {
				want := []string{`$: missing required field "id"`, "$.name: is integer, want string"}
				if strings.Join(r.Issues, "|") != strings.Join(want, "|") {
					t.Errorf("issues = %q", r.Issues)
				}
			},
		},
		{
			name:    "missing variable blocks the send",
			req:     bikasend.Request{Method: "GET", URL: "{{baseUrl}}/items/{{nope}}"},
			wantErr: bikasend.ErrUndefinedVariable,
		},
		{
			name:    "unfilled path parameter blocks the send",
			req:     bikasend.Request{Method: "GET", URL: "{{baseUrl}}/items/{id}"},
			wantErr: bikasend.ErrUndefinedVariable,
		},
		{
			name:    "secret to a host outside the environment",
			req:     bikasend.Request{Method: "GET", URL: "http://localhost:1/x", Headers: "X-API-Key: {{api_key}}"},
			wantErr: bikasend.ErrSecretHost,
		},
		{
			name: "secret is not forwarded on a cross-host redirect",
			req:  bikasend.Request{Method: "GET", URL: "{{baseUrl}}/redirect", Headers: "X-API-Key: {{api_key}}"},
			check: func(t *testing.T, r bikasend.Response) {
				if r.StatusCode != http.StatusFound {
					t.Errorf("status = %d, want the 302 itself", r.StatusCode)
				}
			},
		},
		{
			name: "form body",
			req:  bikasend.Request{Method: "POST", URL: "{{baseUrl}}/form", BodyType: "form", Form: []bikasend.Field{{Name: "note", Value: "{{tenant}}", Enabled: true}, {Name: "skip", Value: "x"}}},
			check: func(t *testing.T, r bikasend.Response) {
				if r.Body != "applicati|demo|" {
					t.Errorf("form = %q", r.Body)
				}
			},
		},
		{
			name: "multipart with a file",
			req:  bikasend.Request{Method: "POST", URL: "{{baseUrl}}/form", BodyType: "multipart", Form: []bikasend.Field{{Name: "note", Value: "n", Enabled: true}, {Name: "file", Value: upload, File: true, Enabled: true}}},
			check: func(t *testing.T, r bikasend.Response) {
				if r.Body != "multipart|n|note.txt:hello" {
					t.Errorf("multipart = %q", r.Body)
				}
			},
		},
		{
			name:    "a file the user did not pick is refused",
			req:     bikasend.Request{Method: "POST", URL: "{{baseUrl}}/form", BodyType: "multipart", Form: []bikasend.Field{{Name: "file", Value: filepath.Join(root, "swagger.json"), File: true, Enabled: true}}},
			wantErr: bikasend.ErrInvalidRequest,
		},
		{
			name: "binary response comes back as base64",
			req:  bikasend.Request{Method: "GET", URL: "{{baseUrl}}/image"},
			check: func(t *testing.T, r bikasend.Response) {
				if r.Body != "" || r.BodyBase64 != "iVBORwABAg==" || r.ContentType != "image/png" {
					t.Errorf("binary = %+v", r)
				}
			},
		},
		{
			name: "oauth2 client credentials",
			req:  bikasend.Request{Method: "GET", URL: "{{baseUrl}}/items/42", Auth: "oauth2"},
			check: func(t *testing.T, r bikasend.Response) {
				if !strings.Contains(r.Body, `"auth": "Bearer [redacted]"`) {
					t.Errorf("bearer token not sent or not redacted: %s", r.Body)
				}
			},
		},
		{
			name: "sigv4",
			req:  bikasend.Request{Method: "GET", URL: "{{baseUrl}}/items/42", Auth: "sigv4"},
			check: func(t *testing.T, r bikasend.Response) {
				if !strings.Contains(r.Body, "AWS4-HMAC-SHA256 Credential=AK/") {
					t.Errorf("not signed: %s", r.Body)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := s.Send(context.Background(), tt.req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, resp)
			}
		})
	}

	if _, err := s.Send(context.Background(), bikasend.Request{Method: "GET", URL: "{{baseUrl}}/items/42", Auth: "oauth2"}); err != nil || tokenCalls.Load() != 1 {
		t.Errorf("token not cached: calls = %d, err = %v", tokenCalls.Load(), err)
	}

	p := s.Preview(bikasend.Request{Method: "GET", URL: "{{baseUrl}}/items/42?k={{api_key}}", Headers: "X-API-Key: {{api_key}}"})
	if strings.Contains(p.URL+p.Headers, "k-123456") || !p.Secret || p.Error != "" {
		t.Errorf("preview leaks or fails: %+v", p)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)
		s.Cancel("slow-1")
	}()
	if _, err := s.Send(context.Background(), bikasend.Request{ID: "slow-1", Method: "GET", URL: "{{baseUrl}}/slow"}); !errors.Is(err, bikasend.ErrCancelled) {
		t.Errorf("cancel: err = %v", err)
	}

	if h := s.History(); len(h) < 5 || h[0].Request.URL != "{{baseUrl}}/items/42" {
		t.Errorf("history keeps templates, newest first: %d entries, first %+v", len(h), h[0].Request)
	}
}

func TestSecretGuards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	s, _ := newTestService(t, srv.URL, testConfig, nil)
	if _, err := s.SetVariable(bikasend.ScopeSession, "token", bikasend.VariableInput{Kind: bikasend.KindSecret, Value: "abcdef"}); err != nil {
		t.Fatal(err)
	}
	// example.invalid never resolves; the guard must refuse before any network call.
	req := bikasend.Request{Method: "GET", URL: "http://example.invalid/x", Headers: "Authorization: Bearer {{token}}"}
	if _, err := s.Send(context.Background(), req); !errors.Is(err, bikasend.ErrSecretHost) {
		t.Errorf("secret to a host no environment lists: err = %v", err)
	}
	if p := s.Preview(req); p.BlockedBy != "unlisted" {
		t.Errorf("unlisted preview = %+v", p)
	}
	if _, err := s.Send(context.Background(), bikasend.Request{Method: "GET", URL: srv.URL, Headers: "Authorization: Bearer {{token}}"}); err != nil {
		t.Errorf("loopback needs no hosts list: %v", err)
	}
	req.AllowHost = "other.example.invalid"
	if _, err := s.Send(context.Background(), req); !errors.Is(err, bikasend.ErrSecretHost) {
		t.Errorf("confirming another host must not unlock this one: err = %v", err)
	}
	req.AllowHost = "example.invalid"
	if _, err := s.Send(context.Background(), req); !errors.Is(err, bikasend.ErrInsecureSecret) {
		t.Errorf("plain http with a secret: err = %v", err)
	}
	if p := s.Preview(req); !p.Insecure || p.BlockedBy != "insecure" {
		t.Errorf("preview = %+v", p)
	}
	req.AllowInsecure = true
	if _, err := s.Send(context.Background(), req); errors.Is(err, bikasend.ErrInsecureSecret) || errors.Is(err, bikasend.ErrSecretHost) {
		t.Errorf("confirmed send was still blocked: %v", err)
	}

	c, err := s.client("", nil)
	if err != nil {
		t.Fatal(err)
	}
	via := []*http.Request{httptest.NewRequest(http.MethodGet, "https://api.example.com/a", nil)}
	redirect := func(target string, secret bool) error {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		return c.CheckRedirect(r.WithContext(context.WithValue(r.Context(), carriesSecretKey{}, secret)), via)
	}
	if err := redirect("http://api.example.com/b", true); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("https to http redirect with a secret: %v", err)
	}
	if err := redirect("https://api.example.com/b", true); err != nil {
		t.Errorf("same-host https redirect: %v", err)
	}
	if err := redirect("http://api.example.com/b", false); err != nil {
		t.Errorf("downgrade without a secret: %v", err)
	}
}

func quoteJSON(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}
