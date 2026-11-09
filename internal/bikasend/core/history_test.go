package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

func TestDownloadUpdate(t *testing.T) {
	payload := []byte("pretend zip")
	sum := sha256.Sum256(payload)
	good := hex.EncodeToString(sum[:])
	plat := platformNames[runtime.GOOS]
	name := "Bikasend-v9.9.9-test.zip"

	away := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) }))
	defer away.Close()
	var entry string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/latest.json":
			_, _ = fmt.Fprintf(w, `{"tag":"v9.9.9","downloads":[%s]}`, entry)
		case r.URL.Path == "/downloads/away":
			// localhost is another host name than the site's 127.0.0.1.
			http.Redirect(w, r, strings.Replace(away.URL, "127.0.0.1", "localhost", 1), http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/downloads/"):
			http.Redirect(w, r, "/blob", http.StatusFound)
		case r.URL.Path == "/blob":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()
	dl := func(platform, file, sha, u string) string {
		return fmt.Sprintf(`{"platform":%q,"name":%q,"sha256":%q,"url":%q}`, platform, file, sha, u)
	}

	s, _ := newTestService(t, "", "", nil)
	s.opts.UpdateURL = site.URL
	dir := t.TempDir()
	s.opts.DownloadDir = dir

	entry = dl(plat, name, good, "/downloads/v9.9.9/"+name)
	for _, want := range []string{name, "Bikasend-v9.9.9-test-1.zip"} {
		path, err := s.DownloadUpdate(context.Background())
		if err != nil || path != filepath.Join(dir, want) {
			t.Fatalf("download = %q, %v; want %s", path, err, want)
		}
		if data, _ := os.ReadFile(path); string(data) != string(payload) {
			t.Errorf("%s holds %q", want, data)
		}
	}

	tests := []struct {
		name, entry string
		max         int64
	}{
		{name: "SHA-256 mismatch", entry: dl(plat, name, strings.Repeat("0", 64), "/downloads/v9.9.9/"+name)},
		{name: "a path in the file name", entry: dl(plat, "../Bikasend-v9.9.9-test.zip", good, "/downloads/x")},
		{name: "an absolute URL", entry: dl(plat, name, good, "https://evil.example/"+name)},
		{name: "a redirect to another host", entry: dl(plat, name, good, "/downloads/away")},
		{name: "no build for this platform", entry: dl("Plan 9", name, good, "/downloads/v9.9.9/"+name)},
		{name: "larger than the cap", entry: dl(plat, name, good, "/downloads/v9.9.9/"+name), max: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			empty := t.TempDir()
			s.opts.DownloadDir = empty
			entry = tt.entry
			if tt.max > 0 {
				defer func(old int64) { maxUpdate = old }(maxUpdate)
				maxUpdate = tt.max
			}
			if path, err := s.DownloadUpdate(context.Background()); !errors.Is(err, bikasend.ErrUpdate) {
				t.Errorf("want ErrUpdate, got %q, %v", path, err)
			}
			if left, _ := os.ReadDir(empty); len(left) != 0 {
				t.Errorf("a failed download left %d files", len(left))
			}
		})
	}
}
