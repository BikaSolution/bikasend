package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
)

func TestScrubRequest(t *testing.T) {
	r := scrubRequest(bikasend.Request{
		Headers:  "X-API-Key: raw-key-1\nAuthorization: Bearer {{token}}\nAccept: json",
		Query:    []bikasend.KV{{Name: "api_key", Value: "raw"}, {Name: "page", Value: "2"}},
		Form:     []bikasend.Field{{Name: "password", Value: "hunter2"}, {Name: "avatar", Value: "/tmp/a.png", File: true}},
		BodyType: "json",
		Body:     `{"user":"a","password":"hunter2","nested":{"token":"{{token}}","client_secret":"x"}}`,
	})
	if r.Headers != "X-API-Key: ••••••\nAuthorization: Bearer {{token}}\nAccept: json" {
		t.Errorf("headers = %q", r.Headers)
	}
	if r.Query[0].Value != maskText || r.Query[1].Value != "2" || r.Form[0].Value != maskText || r.Form[1].Value != "/tmp/a.png" {
		t.Errorf("query %+v form %+v", r.Query, r.Form)
	}
	if strings.Contains(r.Body, "hunter2") || strings.Contains(r.Body, `"x"`) || !strings.Contains(r.Body, "{{token}}") {
		t.Errorf("body = %s", r.Body)
	}

	u := scrubRequest(bikasend.Request{URL: "https://x.example.com/a?page=2&access_token=raw-1&key={{k}}#top", BodyType: "xml", Body: "<pw>hunter2</pw>"})
	if u.URL != "https://x.example.com/a?page=2&access_token=••••••&key={{k}}#top" || u.Body != maskText {
		t.Errorf("url %q body %q", u.URL, u.Body)
	}
	if tmpl := scrubRequest(bikasend.Request{BodyType: "text", Body: "{{payload}}\n"}); tmpl.Body != "{{payload}}\n" {
		t.Errorf("a pure template body must stay: %q", tmpl.Body)
	}
}

func TestCheckUpdate(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"tag":"v1.3.0","downloads":[]}`))
	}))
	defer site.Close()

	s, _ := newTestService(t, "", "", nil)
	s.opts.UpdateURL = site.URL
	up, err := s.CheckUpdate(context.Background())
	if err != nil || !up.Available || up.Latest != "v1.3.0" || up.URL != site.URL+"/#download" {
		t.Errorf("update = %+v, %v", up, err)
	}

	for a, b := range map[[2]string]bool{{"v1.3.0", "v1.2.9"}: true, {"v1.2.3", "v1.2.3"}: false, {"v2.0.0", "dev"}: false, {"v1.10.0", "v1.9.0"}: true, {"v1.0.0-rc1", "v0.9.0"}: true} {
		if newer(a[0], a[1]) != b {
			t.Errorf("newer(%s, %s) = %v", a[0], a[1], !b)
		}
	}
}
