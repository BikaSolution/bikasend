package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

func TestExport(t *testing.T) {
	s, _ := newTestService(t, "https://api.example.com", testConfig, nil)
	set := func(name, value string) {
		t.Helper()
		if _, err := s.SetVariable(bikasend.ScopeSession, name, bikasend.VariableInput{Kind: bikasend.KindSecret, Value: value}); err != nil {
			t.Fatal(err)
		}
	}
	set("api_key", "k'123456")
	req := bikasend.Request{
		Method: "POST", URL: "https://api.example.com/items?x={{api_key}}",
		Headers: "X-API-Key: {{api_key}}", BodyType: "json", Body: `{"a":"it's"}`,
	}
	tests := []struct {
		format string
		want   string
		has    []string
	}{
		{format: "curl", want: "# Set first: BIKASEND_API_KEY\ncurl --globoff -X 'POST' 'https://api.example.com/items?x='\"$BIKASEND_API_KEY\" \\\n  -H 'X-Api-Key: '\"$BIKASEND_API_KEY\" \\\n  --data-raw '{\"a\":\"it'\\''s\"}'"},
		{format: "powershell", want: "# Set first: $env:BIKASEND_API_KEY\n$headers = @{\n  'X-Api-Key' = $env:BIKASEND_API_KEY\n}\n$body = '{\"a\":\"it''s\"}'\nInvoke-RestMethod -Method 'POST' -Uri ('https://api.example.com/items?x=' + $env:BIKASEND_API_KEY) -Headers $headers -Body $body"},
		{format: "httpie", has: []string{"http --ignore-stdin 'POST' 'https://api.example.com/items?x='\"$BIKASEND_API_KEY\"", "'X-Api-Key:'\"$BIKASEND_API_KEY\"", "--raw '{\"a\":\"it'\\''s\"}'"}},
		{format: "fetch", has: []string{`fetch("https://api.example.com/items?x=" + process.env.BIKASEND_API_KEY, {`, `"X-Api-Key": process.env.BIKASEND_API_KEY,`, `body: "{\"a\":\"it's\"}",`}},
		{format: "python", has: []string{"import os", `"https://api.example.com/items?x=" + os.environ["BIKASEND_API_KEY"],`, `"X-Api-Key": os.environ["BIKASEND_API_KEY"],`, `data="{\"a\":\"it's\"}",`}},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			got, err := s.Export(req, tt.format)
			if err != nil {
				t.Fatal(err)
			}
			if tt.want != "" && got != tt.want {
				t.Errorf("got\n%s\nwant\n%s", got, tt.want)
			}
			for _, h := range tt.has {
				if !strings.Contains(got, h) {
					t.Errorf("missing %q in\n%s", h, got)
				}
			}
			if strings.Contains(got, "123456") {
				t.Errorf("the secret value leaked into the export:\n%s", got)
			}
		})
	}

	// Values come from specs, cURL imports and history: quoting must hold against all of them.
	hostile := bikasend.Request{Method: "get&id", URL: "https://api.example.com/x?[1-9]", Headers: "X-Note: it\u2019s $(id) `id`"}
	if got, _ := s.Export(hostile, "curl"); !strings.Contains(got, "curl --globoff -X 'GET&ID'") || !strings.Contains(got, "'X-Note: it\u2019s $(id) `id`'") {
		t.Errorf("curl quoting:\n%s", got)
	}
	if got, _ := s.Export(hostile, "powershell"); !strings.Contains(got, "'it\u2019\u2019s $(id) `id`'") || !strings.Contains(got, "-Method 'GET&ID'") {
		t.Errorf("powershell must double curly quotes too:\n%s", got)
	}

	// A spec example such as @.env must never become "upload this local file".
	atFile := bikasend.Request{Method: "POST", URL: "https://api.example.com/up", Headers: "X-Doc: @.env", BodyType: "multipart",
		Form: []bikasend.Field{{Name: "doc", Value: "@.env", Enabled: true}}}
	if got, err := s.Export(atFile, "curl"); err != nil || !strings.Contains(got, "--form-string 'doc=@.env'") || strings.Contains(got, "-F 'doc=") {
		t.Errorf("curl text fields must use --form-string:\n%s, %v", got, err)
	}
	if _, err := s.Export(atFile, "httpie"); !errors.Is(err, bikasend.ErrInvalidRequest) {
		t.Errorf("httpie would read @.env as a file: %v", err)
	}

	refused := []struct {
		name   string
		r      bikasend.Request
		format string
	}{
		{name: "a URL-escaped secret in a form body", r: bikasend.Request{Method: "POST", URL: "https://api.example.com/f", BodyType: "form", Form: []bikasend.Field{{Name: "token", Value: "{{slashy}}", Enabled: true}}}, format: "curl"},
		{name: "a secret too short to replace", r: bikasend.Request{Method: "GET", URL: "https://api.example.com/{{pin}}"}, format: "python"},
		{name: "an escape character in a header", r: bikasend.Request{Method: "GET", URL: "https://api.example.com/", Headers: "X-A: a\x1b[201~b"}, format: "curl"},
		{name: "a bidi override", r: bikasend.Request{Method: "GET", URL: "https://api.example.com/", Headers: "X-A: a\u202eb"}, format: "fetch"},
		{name: "an invalid header name", r: bikasend.Request{Method: "GET", URL: "https://api.example.com/", Headers: "Bad Name: x"}, format: "curl"},
		{name: "an unknown format", r: req, format: "bash; rm"},
	}
	set("slashy", "a+b/c=d")
	set("pin", "12")
	for _, tt := range refused {
		if got, err := s.Export(tt.r, tt.format); !errors.Is(err, bikasend.ErrInvalidRequest) {
			t.Errorf("%s: want a refusal, got %q, %v", tt.name, got, err)
		}
	}
	if got, _ := s.Export(bikasend.Request{Method: "POST", URL: "https://api.example.com/", BodyType: "text", Body: "a\u2028b"}, "fetch"); !strings.Contains(got, `"a\u2028b"`) {
		t.Errorf("fetch must escape U+2028:\n%s", got)
	}

	set("api-key", "other-value")
	if got, err := s.Export(bikasend.Request{Method: "GET", URL: "https://api.example.com/?a={{api_key}}&b={{api-key}}"}, "curl"); err != nil || !strings.Contains(got, "BIKASEND_API_KEY_2") {
		t.Errorf("two names that collapse to one variable need a suffix:\n%s, %v", got, err)
	}
	if _, err := s.Export(bikasend.Request{Method: "GET", URL: "{{missing}}/x"}, "curl"); err == nil {
		t.Error("export with a missing variable must fail")
	}
}

