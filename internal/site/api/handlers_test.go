package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BikaSolution/bikasend/internal/site/core"
	"github.com/BikaSolution/bikasend/pkg/github"
	"github.com/BikaSolution/bikasend/pkg/httpserve"
)

const (
	macUA     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15"
	windowsUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

var testOptions = Options{TrustCloudflare: true, PublicURL: "https://send.example.com", Repo: "example/bikasend"}

func newHandler(t *testing.T, releases string, down *atomic.Bool) http.Handler {
	t.Helper()
	return newHandlerWith(t, releases, down, testOptions)
}

func newHandlerWith(t *testing.T, releases string, down *atomic.Bool, opts Options) http.Handler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/repos/example/bikasend/releases":
			_, _ = w.Write([]byte(releases))
		case "/repos/example/bikasend/releases/assets/2":
			http.Redirect(w, r, "https://objects.example/win.zip?sig=x", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	gh, err := github.New(srv.Client(), srv.URL, "example/bikasend", "")
	if err != nil {
		t.Fatal(err)
	}
	return httpserve.Handler(Routes(core.NewService(gh, time.Hour), opts))
}

func TestHandlers(t *testing.T) {
	releases := `[
	  {"tag_name": "v0.2.0", "name": "<img src=x onerror=alert(1)>", "body": "* <script>alert(1)</script>\r\n* two & three", "published_at": "2026-11-01T10:00:00Z",
	   "assets": [
	     {"id": 1, "name": "Bikasend-v0.2.0-macos-universal.zip", "size": 3145728, "digest": "sha256:aa11"},
	     {"id": 2, "name": "Bikasend-v0.2.0-windows-amd64.zip", "size": 2097152, "digest": "sha256:bb22"}
	   ]},
	  {"tag_name": "v0.1.0", "published_at": "2026-10-01T10:00:00Z",
	   "assets": [{"id": 9, "name": "Bikasend-v0.1.0-linux-amd64.zip", "size": 1}]},
	  {"tag_name": "v0.0.1-\"><b>x", "prerelease": true, "published_at": "2026-09-01T10:00:00Z", "assets": []}
	]`
	var down atomic.Bool
	handler := newHandler(t, releases, &down)

	tests := []struct {
		name         string
		path         string
		ua           string
		wantStatus   int
		contentType  string
		cacheControl string
		location     string
		contains     []string
		excludes     []string
	}{
		{
			name:        "index for a Windows visitor recommends the Windows build",
			path:        "/",
			ua:          windowsUA,
			wantStatus:  http.StatusOK,
			contentType: "text/html; charset=utf-8",
			contains: []string{
				`href="/downloads/v0.2.0/Bikasend-v0.2.0-windows-amd64.zip">`, "Download for Windows",
				"SHA-256 aa11", "SHA-256 bb22", "Released 1 November 2026",
				`href="/downloads/v0.1.0/Bikasend-v0.1.0-linux-amd64.zip">Linux</a>`,
				`<link rel="stylesheet" href="/static/site.css?v=`,
				`<link rel="alternate" type="application/atom+xml" title="Bikasend releases" href="/feed.xml">`,
				`<a class="hide-sm" href="/changelog">Changelog</a>`,
			},
			excludes: []string{"<style", "style="},
		},
		{
			name:       "index for a Mac visitor recommends the macOS build",
			path:       "/",
			ua:         macUA,
			wantStatus: http.StatusOK,
			contains:   []string{"Download for macOS"},
			excludes:   []string{"Download for Windows"},
		},
		{
			name:       "release names, tags and notes from GitHub are escaped on the index",
			path:       "/",
			wantStatus: http.StatusOK,
			contains:   []string{"&lt;img src=x onerror=alert(1)&gt;", "&lt;script&gt;alert(1)&lt;/script&gt;", "v0.0.1-&#34;&gt;&lt;b&gt;x"},
			excludes:   []string{"<img src=x", "<script>alert", "<b>x"},
		},
		{
			name:        "changelog lists every release with an anchor, notes and downloads",
			path:        "/changelog",
			wantStatus:  http.StatusOK,
			contentType: "text/html; charset=utf-8",
			contains: []string{
				`id="v0.2.0"`, `id="v0.1.0"`, `id="v0.0.1-&#34;&gt;&lt;b&gt;x"`,
				"&lt;img src=x onerror=alert(1)&gt;", "<li>&lt;script&gt;alert(1)&lt;/script&gt;</li>", "<li>two &amp; three</li>",
				`<a href="/downloads/v0.2.0/Bikasend-v0.2.0-windows-amd64.zip">Windows</a> · 2.0 MB`, "SHA-256 aa11",
				"No release notes for this version.", `href="/static/site.css?v=`,
			},
			excludes: []string{"<img src=x", "<script>alert", `"><b>x`, "style="},
		},
		{
			name:         "feed has an entry per release with escaped text content",
			path:         "/feed.xml",
			wantStatus:   http.StatusOK,
			contentType:  "application/atom+xml; charset=utf-8",
			cacheControl: "public, max-age=300",
			contains: []string{
				`<?xml version="1.0" encoding="UTF-8"?>`, `<feed xmlns="http://www.w3.org/2005/Atom">`,
				"<updated>2026-11-01T10:00:00Z</updated>", "<id>urn:bikasend:release:v0.2.0</id>",
				`<link rel="alternate" type="text/html" href="/changelog#v0.2.0"></link>`,
				"<title>&lt;img src=x onerror=alert(1)&gt;</title>",
				`<content type="text">- &lt;script&gt;alert(1)&lt;/script&gt;&#xA;- two &amp; three</content>`,
				"<title>v0.1.0</title>",
			},
			excludes: []string{"<img", "<script", "<b>"},
		},
		{
			name:       "index has a canonical link, preview tags and GitHub links from the configuration",
			path:       "/",
			wantStatus: http.StatusOK,
			contains: []string{
				"<title>Bikasend: open-source Swagger and OpenAPI desktop client</title>",
				`<link rel="canonical" href="https://send.example.com/">`,
				`<meta property="og:url" content="https://send.example.com/">`,
				`<meta property="og:image" content="https://send.example.com/static/og.jpg">`,
				`<meta name="twitter:card" content="summary_large_image">`,
				`<a class="hide-sm" href="https://github.com/example/bikasend" rel="noopener">GitHub</a>`,
				`<a href="https://github.com/example/bikasend/blob/main/LICENSE" rel="noopener">MIT licence</a>`,
			},
		},
		{
			name:       "changelog has its own canonical link and title",
			path:       "/changelog",
			wantStatus: http.StatusOK,
			contains:   []string{"<title>Changelog: Bikasend</title>", `<link rel="canonical" href="https://send.example.com/changelog">`},
		},
		{
			name:         "robots points to the sitemap",
			path:         "/robots.txt",
			wantStatus:   http.StatusOK,
			contentType:  "text/plain; charset=utf-8",
			cacheControl: "public, max-age=3600",
			contains:     []string{"User-agent: *\nAllow: /\n", "Sitemap: https://send.example.com/sitemap.xml"},
		},
		{
			name:        "sitemap lists the pages with the latest release date",
			path:        "/sitemap.xml",
			wantStatus:  http.StatusOK,
			contentType: "application/xml; charset=utf-8",
			contains: []string{
				`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`,
				"<loc>https://send.example.com/</loc><lastmod>2026-11-01</lastmod>",
				"<loc>https://send.example.com/changelog</loc>",
			},
		},
		{
			name:         "latest links relative to the site",
			path:         "/latest.json",
			wantStatus:   http.StatusOK,
			contentType:  "application/json",
			cacheControl: "public, max-age=300",
			contains: []string{`{"tag":"v0.2.0","name":"\u003cimg src=x onerror=alert(1)\u003e","prerelease":false,"published":"2026-11-01T10:00:00Z","notesUrl":"/changelog#v0.2.0","downloads":[` +
				`{"platform":"macOS","name":"Bikasend-v0.2.0-macos-universal.zip","size":"3.0 MB","sha256":"aa11","url":"/downloads/v0.2.0/Bikasend-v0.2.0-macos-universal.zip"},` +
				`{"platform":"Windows","name":"Bikasend-v0.2.0-windows-amd64.zip","size":"2.0 MB","sha256":"bb22","url":"/downloads/v0.2.0/Bikasend-v0.2.0-windows-amd64.zip"}]}`},
		},
		{name: "download redirects to the signed url", path: "/downloads/v0.2.0/Bikasend-v0.2.0-windows-amd64.zip", wantStatus: http.StatusFound, cacheControl: "no-store", location: "https://objects.example/win.zip?sig=x"},
		{name: "unknown asset", path: "/downloads/v0.2.0/secret.txt", wantStatus: http.StatusNotFound},
		{name: "traversal", path: "/downloads/v0.2.0/..%2F..%2Fgo.mod", wantStatus: http.StatusNotFound},
		{name: "github refuses the asset", path: "/downloads/v0.1.0/Bikasend-v0.1.0-linux-amd64.zip", wantStatus: http.StatusBadGateway},
		{name: "favicon", path: "/favicon.ico", wantStatus: http.StatusOK},
		{name: "stylesheet", path: "/static/site.css", wantStatus: http.StatusOK, contentType: "text/css; charset=utf-8", contains: []string{"--accent: #0369a1", ".card h3.card-title"}},
		{name: "static font", path: "/static/OpenSans.woff2", wantStatus: http.StatusOK},
		{name: "static unknown file", path: "/static/handlers.go", wantStatus: http.StatusNotFound},
		{name: "static traversal", path: "/static/..%2Ftemplates%2Findex.html", wantStatus: http.StatusNotFound},
		{name: "unknown page", path: "/admin", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			r.Header.Set("User-Agent", tt.ua)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			h := w.Header()
			if got := h.Get("Location"); got != tt.location {
				t.Errorf("location = %q, want %q", got, tt.location)
			}
			if got := h.Get("Content-Type"); tt.contentType != "" && got != tt.contentType {
				t.Errorf("content type = %q, want %q", got, tt.contentType)
			}
			if got := h.Get("Cache-Control"); tt.cacheControl != "" && got != tt.cacheControl {
				t.Errorf("cache control = %q, want %q", got, tt.cacheControl)
			}
			if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "style-src 'self'") || strings.Contains(csp, "unsafe") {
				t.Errorf("CSP = %q", csp)
			}
			if tt.path == "/" && tt.wantStatus == http.StatusOK && h.Get("Vary") != "User-Agent" {
				t.Errorf("index varies by User-Agent but Vary = %q", h.Get("Vary"))
			}
			body := w.Body.String()
			for _, s := range tt.contains {
				if !strings.Contains(body, s) {
					t.Errorf("body missing %q", s)
				}
			}
			for _, s := range tt.excludes {
				if strings.Contains(body, s) {
					t.Errorf("body contains %q", s)
				}
			}
		})
	}
}

