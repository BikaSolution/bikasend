package core

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
	"github.com/BikaSolution/bikasend/pkg/sdk/site"
	"github.com/BikaSolution/bikasend/pkg/sign"
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
	releaseTag          = regexp.MustCompile(`^v[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$`)
	platformNames       = map[string]string{"darwin": "macOS", "windows": "Windows", "linux": "Linux"}
)

// DownloadUpdate saves this platform's newest build to the download folder after checking its
// SHA-256 against the update site's latest.json. It returns the saved file's path.
func (s *Service) DownloadUpdate(ctx context.Context) (bikasend.UpdateDownload, error) {
	var out bikasend.UpdateDownload
	s.mu.Lock()
	s.pending = nil
	s.mu.Unlock()
	if s.opts.UpdateURL == "" || s.opts.Client == nil {
		return out, fmt.Errorf("%w: this build has no update site", bikasend.ErrUpdate)
	}
	base, err := url.Parse(s.opts.UpdateURL)
	if err != nil || base.Host == "" {
		return out, fmt.Errorf("%w: bad update site %q", bikasend.ErrUpdate, s.opts.UpdateURL)
	}
	latest, err := s.latestRelease(ctx)
	if err != nil {
		return out, err
	}
	if !newer(latest.Tag, s.opts.Version) {
		return out, fmt.Errorf("%w: %s is not newer than this build (%s)", bikasend.ErrUpdate, latest.Tag, s.opts.Version)
	}
	var dl *site.LatestDownload
	for i, d := range latest.Downloads {
		if d.Platform == platformNames[s.goos()] {
			dl = &latest.Downloads[i]
			break
		}
	}
	if dl == nil {
		return out, fmt.Errorf("%w: %s has no build for %s", bikasend.ErrUpdate, latest.Tag, s.goos())
	}
	// Every field comes from the network: the name becomes a file name, the URL a request.
	ref, err := url.Parse(dl.URL)
	if !updateName.MatchString(dl.Name) || !strings.HasPrefix(dl.Name, "Bikasend-"+latest.Tag+"-") || !sha256Hex.MatchString(dl.SHA256) || err != nil || ref.Scheme != "" || ref.Host != "" || !strings.HasPrefix(ref.Path, "/downloads/") {
		return out, fmt.Errorf("%w: latest.json has an unexpected download entry", bikasend.ErrUpdate)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.ResolveReference(ref).String(), nil)
	if err != nil {
		return out, errors.Join(bikasend.ErrUpdate, err)
	}
	resp, err := s.updateClient(base, 10*time.Minute, true).Do(req)
	if err != nil {
		return out, errors.Join(bikasend.ErrUpdate, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("%w: download: %s", bikasend.ErrUpdate, resp.Status)
	}

	dir := s.downloadDir()
	tmp, err := os.CreateTemp(dir, ".bikasend-update-*")
	if err != nil {
		return out, errors.Join(bikasend.ErrUpdate, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(resp.Body, maxUpdate+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return out, errors.Join(bikasend.ErrUpdate, err)
	}
	if n > maxUpdate {
		return out, fmt.Errorf("%w: the download is larger than %d MB", bikasend.ErrUpdate, maxUpdate>>20)
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if got != dl.SHA256 {
		return out, fmt.Errorf("%w: SHA-256 mismatch: got %s, want %s", bikasend.ErrUpdate, got, dl.SHA256)
	}
	dest, err := linkNew(tmp.Name(), dir, dl.Name)
	if err != nil {
		return out, err
	}
	out = bikasend.UpdateDownload{Tag: latest.Tag, Path: dest, SHA256: got, SiteSHA256: dl.SHA256}

	// GitHub is a second source for the hash: a hacked update site alone cannot pass both checks.
	// An unconfirmed zip is deleted: saved by the app, it would skip Gatekeeper and SmartScreen.
	gh, err := s.githubDigest(ctx, latest.Tag, dl.Name)
	if err == nil && gh != got {
		err = fmt.Errorf("GitHub lists SHA-256 %s, the download has %s", gh, got)
	}
	if err != nil {
		_ = os.Remove(dest)
		return bikasend.UpdateDownload{}, fmt.Errorf("%w: GitHub did not confirm %s (%w); the file was removed, try again later or use the download page", bikasend.ErrUpdate, dl.Name, err)
	}
	out.GitHubSHA256 = gh
	// The release key is offline with the maintainer: GitHub and the update site together cannot sign.
	if err := s.checkSignature(ctx, latest.Tag, dl.Name, got); err != nil {
		_ = os.Remove(dest)
		return bikasend.UpdateDownload{}, fmt.Errorf("%w: %s is not signed with the release key (%w); the file was removed", bikasend.ErrUpdate, dl.Name, err)
	}
	out.Signed = true
	out.Target, err = s.installTarget()
	if err != nil {
		out.Reason = err.Error()
		return out, nil
	}
	out.Installable = true
	s.mu.Lock()
	s.pending = &pendingUpdate{path: dest, sha256: got, target: out.Target}
	s.mu.Unlock()
	return out, nil
}

// checkSignature fetches the release's SHA256SUMS and its signature from GitHub, checks the
// signature with the key built into the app, and finds name with sha in the list.
func (s *Service) checkSignature(ctx context.Context, tag, name, sha string) error {
	owner, repo, ok := strings.Cut(s.opts.Repo, "/")
	if len(s.opts.ReleaseKey) != ed25519.PublicKeySize || !ok || owner == "" || repo == "" {
		return errors.New("this build has no release key")
	}
	if !releaseTag.MatchString(tag) {
		return fmt.Errorf("unexpected tag %s", quoteStart(tag))
	}
	web := s.opts.GitHubWeb
	if web == "" {
		web = "https://github.com"
	}
	base, err := url.Parse(web)
	if err != nil || base.Host == "" {
		return fmt.Errorf("bad GitHub address %q", web)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sumsName := "Bikasend-" + tag + "-SHA256SUMS"
	fetch := func(file string) ([]byte, error) {
		u := base.JoinPath(url.PathEscape(owner), url.PathEscape(repo), "releases", "download", url.PathEscape(tag), url.PathEscape(file))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := s.updateClient(base, 30*time.Second, true).Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: %s", file, resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, maxSignedFile))
	}
	sums, err := fetch(sumsName)
	if err != nil {
		return err
	}
	sig, err := fetch(sumsName + ".sig")
	if err != nil {
		return err
	}
	if err := sign.Verify(s.opts.ReleaseKey, sums, sig); err != nil {
		return err
	}
	list, err := sign.ParseSums(sums)
	if err != nil {
		return err
	}
	if list[name] != sha {
		return fmt.Errorf("the signed list does not hold %s with this SHA-256", name)
	}
	return nil
}

// linkNew links tmp into dir under name, or name-1, name-2 …; Link never replaces a file, even in a race.
func linkNew(tmp, dir, name string) (string, error) {
	for i := range 100 {
		dest := filepath.Join(dir, name)
		if i > 0 {
			dest = filepath.Join(dir, strings.TrimSuffix(name, ".zip")+"-"+strconv.Itoa(i)+".zip")
		}
		err := os.Link(tmp, dest)
		if err == nil {
			return dest, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", errors.Join(bikasend.ErrUpdate, err)
		}
	}
	return "", fmt.Errorf("%w: too many files named %s in %s", bikasend.ErrUpdate, name, dir)
}

func (s *Service) githubDigest(ctx context.Context, tag, name string) (string, error) {
	if s.opts.GitHub == nil {
		return "", errors.New("this build does not name its GitHub repo")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	rel, err := s.opts.GitHub.ReleaseByTag(ctx, tag)
	if err != nil {
		return "", err
	}
	if rel.Draft || rel.Prerelease || rel.TagName != tag {
		return "", fmt.Errorf("GitHub has no published release %s", tag)
	}
	for _, a := range rel.Assets {
		if a.Name == name {
			hash, ok := strings.CutPrefix(a.Digest, "sha256:")
			if !ok || !sha256Hex.MatchString(hash) {
				return "", fmt.Errorf("GitHub lists no SHA-256 for %s", name)
			}
			return hash, nil
		}
	}
	return "", fmt.Errorf("release %s on GitHub has no file %s", tag, name)
}

type pendingUpdate struct {
	path, sha256, target string
}

// rename is a variable so tests can make the swap fail.
var rename = os.Rename

// maxSignedFile caps SHA256SUMS and its signature; sign.ParseSums allows 64 KB.
const maxSignedFile = 65 << 10

// maxInstall caps the unpacked app; the zip itself is capped by maxUpdate.
var maxInstall int64 = 200 << 20

const maxInstallFiles = 2_000

func (s *Service) goos() string {
	if s.opts.GOOS != "" {
		return s.opts.GOOS
	}
	return runtime.GOOS
}

// installTarget is the file (Linux, Windows) or .app bundle (macOS) an update replaces.
func (s *Service) installTarget() (string, error) {
	executable := s.opts.Executable
	if executable == nil {
		executable = os.Executable
	}
	exe, err := executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "", fmt.Errorf("cannot find the running app: %w", err)
	}
	target := exe
	if s.goos() == "darwin" {
		target = filepath.Dir(filepath.Dir(filepath.Dir(exe)))
		if filepath.Ext(target) != ".app" || filepath.Base(filepath.Dir(exe)) != "MacOS" || filepath.Base(filepath.Dir(filepath.Dir(exe))) != "Contents" {
			return "", errors.New("the app is not running from a .app bundle")
		}
	}
	probe, err := os.CreateTemp(filepath.Dir(target), ".bikasend-check-*")
	if err != nil {
		return "", fmt.Errorf("cannot write next to %s: %w", target, err)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return target, nil
}

// InstallUpdate unpacks the zip DownloadUpdate checked into place and starts the new app.
// The api layer quits the old one.
func (s *Service) InstallUpdate(context.Context) error {
	s.mu.Lock()
	p := s.pending
	s.pending = nil
	s.mu.Unlock()
	if p == nil {
		return fmt.Errorf("%w: download and check an update first", bikasend.ErrUpdate)
	}
	if err := install(s.goos(), p); err != nil {
		s.mu.Lock()
		if s.pending == nil {
			s.pending = p
		}
		s.mu.Unlock()
		return errors.Join(bikasend.ErrUpdate, err)
	}
	start := s.opts.Start
	if start == nil {
		start = startApp
	}
	if err := start(s.goos(), p.target); err != nil {
		return fmt.Errorf("%w: the update is installed but did not start; start Bikasend again: %w", bikasend.ErrUpdate, err)
	}
	return nil
}

func install(goos string, p *pendingUpdate) error {
	f, err := os.Open(p.path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxUpdate {
		return fmt.Errorf("the download is larger than %d MB", maxUpdate>>20)
	}
	// Hash the open file again and unpack from the same handle: the zip sat in Downloads meanwhile.
	sum := sha256.New()
	if _, err := io.Copy(sum, io.NewSectionReader(f, 0, info.Size())); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != p.sha256 {
		return fmt.Errorf("%s changed after it was checked", p.path)
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return err
	}
	switch goos {
	case "darwin":
		return installBundle(zr, p.target)
	case "windows":
		return installExe(zr, p.target, "Bikasend.exe", true)
	default:
		return installExe(zr, p.target, "Bikasend", false)
	}
}

// installExe swaps in the one binary the zip holds. Windows cannot overwrite a running .exe but
// can rename it, so the old one moves to .old and the next start removes it.
func installExe(zr *zip.Reader, target, name string, windows bool) error {
	if len(zr.File) != 1 || zr.File[0].Name != name || !zr.File[0].Mode().IsRegular() {
		return fmt.Errorf("the zip must hold only %s", name)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".bikasend-new-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = unzipFile(zr.File[0], tmp, maxInstall)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if !windows {
		if err := os.Chmod(tmp.Name(), 0o755); err != nil { // #nosec G302 -- the app binary must be executable.
			return err
		}
		return rename(tmp.Name(), target)
	}
	old := target + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := rename(target, old); err != nil {
		return err
	}
	if err := rename(tmp.Name(), target); err != nil {
		if rerr := rename(old, target); rerr != nil {
			return fmt.Errorf("%w; the previous app is at %s", err, old)
		}
		return err
	}
	return nil
}

// installBundle unpacks Bikasend.app next to the running bundle through os.Root, then swaps the two.
func installBundle(zr *zip.Reader, bundle string) error {
	if len(zr.File) > maxInstallFiles {
		return fmt.Errorf("the zip has more than %d files", maxInstallFiles)
	}
	stage, err := os.MkdirTemp(filepath.Dir(bundle), ".bikasend-new-*")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(stage)
		}
	}()
	root, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	left := maxInstall
	for _, zf := range zr.File {
		name := strings.TrimSuffix(zf.Name, "/")
		if !fs.ValidPath(name) || strings.ContainsAny(name, `\:`) || (name != "Bikasend.app" && !strings.HasPrefix(name, "Bikasend.app/")) {
			return fmt.Errorf("the zip holds an unexpected path %s", quoteStart(zf.Name))
		}
		mode := zf.Mode()
		switch {
		case mode.IsDir():
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
		case mode.IsRegular():
			if err := root.MkdirAll(path.Dir(name), 0o755); err != nil {
				return err
			}
			perm := os.FileMode(0o644)
			if mode&0o111 != 0 {
				perm = 0o755
			}
			out, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
			if err != nil {
				return err
			}
			n, err := unzipFile(zf, out, left)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			left -= n
		default:
			return fmt.Errorf("the zip holds a link or special file %s", quoteStart(zf.Name))
		}
	}
	if fi, err := root.Stat("Bikasend.app/Contents/MacOS/Bikasend"); err != nil || !fi.Mode().IsRegular() {
		return errors.New("the zip holds no Bikasend.app/Contents/MacOS/Bikasend")
	}
	old := filepath.Join(stage, "previous.app")
	if err := rename(bundle, old); err != nil {
		return err
	}
	if err := rename(filepath.Join(stage, "Bikasend.app"), bundle); err != nil {
		if rerr := rename(old, bundle); rerr != nil {
			keep = true
			return fmt.Errorf("%w; the previous app is at %s", err, old)
		}
		return err
	}
	return nil
}

// unzipFile copies one entry, trusting neither the size the zip claims nor the data's length.
func unzipFile(zf *zip.File, w io.Writer, limit int64) (int64, error) {
	size := zf.FileInfo().Size()
	if size < 0 || size > limit {
		return 0, fmt.Errorf("the app is larger than %d MB unpacked", maxInstall>>20)
	}
	r, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer func() { _ = r.Close() }()
	n, err := io.Copy(w, io.LimitReader(r, size+1))
	if err != nil {
		return n, err
	}
	if n != size {
		return n, errors.New("a file in the zip has the wrong size")
	}
	return n, nil
}

// startApp starts the installed app on its own; it outlives this process.
func startApp(goos, target string) error {
	// Background: the new app must outlive this one.
	cmd := exec.CommandContext(context.Background(), target) // #nosec G204 -- target is this app's own path from os.Executable.
	if goos == "darwin" {
		cmd = exec.CommandContext(context.Background(), "/usr/bin/open", "-n", target) // #nosec G204 -- fixed tool; target is this app's bundle.
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// removeOldExe deletes the .exe a Windows update left behind.
func (s *Service) removeOldExe() {
	if s.goos() != "windows" {
		return
	}
	executable := s.opts.Executable
	if executable == nil {
		executable = os.Executable
	}
	exe, err := executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err == nil {
		_ = os.Remove(exe + ".old")
	}
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
