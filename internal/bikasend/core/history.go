package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
	"github.com/BikaSolution/bikasend/pkg/sdk/site"
)

func (s *Service) addHistory(r bikasend.Request, env string, out bikasend.Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID, r.AllowInsecure = "", false
	entry := bikasend.HistoryEntry{Time: time.Now().Format(time.RFC3339), Environment: env, Request: r, Status: out.StatusCode, DurationMs: out.DurationMs}
	s.history = append([]bikasend.HistoryEntry{entry}, s.history...)
	if len(s.history) > maxHistory {
		s.history = s.history[:maxHistory]
	}
	if s.state.Settings.SaveHistory {
		s.saveHistory()
	}
}

func (s *Service) History() []bikasend.HistoryEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]bikasend.HistoryEntry, len(s.history))
	copy(out, s.history)
	return out
}

func (s *Service) ClearHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = []bikasend.HistoryEntry{}
	err := os.Remove(filepath.Join(s.opts.ConfigDir, "history.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Service) loadHistory() {
	if !s.state.Settings.SaveHistory {
		return
	}
	data, err := os.ReadFile(filepath.Join(s.opts.ConfigDir, "history.json"))
	if err != nil {
		return
	}
	var h []bikasend.HistoryEntry
	if json.Unmarshal(data, &h) == nil {
		s.history = h
	}
}

// saveHistory writes history with every secret-looking value masked; templates like {{api_key}} stay.
func (s *Service) saveHistory() {
	scrubbed := make([]bikasend.HistoryEntry, len(s.history))
	for i, h := range s.history {
		h.Request = scrubRequest(h.Request)
		scrubbed[i] = h
	}
	data, err := json.Marshal(scrubbed)
	if err != nil || s.opts.ConfigDir == "" {
		return
	}
	if os.MkdirAll(s.opts.ConfigDir, 0o700) == nil {
		_ = writeAtomic(filepath.Join(s.opts.ConfigDir, "history.json"), data)
	}
}

func scrubValue(name, value string) string {
	// A value built from {{variables}} is a template, not a secret.
	if secretName(name) && value != "" && !varRef.MatchString(value) {
		return maskText
	}
	return value
}

func scrubRequest(r bikasend.Request) bikasend.Request {
	var lines []string
	for line := range strings.Lines(r.Headers) {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r\n"), ":")
		if ok {
			v = " " + scrubValue(k, strings.TrimSpace(v))
		}
		lines = append(lines, k+map[bool]string{true: ":", false: ""}[ok]+v)
	}
	r.Headers = strings.Join(lines, "\n")
	r.URL = scrubURL(r.URL)
	scrubKV := func(in []bikasend.KV) []bikasend.KV {
		out := make([]bikasend.KV, len(in))
		for i, kv := range in {
			kv.Value = scrubValue(kv.Name, kv.Value)
			out[i] = kv
		}
		return out
	}
	r.Query, r.PathParams = scrubKV(r.Query), scrubKV(r.PathParams)
	form := make([]bikasend.Field, len(r.Form))
	for i, f := range r.Form {
		if !f.File {
			f.Value = scrubValue(f.Name, f.Value)
		}
		form[i] = f
	}
	r.Form = form
	switch r.BodyType {
	case "json":
		var v any
		if json.Unmarshal([]byte(r.Body), &v) != nil {
			r.Body = scrubBody(r.Body)
			break
		}
		if b, err := json.MarshalIndent(scrubJSON(v), "", "  "); err == nil {
			r.Body = string(b)
		}
	case "text", "xml":
		r.Body = scrubBody(r.Body)
	}
	return r
}

// scrubBody keeps free text off disk: it can hold a pasted secret that no name marks.
func scrubBody(body string) string {
	if strings.TrimSpace(varRef.ReplaceAllString(body, "")) == "" {
		return body
	}
	return maskText
}

// scrubURL masks query values whose names look secret, as in ?token=…
func scrubURL(raw string) string {
	base, query, ok := strings.Cut(raw, "?")
	if !ok {
		return raw
	}
	fragment := ""
	if q, f, hasFragment := strings.Cut(query, "#"); hasFragment {
		query, fragment = q, "#"+f
	}
	parts := strings.Split(query, "&")
	for i, p := range parts {
		name, value, hasValue := strings.Cut(p, "=")
		if !hasValue {
			continue
		}
		decoded, err := url.QueryUnescape(name)
		if err != nil {
			decoded = name
		}
		if v := scrubValue(decoded, value); v != value {
			parts[i] = name + "=" + v
		}
	}
	return base + "?" + strings.Join(parts, "&") + fragment
}

func scrubJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if s, ok := val.(string); ok {
				t[k] = scrubValue(k, s)
			} else {
				t[k] = scrubJSON(val)
			}
		}
	case []any:
		for i := range t {
			t[i] = scrubJSON(t[i])
		}
	}
	return v
}