func TestLinksIgnoreHostHeader(t *testing.T) {
	var down atomic.Bool
	handler := newHandler(t, `[{"tag_name": "v0.1.0"}]`, &down)
	for _, path := range []string{"/", "/changelog", "/robots.txt", "/sitemap.xml"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = "evil.example"
		r.Header.Set("X-Forwarded-Host", "evil.example")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "evil.example") {
			t.Errorf("%s: a link used the request's Host header", path)
		}
	}
}

func TestWithoutPublicURL(t *testing.T) {
	var down atomic.Bool
	handler := newHandlerWith(t, `[{"tag_name": "v0.1.0"}]`, &down, Options{})
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	if w := get("/sitemap.xml"); w.Code != http.StatusNotFound {
		t.Errorf("sitemap without a public URL: status %d, want 404", w.Code)
	}
	if body := get("/robots.txt").Body.String(); strings.Contains(body, "Sitemap") {
		t.Errorf("robots without a public URL: %q", body)
	}
	if body := get("/").Body.String(); strings.Contains(body, "canonical") || strings.Contains(body, "og:url") || strings.Contains(body, "github.com") {
		t.Error("index without options must leave out canonical, og:url and GitHub links")
	}
}

func TestWithoutReleases(t *testing.T) {
	tests := []struct {
		name       string
		releases   string
		down       bool
		path       string
		wantStatus int
		contains   string
	}{
		{name: "index when GitHub is down", down: true, path: "/", wantStatus: http.StatusOK, contains: "Release information is unavailable"},
		{name: "changelog when GitHub is down", down: true, path: "/changelog", wantStatus: http.StatusOK, contains: "Release information is unavailable"},
		{name: "latest when GitHub is down", down: true, path: "/latest.json", wantStatus: http.StatusServiceUnavailable, contains: `{"error":"unavailable"}`},
		{name: "feed when GitHub is down", down: true, path: "/feed.xml", wantStatus: http.StatusServiceUnavailable},
		{name: "latest with no releases", releases: `[]`, path: "/latest.json", wantStatus: http.StatusNotFound, contains: `{"error":"no releases"}`},
		{name: "feed with no releases", releases: `[]`, path: "/feed.xml", wantStatus: http.StatusOK, contains: "<updated>1970-01-01T00:00:00Z</updated>"},
		{name: "changelog with no releases", releases: `[]`, path: "/changelog", wantStatus: http.StatusOK, contains: "No release has been published yet."},
		{name: "sitemap when GitHub is down", down: true, path: "/sitemap.xml", wantStatus: http.StatusOK, contains: "<url><loc>https://send.example.com/</loc></url>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var down atomic.Bool
			down.Store(tt.down)
			handler := newHandler(t, tt.releases, &down)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if w.Code != tt.wantStatus || !strings.Contains(w.Body.String(), tt.contains) {
				t.Errorf("status = %d, body = %q; want %d with %q", w.Code, w.Body.String(), tt.wantStatus, tt.contains)
			}
			if tt.wantStatus != http.StatusOK && w.Header().Get("Cache-Control") == "public, max-age=300" {
				t.Error("an error must not be cached")
			}
		})
	}
}

