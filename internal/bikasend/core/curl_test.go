package core

import (
	"errors"
	"reflect"
	"testing"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

func TestCurl(t *testing.T) {
	s, _ := newTestService(t, "https://api.example.com", testConfig, nil)
	if _, err := s.SetVariable(bikasend.ScopeSession, "api_key", bikasend.VariableInput{Kind: bikasend.KindSecret, Value: "k'123456"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Curl(bikasend.Request{
		Method: "POST", URL: "https://api.example.com/items?x={{api_key}}",
		Headers: "X-API-Key: {{api_key}}", BodyType: "json", Body: `{"a":"it's"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "# Set first: API_KEY\ncurl \\\n  -X \\\n  POST \\\n  'https://api.example.com/items?x='\"$API_KEY\" \\\n  -H \\\n  'X-Api-Key: '\"$API_KEY\" \\\n  --data-raw \\\n  '{\"a\":\"it'\\''s\"}'"
	if got != want {
		t.Errorf("curl =\n%s\nwant\n%s", got, want)
	}
	if _, err := s.Curl(bikasend.Request{Method: "GET", URL: "{{missing}}/x"}); err == nil {
		t.Error("curl with a missing variable must fail")
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