// CheckUpdate asks the download site for the newest release. A failure is never fatal to the app.
func (s *Service) CheckUpdate(ctx context.Context) (bikasend.Update, error) {
	s.mu.Lock()
	enabled := s.state.Settings.CheckUpdates
	s.mu.Unlock()
	up := bikasend.Update{Current: s.opts.Version}
	if !enabled || s.opts.UpdateURL == "" || s.opts.Client == nil {
		return up, nil
	}
	latest, err := s.latestRelease(ctx)
	if err != nil {
		return up, err
	}
	up.Latest = latest.Tag
	up.Available = newer(latest.Tag, s.opts.Version)
	up.URL = strings.TrimRight(s.opts.UpdateURL, "/") + "/#download"
	return up, nil
}

// updateClient copies the app's client with a redirect rule: the update site itself, and with
// github true also GitHub's download hosts over https.
func (s *Service) updateClient(base *url.URL, timeout time.Duration, github bool) *http.Client {
	c := *s.opts.Client
	c.Timeout = timeout
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme == base.Scheme && req.URL.Host == base.Host {
			return nil
		}
		h := req.URL.Hostname()
		if github && req.URL.Scheme == "https" && (h == "github.com" || strings.HasSuffix(h, ".githubusercontent.com")) {
			return nil
		}
		return fmt.Errorf("redirect to %s refused", req.URL.Host)
	}
	return &c
}