func TestRateLimits(t *testing.T) {
	var down atomic.Bool
	handler := newHandler(t, `[{"tag_name": "v0.1.0"}]`, &down)
	get := func(path, ip string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "172.18.0.3:4100"
		r.Header.Set("CF-Connecting-IP", ip)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	for i := range downloadsBurst {
		if w := get("/latest.json", "203.0.113.1"); w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 within the burst", i+1, w.Code)
		}
	}
	for _, path := range []string{"/latest.json", "/downloads/v0.1.0/x.zip"} {
		w := get(path, "203.0.113.1")
		if w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "2" {
			t.Errorf("%s over the limit: status = %d, Retry-After = %q; want 429, 2", path, w.Code, w.Header().Get("Retry-After"))
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
			t.Errorf("%s: 429 lacks the security headers", path)
		}
	}
	if w := get("/", "203.0.113.1"); w.Code != http.StatusOK {
		t.Errorf("pages have their own limit: status = %d", w.Code)
	}
	if w := get("/latest.json", "203.0.113.2"); w.Code != http.StatusOK {
		t.Errorf("another client: status = %d", w.Code)
	}

	for range pagesBurst {
		get("/static/site.css", "203.0.113.3")
	}
	if w := get("/changelog", "203.0.113.3"); w.Code != http.StatusTooManyRequests {
		t.Errorf("pages over the limit: status = %d, want 429", w.Code)
	}
}
