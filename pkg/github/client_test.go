package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew(t *testing.T) {
	for _, repo := range []string{"", "owner", "owner/name/extra", "../etc", "owner/..", "a b/c"} {
		if _, err := New(http.DefaultClient, "https://api.github.com", repo, ""); !errors.Is(err, ErrInvalidRepo) {
			t.Errorf("repo %q: err = %v, want ErrInvalidRepo", repo, err)
		}
	}
	for _, base := range []string{"", "http://api.github.com", "ftp://x", "https://"} {
		if _, err := New(http.DefaultClient, base, "o/r", "secret"); !errors.Is(err, ErrInvalidAPI) {
			t.Errorf("base %q must be refused: err = %v", base, err)
		}
	}
	c, err := New(http.DefaultClient, "https://api.github.com", "o/r", "")
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]bool{"objects.githubusercontent.com": true, "release-assets.githubusercontent.com": true, "github.com": true, "evil.com": false, "githubusercontent.com.evil.com": false} {
		if c.assetHost(host) != want {
			t.Errorf("asset host %q allowed = %v, want %v", host, !want, want)
		}
	}
}

func TestClient(t *testing.T) {
	var gotAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/repos/example/app/releases":
			if r.URL.Query().Get("per_page") != "5" {
				t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
			}
			_, _ = w.Write([]byte(`[{"tag_name":"v1.0.0","assets":[{"id":7,"name":"a.zip","size":3,"digest":"sha256:ab"}]}]`))
		case "/repos/example/app/releases/assets/7":
			if r.Header.Get("Accept") != "application/octet-stream" {
				t.Errorf("accept = %q", r.Header.Get("Accept"))
			}
			http.Redirect(w, r, "https://objects.example/a.zip?sig=1", http.StatusFound)
		case "/repos/example/app/releases/assets/8":
			http.Redirect(w, r, "http://objects.example/a.zip", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.Client(), srv.URL, "example/app", "secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	releases, err := c.Releases(ctx, 5)
	if err != nil || len(releases) != 1 || releases[0].Assets[0].Digest != "sha256:ab" {
		t.Fatalf("releases = %+v, err = %v", releases, err)
	}
	u, err := c.AssetURL(ctx, 7)
	if err != nil || u != "https://objects.example/a.zip?sig=1" {
		t.Errorf("asset url = %q, err = %v", u, err)
	}
	if _, err := c.AssetURL(ctx, 8); !errors.Is(err, ErrUpstream) {
		t.Errorf("plain http redirect: err = %v, want ErrUpstream", err)
	}
	if _, err := c.AssetURL(ctx, 9); !errors.Is(err, ErrUpstream) {
		t.Errorf("missing asset: err = %v, want ErrUpstream", err)
	}
	for _, a := range gotAuth {
		if a != "Bearer secret" {
			t.Errorf("authorization = %q", a)
		}
	}

	bad, _ := New(srv.Client(), srv.URL, "example/missing", "")
	if _, err := bad.Releases(ctx, 5); !errors.Is(err, ErrUpstream) {
		t.Errorf("missing repo: err = %v, want ErrUpstream", err)
	}
}