func (s *Service) latestRelease(ctx context.Context) (site.LatestRelease, error) {
	var latest site.LatestRelease
	base, err := url.Parse(s.opts.UpdateURL)
	if err != nil || base.Host == "" {
		return latest, fmt.Errorf("%w: bad update site %q", bikasend.ErrUpdate, s.opts.UpdateURL)
	}
	u, err := url.JoinPath(s.opts.UpdateURL, "latest.json")
	if err != nil {
		return latest, errors.Join(bikasend.ErrUpdate, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return latest, errors.Join(bikasend.ErrUpdate, err)
	}
	resp, err := s.updateClient(base, 10*time.Second, false).Do(req)
	if err != nil {
		return latest, errors.Join(bikasend.ErrUpdate, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return latest, fmt.Errorf("%w: %s", bikasend.ErrUpdate, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&latest); err != nil {
		return latest, errors.Join(bikasend.ErrUpdate, err)
	}
	return latest, nil
}

var (
	// maxUpdate caps a downloaded build; a var so a test can cross it.
	maxUpdate     int64 = 300 << 20
	updateName          = regexp.MustCompile(`^Bikasend-v[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}-[a-z0-9-]{1,40}\.zip$`)
	sha256Hex           = regexp.MustCompile(`^[0-9a-f]{64}$`)
	platformNames       = map[string]string{"darwin": "macOS", "windows": "Windows", "linux": "Linux"}
)

// DownloadUpdate saves this platform's newest build to the download folder after checking its
// SHA-256 against the update site's latest.json. It returns the saved file's path.
func (s *Service) DownloadUpdate(ctx context.Context) (string, error) {
	if s.opts.UpdateURL == "" || s.opts.Client == nil {
		return "", fmt.Errorf("%w: this build has no update site", bikasend.ErrUpdate)
	}
	base, err := url.Parse(s.opts.UpdateURL)
	if err != nil || base.Host == "" {
		return "", fmt.Errorf("%w: bad update site %q", bikasend.ErrUpdate, s.opts.UpdateURL)
	}
	latest, err := s.latestRelease(ctx)
	if err != nil {
		return "", err
	}
	if !newer(latest.Tag, s.opts.Version) {
		return "", fmt.Errorf("%w: %s is not newer than this build (%s)", bikasend.ErrUpdate, latest.Tag, s.opts.Version)
	}
	var dl *site.LatestDownload
	for i, d := range latest.Downloads {
		if d.Platform == platformNames[runtime.GOOS] {
			dl = &latest.Downloads[i]
			break
		}
	}
	if dl == nil {
		return "", fmt.Errorf("%w: %s has no build for %s", bikasend.ErrUpdate, latest.Tag, runtime.GOOS)
	}
	// Every field comes from the network: the name becomes a file name, the URL a request.
	ref, err := url.Parse(dl.URL)
	if !updateName.MatchString(dl.Name) || !strings.HasPrefix(dl.Name, "Bikasend-"+latest.Tag+"-") || !sha256Hex.MatchString(dl.SHA256) || err != nil || ref.Scheme != "" || ref.Host != "" || !strings.HasPrefix(ref.Path, "/downloads/") {
		return "", fmt.Errorf("%w: latest.json has an unexpected download entry", bikasend.ErrUpdate)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.ResolveReference(ref).String(), nil)
	if err != nil {
		return "", errors.Join(bikasend.ErrUpdate, err)
	}
	resp, err := s.updateClient(base, 10*time.Minute, true).Do(req)
	if err != nil {
		return "", errors.Join(bikasend.ErrUpdate, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: download: %s", bikasend.ErrUpdate, resp.Status)
	}

	dir := s.downloadDir()
	tmp, err := os.CreateTemp(dir, ".bikasend-update-*")
	if err != nil {
		return "", errors.Join(bikasend.ErrUpdate, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(resp.Body, maxUpdate+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", errors.Join(bikasend.ErrUpdate, err)
	}
	if n > maxUpdate {
		return "", fmt.Errorf("%w: the download is larger than %d MB", bikasend.ErrUpdate, maxUpdate>>20)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != dl.SHA256 {
		return "", fmt.Errorf("%w: SHA-256 mismatch: got %s, want %s", bikasend.ErrUpdate, got, dl.SHA256)
	}
	// Link fails when the name exists, so a file already there is never replaced, even in a race.
	for i := range 100 {
		dest := filepath.Join(dir, dl.Name)
		if i > 0 {
			dest = filepath.Join(dir, strings.TrimSuffix(dl.Name, ".zip")+"-"+strconv.Itoa(i)+".zip")
		}
		err := os.Link(tmp.Name(), dest)
		if err == nil {
			return dest, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", errors.Join(bikasend.ErrUpdate, err)
		}
	}
	return "", fmt.Errorf("%w: too many files named %s in %s", bikasend.ErrUpdate, dl.Name, dir)
}

func (s *Service) downloadDir() string {
	if s.opts.DownloadDir != "" {
		return s.opts.DownloadDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	if fi, err := os.Stat(filepath.Join(home, "Downloads")); err == nil && fi.IsDir() {
		return filepath.Join(home, "Downloads")
	}
	return home
}

// newer reports whether tag a is a higher vX.Y.Z than b. A dev build never sees updates.
func newer(a, b string) bool {
	pa, okA := semver(a)
	pb, okB := semver(b)
	if !okA || !okB {
		return false
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func semver(v string) ([3]int, bool) {
	var out [3]int
	v, _, _ = strings.Cut(strings.TrimPrefix(v, "v"), "-")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
