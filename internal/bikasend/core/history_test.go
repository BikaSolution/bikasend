package core

import (
	"archive/zip"
	"bytes"
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

	"github.com/BikaSolution/bikasend/pkg/github"
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

	s.opts.Version = "v9.9.9"
	entry = dl(plat, name, good, "/downloads/v9.9.9/"+name)
	if _, err := s.DownloadUpdate(context.Background()); !errors.Is(err, bikasend.ErrUpdate) {
		t.Errorf("the same or an older version must not download: %v", err)
	}
	s.opts.Version = "v1.2.3"

	entry = dl(plat, name, good, "/downloads/v9.9.9/"+name)
	for _, want := range []string{name, "Bikasend-v9.9.9-test-1.zip"} {
		res, err := s.DownloadUpdate(context.Background())
		if err != nil || res.Path != filepath.Join(dir, want) || res.SHA256 != good || res.SiteSHA256 != good {
			t.Fatalf("download = %+v, %v; want %s", res, err, want)
		}
		if res.Installable || !strings.Contains(res.Reason, "GitHub") {
			t.Errorf("without a GitHub repo the update must not install: %+v", res)
		}
		if data, _ := os.ReadFile(res.Path); string(data) != string(payload) {
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
		{name: "a name for another tag", entry: dl(plat, "Bikasend-v1.0.0-test.zip", good, "/downloads/v9.9.9/x")},
		{name: "a name that is too long", entry: dl(plat, "Bikasend-v9.9.9-"+strings.Repeat("a", 300)+".zip", good, "/downloads/v9.9.9/x")},
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
			if res, err := s.DownloadUpdate(context.Background()); !errors.Is(err, bikasend.ErrUpdate) {
				t.Errorf("want ErrUpdate, got %+v, %v", res, err)
			}
			if left, _ := os.ReadDir(empty); len(left) != 0 {
				t.Errorf("a failed download left %d files", len(left))
			}
		})
	}
}

type fakeReleases struct {
	rel github.Release
	err error
}

func (f *fakeReleases) ReleaseByTag(context.Context, string) (github.Release, error) {
	return f.rel, f.err
}

type zipEntry struct {
	name string
	mode os.FileMode
	data string
}

func makeZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		hdr.SetMode(e.mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(e.data))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallUpdate(t *testing.T) {
	name := "Bikasend-v9.9.9-test.zip"
	var payload []byte
	var plat string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.json":
			sum := sha256.Sum256(payload)
			_, _ = fmt.Fprintf(w, `{"tag":"v9.9.9","downloads":[{"platform":%q,"name":%q,"sha256":%q,"url":"/downloads/v9.9.9/x"}]}`, plat, name, hex.EncodeToString(sum[:]))
		case "/downloads/v9.9.9/x":
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer site.Close()

	// setup returns a service whose running app is exe, built for goos, with GitHub listing the payload's hash.
	setup := func(t *testing.T, goos, exe string, zipData []byte) (*Service, *fakeReleases, *[]string) {
		payload, plat = zipData, platformNames[goos]
		s, _ := newTestService(t, "", "", nil)
		s.opts.UpdateURL = site.URL
		s.opts.DownloadDir = t.TempDir()
		s.opts.GOOS = goos
		s.opts.Executable = func() (string, error) { return exe, nil }
		sum := sha256.Sum256(zipData)
		gh := &fakeReleases{rel: github.Release{TagName: "v9.9.9", Assets: []github.Asset{{Name: name, Digest: "sha256:" + hex.EncodeToString(sum[:])}}}}
		s.opts.GitHub = gh
		started := &[]string{}
		s.opts.Start = func(goos, target string) error { *started = append(*started, goos+" "+target); return nil }
		return s, gh, started
	}
	ctx := context.Background()
	writeFile := func(t *testing.T, p, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o755); err != nil { // #nosec G306 -- a stand-in app binary.
			t.Fatal(err)
		}
	}
	readFile := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	t.Run("linux replaces the binary and starts it", func(t *testing.T) {
		exe := filepath.Join(t.TempDir(), "Bikasend")
		writeFile(t, exe, "old")
		s, _, started := setup(t, "linux", exe, makeZip(t, zipEntry{"Bikasend", 0o755, "new"}))
		res, err := s.DownloadUpdate(ctx)
		if err != nil || !res.Installable || res.Target != exe || res.GitHubSHA256 != res.SHA256 || res.Reason != "" {
			t.Fatalf("download = %+v, %v", res, err)
		}
		if err := s.InstallUpdate(ctx); err != nil {
			t.Fatal(err)
		}
		if readFile(exe) != "new" || len(*started) != 1 || (*started)[0] != "linux "+exe {
			t.Errorf("binary %q, started %v", readFile(exe), *started)
		}
		if fi, _ := os.Stat(exe); runtime.GOOS != "windows" && fi.Mode()&0o100 == 0 {
			t.Errorf("the new binary is not executable: %v", fi.Mode())
		}
		if left, _ := os.ReadDir(filepath.Dir(exe)); len(left) != 1 {
			t.Errorf("install left %d files next to the app", len(left))
		}
		if err := s.InstallUpdate(ctx); !errors.Is(err, bikasend.ErrUpdate) {
			t.Errorf("a second install without a new download must fail: %v", err)
		}
	})

	t.Run("windows moves the running exe aside", func(t *testing.T) {
		exe := filepath.Join(t.TempDir(), "Bikasend.exe")
		writeFile(t, exe, "old")
		s, _, _ := setup(t, "windows", exe, makeZip(t, zipEntry{"Bikasend.exe", 0o755, "new"}))
		if res, err := s.DownloadUpdate(ctx); err != nil || !res.Installable {
			t.Fatalf("download = %+v, %v", res, err)
		}
		if err := s.InstallUpdate(ctx); err != nil {
			t.Fatal(err)
		}
		if readFile(exe) != "new" || readFile(exe+".old") != "old" {
			t.Errorf("exe %q, old %q", readFile(exe), readFile(exe+".old"))
		}
		s.removeOldExe()
		if _, err := os.Stat(exe + ".old"); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the next start must remove the old exe: %v", err)
		}
	})

	t.Run("macOS swaps the bundle", func(t *testing.T) {
		apps := t.TempDir()
		bundle := filepath.Join(apps, "Bikasend.app")
		writeFile(t, filepath.Join(bundle, "Contents", "MacOS", "Bikasend"), "old")
		writeFile(t, filepath.Join(bundle, "Contents", "Stale.txt"), "gone")
		zipData := makeZip(t,
			zipEntry{"Bikasend.app/", os.ModeDir | 0o755, ""},
			zipEntry{"Bikasend.app/Contents/Info.plist", 0o644, "plist"},
			zipEntry{"Bikasend.app/Contents/MacOS/Bikasend", 0o755, "new"})
		s, _, started := setup(t, "darwin", filepath.Join(bundle, "Contents", "MacOS", "Bikasend"), zipData)
		if res, err := s.DownloadUpdate(ctx); err != nil || !res.Installable || res.Target != bundle {
			t.Fatalf("download = %+v, %v", res, err)
		}
		if err := s.InstallUpdate(ctx); err != nil {
			t.Fatal(err)
		}
		if readFile(filepath.Join(bundle, "Contents", "MacOS", "Bikasend")) != "new" || readFile(filepath.Join(bundle, "Contents", "Info.plist")) != "plist" {
			t.Error("the bundle was not replaced")
		}
		if _, err := os.Stat(filepath.Join(bundle, "Contents", "Stale.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Error("files of the old bundle must go")
		}
		if left, _ := os.ReadDir(apps); len(left) != 1 || (*started)[0] != "darwin "+bundle {
			t.Errorf("left %d entries, started %v", len(left), *started)
		}
	})

	t.Run("GitHub disagrees", func(t *testing.T) {
		exe := filepath.Join(t.TempDir(), "Bikasend")
		writeFile(t, exe, "old")
		s, gh, _ := setup(t, "linux", exe, makeZip(t, zipEntry{"Bikasend", 0o755, "new"}))
		gh.rel.Assets[0].Digest = "sha256:" + strings.Repeat("0", 64)
		if _, err := s.DownloadUpdate(ctx); !errors.Is(err, bikasend.ErrUpdate) {
			t.Errorf("a GitHub mismatch must fail: %v", err)
		}
		if left, _ := os.ReadDir(s.opts.DownloadDir); len(left) != 0 {
			t.Error("a download GitHub disagrees with must be removed")
		}
		if err := s.InstallUpdate(ctx); !errors.Is(err, bikasend.ErrUpdate) {
			t.Errorf("nothing may install: %v", err)
		}
	})

	for _, tt := range []struct {
		name  string
		spoil func(t *testing.T, s *Service, gh *fakeReleases, exe string)
	}{
		{name: "GitHub unreachable", spoil: func(_ *testing.T, _ *Service, gh *fakeReleases, _ string) { gh.err = errors.New("offline") }},
		{name: "a draft on GitHub", spoil: func(_ *testing.T, _ *Service, gh *fakeReleases, _ string) { gh.rel.Draft = true }},
		{name: "no digest on GitHub", spoil: func(_ *testing.T, _ *Service, gh *fakeReleases, _ string) { gh.rel.Assets[0].Digest = "" }},
		{name: "no GitHub repo", spoil: func(_ *testing.T, s *Service, _ *fakeReleases, _ string) { s.opts.GitHub = nil }},
		{name: "macOS outside a bundle", spoil: func(_ *testing.T, s *Service, _ *fakeReleases, _ string) {
			s.opts.GOOS, plat = "darwin", platformNames["darwin"]
		}},
		{name: "no running app", spoil: func(_ *testing.T, s *Service, _ *fakeReleases, _ string) {
			s.opts.Executable = func() (string, error) { return "", errors.New("unknown") }
		}},
	} {
		t.Run(tt.name+" saves but does not install", func(t *testing.T) {
			exe := filepath.Join(t.TempDir(), "Bikasend")
			writeFile(t, exe, "old")
			s, gh, started := setup(t, "linux", exe, makeZip(t, zipEntry{"Bikasend", 0o755, "new"}))
			tt.spoil(t, s, gh, exe)
			res, err := s.DownloadUpdate(ctx)
			if err != nil || res.Installable || res.Reason == "" || readFile(res.Path) == "" {
				t.Fatalf("download = %+v, %v", res, err)
			}
			if err := s.InstallUpdate(ctx); !errors.Is(err, bikasend.ErrUpdate) || readFile(exe) != "old" || len(*started) != 0 {
				t.Errorf("install = %v, binary %q, started %v", err, readFile(exe), *started)
			}
		})
	}

	t.Run("a zip changed after the check", func(t *testing.T) {
		exe := filepath.Join(t.TempDir(), "Bikasend")
		writeFile(t, exe, "old")
		s, _, _ := setup(t, "linux", exe, makeZip(t, zipEntry{"Bikasend", 0o755, "new"}))
		res, err := s.DownloadUpdate(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(res.Path, makeZip(t, zipEntry{"Bikasend", 0o755, "evil"}), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.InstallUpdate(ctx); !errors.Is(err, bikasend.ErrUpdate) || readFile(exe) != "old" {
			t.Errorf("install = %v, binary %q", err, readFile(exe))
		}
	})
}

func TestInstallHostileZip(t *testing.T) {
	app := func(rest ...zipEntry) []zipEntry {
		return append([]zipEntry{{"Bikasend.app/Contents/MacOS/Bikasend", 0o755, "new"}}, rest...)
	}
	many := make([]zipEntry, maxInstallFiles+1)
	for i := range many {
		many[i] = zipEntry{fmt.Sprintf("Bikasend.app/f%d", i), 0o644, ""}
	}
	tests := []struct {
		name    string
		bundle  bool
		entries []zipEntry
		max     int64
	}{
		{name: "two files for one binary", entries: []zipEntry{{"Bikasend", 0o755, "new"}, {"extra", 0o644, ""}}},
		{name: "the wrong binary name", entries: []zipEntry{{"evil", 0o755, "new"}}},
		{name: "a directory as the binary", entries: []zipEntry{{"Bikasend/", os.ModeDir | 0o755, ""}}},
		{name: "a binary over the cap", entries: []zipEntry{{"Bikasend", 0o755, "too big"}}, max: 3},
		{name: "a path out of the bundle", bundle: true, entries: app(zipEntry{"Bikasend.app/../evil", 0o644, "x"})},
		{name: "an absolute path", bundle: true, entries: app(zipEntry{"/tmp/evil", 0o644, "x"})},
		{name: "a backslash path", bundle: true, entries: app(zipEntry{`Bikasend.app\..\evil`, 0o644, "x"})},
		{name: "a file beside the bundle", bundle: true, entries: app(zipEntry{"Other.app/x", 0o644, "x"})},
		{name: "a symlink", bundle: true, entries: app(zipEntry{"Bikasend.app/Contents/link", os.ModeSymlink | 0o777, "/etc/passwd"})},
		{name: "no app binary", bundle: true, entries: []zipEntry{{"Bikasend.app/Contents/Info.plist", 0o644, "x"}}},
		{name: "too many files", bundle: true, entries: many},
		{name: "a bundle over the cap", bundle: true, entries: app(zipEntry{"Bikasend.app/big", 0o644, "0123456789"}), max: 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.max > 0 {
				defer func(old int64) { maxInstall = old }(maxInstall)
				maxInstall = tt.max
			}
			data := makeZip(t, tt.entries...)
			zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			target := filepath.Join(dir, "Bikasend")
			if tt.bundle {
				target = filepath.Join(dir, "Bikasend.app")
				err = os.MkdirAll(filepath.Join(target, "Contents", "MacOS"), 0o755)
			}
			if err == nil {
				err = os.WriteFile(filepath.Join(dir, "marker"), []byte("old"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.bundle {
				err = installBundle(zr, target)
			} else {
				err = installExe(zr, target, "Bikasend", false)
			}
			if err == nil {
				t.Fatal("a hostile zip must not install")
			}
			left, _ := os.ReadDir(dir)
			want := 1
			if tt.bundle {
				want = 2
			}
			if len(left) != want {
				t.Errorf("a refused install left %d entries in %s", len(left), dir)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil")); err == nil {
				t.Error("a zip wrote outside the app folder")
			}
		})
	}
}