func TestImportCurl(t *testing.T) {
	s, _ := newTestService(t, "", "", nil)
	tests := []struct {
		name    string
		cmd     string
		want    bikasend.Request
		wantErr error
	}{
		{
			name: "json post with headers and continuation",
			cmd:  "curl -X POST 'https://api.example.com/items' \\\n  -H 'Authorization: Bearer abc' \\\n  -H \"Accept: application/json\" \\\n  --data-raw '{\"a\":1}'",
			want: bikasend.Request{Method: "POST", URL: "https://api.example.com/items", BodyType: "json", Body: `{"a":1}`, Headers: "Authorization: Bearer abc\nAccept: application/json\nContent-Type: application/json"},
		},
		{
			name: "form data defaults to POST and urlencoded",
			cmd:  `curl https://x.example.com/login -d user=a -d pass=b -u me:pw --compressed -s`,
			want: bikasend.Request{Method: "POST", URL: "https://x.example.com/login", BodyType: "text", Body: "user=a&pass=b", Headers: "Authorization: Basic bWU6cHc=\nContent-Type: application/x-www-form-urlencoded"},
		},
		{
			name: "multipart with a file",
			cmd:  `curl -F name=x -F file=@/tmp/a.png https://x.example.com/up`,
			want: bikasend.Request{Method: "POST", URL: "https://x.example.com/up", BodyType: "multipart", Form: []bikasend.Field{{Name: "name", Value: "x", Enabled: true}, {Name: "file", Value: "/tmp/a.png", File: true, Enabled: true}}},
		},
		{
			name: "get with -G moves data into the query",
			cmd:  `curl -G --url=https://x.example.com/s -d q=go`,
			want: bikasend.Request{Method: "GET", URL: "https://x.example.com/s?q=go", BodyType: "none"},
		},
		{name: "not curl", cmd: `wget https://x`, wantErr: bikasend.ErrCurl},
		{name: "no url", cmd: `curl -X GET`, wantErr: bikasend.ErrCurl},
		{name: "unterminated quote", cmd: `curl 'https://x`, wantErr: bikasend.ErrCurl},
		{name: "body from a file", cmd: `curl -d @body.json https://x.example.com`, wantErr: bikasend.ErrCurl},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.ImportCurl(tt.cmd)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if tt.want.Form == nil {
				tt.want.Form = []bikasend.Field{}
			}
			tt.want.PathParams, tt.want.Query = []bikasend.KV{}, []bikasend.KV{}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}
