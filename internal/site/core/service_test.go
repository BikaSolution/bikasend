package core

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BikaSolution/bikasend/pkg/github"
	"github.com/BikaSolution/bikasend/pkg/sdk/site"
)

const releasesJSON = `[
  {"tag_name": "v0.3.0-rc1", "draft": true, "assets": []},
  {"tag_name": "v0.2.0", "name": "Second", "body": "## What's Changed\r\n* **Faster** scan by @dev\r\n* Fix \u0060send\u0060\r\n\r\nFull notes\r\nhere", "published_at": "2026-11-01T10:00:00Z",
   "assets": [
     {"id": 3, "name": "Bikasend-v0.2.0-linux-amd64.zip", "size": 1048576, "digest": "sha256:cc"},
     {"id": 1, "name": "checksums.txt", "size": 10},
     {"id": 2, "name": "Bikasend-v0.2.0-windows-amd64.zip", "size": 2097152, "digest": "sha256:bb"},
     {"id": 4, "name": "Bikasend-v0.2.0-macos-universal.zip", "size": 3145728, "digest": "sha256:aa"}
   ]},
  {"tag_name": "v0.1.0", "prerelease": true, "published_at": "2026-10-01T10:00:00Z", "assets": []}
]`

func newGitHub(t *testing.T, fail *atomic.Bool, body string) (*github.Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/repos/example/bikasend/releases":
			calls.Add(1)
			_, _ = w.Write([]byte(body))
		case "/repos/example/bikasend/releases/assets/4":
			calls.Add(1)
			http.Redirect(w, r, "https://objects.example/mac.zip?sig=x", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	gh, err := github.New(srv.Client(), srv.URL, "example/bikasend", "")
	if err != nil {
		t.Fatal(err)
	}
	return gh, &calls
}

func TestReleases(t *testing.T) {
	var fail atomic.Bool
	gh, calls := newGitHub(t, &fail, releasesJSON)
	svc := NewService(gh, time.Hour)
	ctx := context.Background()

	releases, err := svc.Releases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 || releases[0].Tag != "v0.2.0" || !releases[1].Prerelease {
		t.Fatalf("drafts must be skipped, order kept: %+v", releases)
	}
	latest := releases[0]
	var platforms []string
	for _, d := range latest.Downloads {
		platforms = append(platforms, d.Platform)
	}
	if !reflect.DeepEqual(platforms, []string{"macOS", "Windows", "Linux"}) {
		t.Errorf("platforms = %v", platforms)
	}
	if mac := latest.Downloads[0]; mac.SHA256 != "aa" || mac.Size != "3.0 MB" {
		t.Errorf("mac download = %+v", mac)
	}
	if len(latest.Files) != 1 || latest.Files[0].Name != "checksums.txt" {
		t.Errorf("files = %+v", latest.Files)
	}
	wantNotes := []site.Note{
		{Kind: "h", Text: "What's Changed"},
		{Kind: "ul", Items: []string{"Faster scan by @dev", "Fix send"}},
		{Kind: "p", Text: "Full notes here"},
	}
	if !reflect.DeepEqual(latest.Notes, wantNotes) {
		t.Errorf("notes =\n%+v\nwant\n%+v", latest.Notes, wantNotes)
	}

	if _, err := svc.Releases(ctx); err != nil || calls.Load() != 1 {
		t.Errorf("second call within ttl: err = %v, calls = %d, want cached", err, calls.Load())
	}

	svc.fetched = time.Time{}
	fail.Store(true)
	if cached, err := svc.Releases(ctx); err != nil || len(cached) != 2 {
		t.Errorf("github down: want stale cache, got %d releases, err = %v", len(cached), err)
	}
}

func TestReleasesUnavailable(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	gh, calls := newGitHub(t, &fail, releasesJSON)
	svc := NewService(gh, time.Hour)
	if _, err := svc.Releases(context.Background()); !errors.Is(err, site.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
	fail.Store(false)
	if _, err := svc.Releases(context.Background()); !errors.Is(err, site.ErrUnavailable) || calls.Load() != 0 {
		t.Errorf("within the back-off: err = %v, calls = %d; want the cached error and no call", err, calls.Load())
	}
	svc.retryAt = time.Time{}
	if _, err := svc.Releases(context.Background()); err != nil {
		t.Errorf("after the back-off: %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	svc.fetched = time.Time{}
	if _, err := svc.Releases(cancelled); err != nil {
		t.Errorf("a visitor who left must not cancel the shared fetch: %v", err)
	}
}

func TestLatest(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantTag string
		wantErr error
	}{
		{name: "skips drafts and newer pre-releases", body: `[
		  {"tag_name": "v0.4.0", "draft": true},
		  {"tag_name": "v0.3.0-rc1", "prerelease": true},
		  {"tag_name": "v0.2.0"},
		  {"tag_name": "v0.1.0"}
		]`, wantTag: "v0.2.0"},
		{name: "falls back to the newest pre-release", body: `[{"tag_name": "v0.2.0-rc2", "prerelease": true}, {"tag_name": "v0.2.0-rc1", "prerelease": true}]`, wantTag: "v0.2.0-rc2"},
		{name: "no releases", body: `[]`, wantErr: site.ErrNotFound},
		{name: "only drafts", body: `[{"tag_name": "v0.1.0", "draft": true}]`, wantErr: site.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fail atomic.Bool
			gh, _ := newGitHub(t, &fail, tt.body)
			got, err := NewService(gh, time.Hour).Latest(context.Background())
			if !errors.Is(err, tt.wantErr) || got.Tag != tt.wantTag {
				t.Errorf("got %q, %v; want %q, %v", got.Tag, err, tt.wantTag, tt.wantErr)
			}
		})
	}
}

func TestDownloadURL(t *testing.T) {
	var fail atomic.Bool
	gh, calls := newGitHub(t, &fail, releasesJSON)
	svc := NewService(gh, time.Hour)

	tests := []struct {
		name    string
		tag     string
		file    string
		want    string
		wantErr error
	}{
		{name: "known asset", tag: "v0.2.0", file: "Bikasend-v0.2.0-macos-universal.zip", want: "https://objects.example/mac.zip?sig=x"},
		{name: "asset of another tag", tag: "v0.1.0", file: "Bikasend-v0.2.0-macos-universal.zip", wantErr: site.ErrNotFound},
		{name: "draft asset", tag: "v0.3.0-rc1", file: "x.zip", wantErr: site.ErrNotFound},
		{name: "traversal", tag: "..", file: "../../etc/passwd", wantErr: site.ErrNotFound},
		{name: "github refuses asset", tag: "v0.2.0", file: "checksums.txt", wantErr: site.ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.DownloadURL(context.Background(), tt.tag, tt.file)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Errorf("got %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}

	before := calls.Load()
	for range 5 {
		if _, err := svc.DownloadURL(context.Background(), "v0.2.0", "Bikasend-v0.2.0-macos-universal.zip"); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load() - before; n != 0 {
		t.Errorf("repeat downloads called GitHub %d times, want the cached signed URL", n)
	}
}
