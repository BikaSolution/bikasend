package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BikaSolution/bikasend/pkg/awscli"
	"github.com/BikaSolution/bikasend/pkg/sdk/bikasend"
	"github.com/BikaSolution/bikasend/pkg/sigv4"
)

const (
	maxBody       = 10 << 20
	maxUpload     = 50 << 20
	maxTLSFile    = 1 << 20
	maxHistory    = 200
	defaultTimout = 2 * time.Minute
)

var pathParam = regexp.MustCompile(`\{[A-Za-z0-9_.-]+\}`)

// awsRegion builds a console host name, so it must look like an AWS region and nothing else.
var awsRegion = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)

type carriesSecretKey struct{}

type built struct {
	req     *http.Request
	body    []byte
	exp     *expansion
	env     *environment
	envName string
	preview bikasend.Preview
}

// build resolves every template in r. It never fails on missing variables; the caller decides.
func (s *Service) build(ctx context.Context, r bikasend.Request) (*built, error) {
	exp := newExpansion(s.lookup(r.Server))
	b := &built{exp: exp, env: s.envConfig(s.env), envName: s.env}
	method := strings.ToUpper(strings.TrimSpace(r.Method))
	if method == "" {
		method = http.MethodGet
	}

	rawURL := strings.TrimSpace(exp.expand(r.URL))
	for _, p := range r.PathParams {
		if v := exp.expand(p.Value); v != "" {
			rawURL = strings.ReplaceAll(rawURL, "{"+p.Name+"}", url.PathEscape(v))
		}
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return b, fmt.Errorf("%w: %q is not an http or https URL", bikasend.ErrInvalidRequest, exp.mask(rawURL))
	}
	extra := url.Values{}
	for _, q := range r.Query {
		if q.Enabled && strings.TrimSpace(q.Name) != "" {
			extra.Add(exp.expand(q.Name), exp.expand(q.Value))
		}
	}
	if len(extra) > 0 {
		if u.RawQuery != "" {
			u.RawQuery += "&"
		}
		u.RawQuery += extra.Encode()
	}
	if left := pathParam.FindAllString(u.Path, -1); len(left) > 0 {
		b.preview.Missing = append(b.preview.Missing, left...)
	}

	headers := http.Header{}
	host := ""
	for line := range strings.Lines(r.Headers) {
		k, v, ok := strings.Cut(line, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(exp.expand(v))
		// Unfilled template lines ("X-API-Key: ") stay out of the request.
		if !ok || k == "" || v == "" {
			continue
		}
		if strings.EqualFold(k, "Host") {
			host = v
			continue
		}
		headers.Add(k, v)
	}

	var preview string
	switch r.BodyType {
	case "json", "text", "xml":
		b.body = []byte(exp.expand(r.Body))
		preview = string(b.body)
	case "form":
		form := url.Values{}
		for _, f := range r.Form {
			if f.Enabled && f.Name != "" {
				form.Add(exp.expand(f.Name), exp.expand(f.Value))
			}
		}
		b.body = []byte(form.Encode())
		preview = string(b.body)
		if headers.Get("Content-Type") == "" {
			headers.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	case "multipart":
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		var lines []string
		for _, f := range r.Form {
			if !f.Enabled || f.Name == "" {
				continue
			}
			name, value := exp.expand(f.Name), exp.expand(f.Value)
			if !f.File {
				_ = w.WriteField(name, value)
				lines = append(lines, name+"="+value)
				continue
			}
			lines = append(lines, name+"=@"+filepath.Base(value))
			if !s.uploads[filepath.Clean(value)] {
				return b, fmt.Errorf("%w: choose the file for %q with the file picker", bikasend.ErrInvalidRequest, name)
			}
			if err := addFile(w, name, value); err != nil {
				return b, err
			}
		}
		_ = w.Close()
		b.body = buf.Bytes()
		preview = strings.Join(lines, "\n")
		headers.Set("Content-Type", w.FormDataContentType())
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(b.body))
	if err != nil {
		return b, errors.Join(bikasend.ErrInvalidRequest, err)
	}
	if len(b.body) == 0 {
		req.Body, req.ContentLength = http.NoBody, 0
	}
	req.Header = headers
	if host != "" {
		req.Host = host
	}
	b.req = req

	var hl []string
	for _, k := range sortedHeaderKeys(headers) {
		for _, v := range headers[k] {
			hl = append(hl, k+": "+v)
		}
	}
	switch r.Auth {
	case "oauth2":
		hl = append(hl, "Authorization: Bearer "+maskText+" (OAuth2)")
	case "sigv4":
		hl = append(hl, "Authorization: AWS4-HMAC-SHA256 "+maskText+" (SigV4)")
	}
	b.preview.Method, b.preview.URL = method, exp.mask(req.URL.String())
	b.preview.Headers = exp.mask(strings.Join(hl, "\n"))
	b.preview.Body = exp.mask(preview)
	b.preview.Used = exp.used
	for name := range exp.missing {
		b.preview.Missing = append(b.preview.Missing, "{{"+name+"}}")
	}
	slices.Sort(b.preview.Missing)
	b.preview.Secret = len(exp.secrets) > 0 || r.Auth != ""
	if err := exp.problem(); err != nil {
		b.preview.Error = err.Error()
	}
	return b, nil
}

func addFile(w *multipart.Writer, field, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w: file for %q: %w", bikasend.ErrInvalidRequest, field, err)
	}
	if info.Size() > maxUpload {
		return fmt.Errorf("%w: %s is larger than 50 MB", bikasend.ErrInvalidRequest, filepath.Base(path))
	}
	data, err := os.ReadFile(path) // #nosec G304 -- build accepts only paths the user picked in the file dialog.
	if err != nil {
		return fmt.Errorf("%w: %w", bikasend.ErrInvalidRequest, err)
	}
	part, err := w.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

// guard applies the secret rules: listed or confirmed hosts only, and no secrets over plain http.
func (b *built) guard(allowInsecure bool, allowHost string) error {
	if !b.preview.Secret {
		return nil
	}
	host := b.req.URL.Hostname()
	listed := b.env != nil && len(b.env.Hosts) > 0
	if listed && !matchHost(b.env.Hosts, b.req.URL.Host) && !matchHost(b.env.Hosts, host) {
		b.preview.BlockedBy = "hosts"
		return fmt.Errorf("%w: %s is not in the hosts list of environment %q", bikasend.ErrSecretHost, host, b.envName)
	}
	// The URL can come from an untrusted spec, so an unlisted host needs the user's confirmation.
	confirmed := allowHost != "" && (strings.EqualFold(allowHost, b.req.URL.Host) || strings.EqualFold(allowHost, host))
	if !listed && !isLoopback(host) && !confirmed {
		b.preview.BlockedBy = "unlisted"
		return fmt.Errorf("%w: no environment lists %s in its hosts", bikasend.ErrSecretHost, host)
	}
	if b.req.URL.Scheme == "http" && !isLoopback(host) {
		b.preview.Insecure = true
		if !allowInsecure {
			b.preview.BlockedBy = "insecure"
			return fmt.Errorf("%w: this request sends a secret to %s without TLS", bikasend.ErrInsecureSecret, host)
		}
	}
	return nil
}

func (s *Service) AllowUpload(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploads[filepath.Clean(path)] = true
}

func (s *Service) Preview(r bikasend.Request) bikasend.Preview {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.build(context.Background(), r)
	if err != nil {
		b.preview.Error = err.Error()
		return b.preview
	}
	if err := b.guard(r.AllowInsecure, r.AllowHost); err != nil {
		b.preview.Warning = err.Error()
	}
	return b.preview
}

func (s *Service) Send(ctx context.Context, r bikasend.Request) (bikasend.Response, error) {
	s.mu.Lock()
	b, err := s.build(ctx, r)
	if err == nil && len(b.preview.Missing) > 0 {
		err = b.exp.problem()
		if err == nil {
			err = fmt.Errorf("%w: fill in %s", bikasend.ErrUndefinedVariable, strings.Join(b.preview.Missing, ", "))
		}
	}
	if err == nil {
		err = b.exp.problem()
	}
	if err == nil {
		err = b.guard(r.AllowInsecure, r.AllowHost)
	}
	var client *http.Client
	if err == nil {
		client, err = s.client(b.envName, b.env)
	}
	if err != nil {
		s.mu.Unlock()
		return bikasend.Response{}, err
	}
	if r.ID != "" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		s.inflight[r.ID] = cancel
		defer func() {
			s.mu.Lock()
			delete(s.inflight, r.ID)
			s.mu.Unlock()
			cancel()
		}()
	}
	s.mu.Unlock()

	switch r.Auth {
	case "oauth2":
		token, err := s.oauthToken(ctx, b)
		if err != nil {
			return bikasend.Response{}, err
		}
		b.req.Header.Set("Authorization", "Bearer "+token)
		b.exp.secrets["$oauth2"] = token
	case "sigv4":
		if err := s.signV4(ctx, b); err != nil {
			return bikasend.Response{}, err
		}
	}

	var t bikasend.Timing
	var dnsStart, connStart, tlsStart time.Time
	start := time.Now()
	trace := &httptrace.ClientTrace{
		DNSStart:             func(httptrace.DNSStartInfo) { dnsStart = time.Now() },
		DNSDone:              func(httptrace.DNSDoneInfo) { t.DNSMs = time.Since(dnsStart).Milliseconds() },
		ConnectStart:         func(string, string) { connStart = time.Now() },
		ConnectDone:          func(string, string, error) { t.ConnectMs = time.Since(connStart).Milliseconds() },
		TLSHandshakeStart:    func() { tlsStart = time.Now() },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { t.TLSMs = time.Since(tlsStart).Milliseconds() },
		GotFirstResponseByte: func() { t.FirstByteMs = time.Since(start).Milliseconds() },
	}
	ctx = context.WithValue(httptrace.WithClientTrace(ctx, trace), carriesSecretKey{}, b.preview.Secret)
	resp, err := client.Do(b.req.WithContext(ctx))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return bikasend.Response{}, bikasend.ErrCancelled
		}
		return bikasend.Response{}, errors.Join(bikasend.ErrRequestFailed, errors.New(b.exp.mask(err.Error())))
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return bikasend.Response{}, bikasend.ErrCancelled
		}
		return bikasend.Response{}, errors.Join(bikasend.ErrRequestFailed, err)
	}
	t.TotalMs = time.Since(start).Milliseconds()

	out := bikasend.Response{
		Status:      resp.Status,
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		Size:        len(data),
		Truncated:   len(data) > maxBody,
		DurationMs:  t.TotalMs,
		Timing:      t,
		Issues:      []string{},
	}
	if out.Truncated {
		data = data[:maxBody]
		out.Size = maxBody
	}

	s.mu.Lock()
	if op, ok := s.ops[r.Operation]; ok && s.docs != nil && !out.Truncated {
		out.Issues = s.docs.validateResponse(op, resp.StatusCode, out.ContentType, data)
	}
	s.mu.Unlock()

	if isText(out.ContentType, data) {
		var pretty bytes.Buffer
		if json.Indent(&pretty, data, "", "  ") == nil {
			data = pretty.Bytes()
		}
		out.Body = string(data)
	} else {
		out.BodyBase64 = base64.StdEncoding.EncodeToString(data)
	}
	var headers strings.Builder
	for _, k := range sortedHeaderKeys(resp.Header) {
		for _, v := range resp.Header[k] {
			fmt.Fprintf(&headers, "%s: %s\n", k, v)
		}
	}
	out.Headers = headers.String()

	// Some APIs echo the key back; never show a loaded secret in a response.
	for _, v := range b.exp.sortedSecrets() {
		for _, form := range secretForms(v) {
			if strings.Contains(out.Body, form) || strings.Contains(out.Headers, form) {
				out.Body = strings.ReplaceAll(out.Body, form, "[redacted]")
				out.Headers = strings.ReplaceAll(out.Headers, form, "[redacted]")
				out.Redacted = true
			}
		}
	}

	s.addHistory(r, b.envName, out)
	return out, nil
}

func (s *Service) Cancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.inflight[id]; ok {
		cancel()
	}
}

func (s *Service) ClearCookies() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jars = map[string]http.CookieJar{}
	s.clients = map[string]*http.Client{}
}

func isText(contentType string, data []byte) bool {
	mt, _, _ := mime.ParseMediaType(contentType)
	switch {
	case strings.HasPrefix(mt, "text/"), isJSONType(mt), strings.Contains(mt, "xml"),
		strings.Contains(mt, "javascript"), mt == "application/x-www-form-urlencoded":
		return true
	case mt == "":
		return utf8.Valid(data)
	}
	return false
}

func sortedHeaderKeys(h http.Header) []string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// client returns the HTTP client of an environment: proxy, TLS, cookies, timeout and redirects.
func (s *Service) client(name string, env *environment) (*http.Client, error) {
	if c, ok := s.clients[name]; ok {
		return c, nil
	}
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, errors.New("unexpected default transport")
	}
	tr = tr.Clone()
	if s.state.Settings.Proxy != "" {
		u, err := url.Parse(s.state.Settings.Proxy)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(u)
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	timeout := defaultTimout
	follow := true
	if env != nil {
		if env.TimeoutSeconds > 0 {
			timeout = time.Duration(env.TimeoutSeconds) * time.Second
		}
		if env.FollowRedirects != nil {
			follow = *env.FollowRedirects
		}
		if env.TLS != nil {
			if err := s.loadTLS(env.TLS, tlsCfg); err != nil {
				return nil, err
			}
		}
	}
	tr.TLSClientConfig = tlsCfg
	jar, ok := s.jars[name]
	if !ok {
		jar, _ = cookiejar.New(nil)
		s.jars[name] = jar
	}
	c := &http.Client{
		Transport: tr,
		Jar:       jar,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !follow {
				return http.ErrUseLastResponse
			}
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			// Go strips Authorization across hosts but not custom headers such as X-API-Key.
			secret, _ := req.Context().Value(carriesSecretKey{}).(bool)
			if secret && req.URL.Host != via[0].URL.Host {
				return http.ErrUseLastResponse
			}
			if secret && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	s.clients[name] = c
	return c, nil
}

func (s *Service) loadTLS(cfg *tlsConfig, out *tls.Config) error {
	if s.root == nil {
		return bikasend.ErrNoFolder
	}
	if cfg.CAFile != "" {
		pem, err := readCapped(s.root.FS(), cfg.CAFile, maxTLSFile)
		if err != nil {
			return fmt.Errorf("%w: caFile: %w", bikasend.ErrInvalidConfig, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return fmt.Errorf("%w: caFile %s has no PEM certificates", bikasend.ErrInvalidConfig, cfg.CAFile)
		}
		out.RootCAs = pool
	}
	if cfg.CertFile != "" || cfg.KeyFile != "" {
		certPEM, err := readCapped(s.root.FS(), cfg.CertFile, maxTLSFile)
		if err != nil {
			return fmt.Errorf("%w: certFile: %w", bikasend.ErrInvalidConfig, err)
		}
		keyPEM, err := readCapped(s.root.FS(), cfg.KeyFile, maxTLSFile)
		if err != nil {
			return fmt.Errorf("%w: keyFile: %w", bikasend.ErrInvalidConfig, err)
		}
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return fmt.Errorf("%w: client certificate: %w", bikasend.ErrInvalidConfig, err)
		}
		out.Certificates = []tls.Certificate{cert}
	}
	return nil
}

func checkProxy(p string) error {
	u, err := url.Parse(p)
	if err != nil || u.Host == "" || !slices.Contains([]string{"http", "https", "socks5"}, u.Scheme) {
		return fmt.Errorf("%w: proxy must look like http://host:port", bikasend.ErrInvalidRequest)
	}
	return nil
}

// oauthToken returns a cached client-credentials token, or fetches one.
func (s *Service) oauthToken(ctx context.Context, b *built) (string, error) {
	if b.env == nil || b.env.OAuth2 == nil {
		return "", fmt.Errorf("%w: environment %q has no oauth2 settings", bikasend.ErrAuth, b.envName)
	}
	s.mu.Lock()
	if t, ok := s.tokens[b.envName]; ok && time.Now().Before(t.expires) {
		s.mu.Unlock()
		return t.value, nil
	}
	cfg := b.env.OAuth2
	exp := newExpansion(s.lookup(""))
	tokenURL, id, secret := exp.expand(cfg.TokenURL), exp.expand(cfg.ClientID), exp.expand(cfg.ClientSecret)
	scopes := make([]string, 0, len(cfg.Scopes))
	for _, sc := range cfg.Scopes {
		scopes = append(scopes, exp.expand(sc))
	}
	client, err := s.client(b.envName, b.env)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if err := exp.problem(); err != nil {
		return "", errors.Join(bikasend.ErrAuth, err)
	}
	u, err := url.Parse(tokenURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && (u.Scheme != "http" || !isLoopback(u.Hostname()))) {
		return "", fmt.Errorf("%w: tokenUrl must be https", bikasend.ErrAuth)
	}
	if len(b.env.Hosts) == 0 {
		return "", fmt.Errorf("%w: environment %q needs a hosts list for oauth2", bikasend.ErrSecretHost, b.envName)
	}
	if !matchHost(b.env.Hosts, u.Host) && !matchHost(b.env.Hosts, u.Hostname()) {
		return "", fmt.Errorf("%w: token host %s is not in the hosts list", bikasend.ErrSecretHost, u.Hostname())
	}

	form := url.Values{"grant_type": {"client_credentials"}}
	if len(scopes) > 0 {
		form.Set("scope", strings.Join(scopes, " "))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.Join(bikasend.ErrAuth, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(id), url.QueryEscape(secret))
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.Join(bikasend.ErrAuth, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || resp.StatusCode != http.StatusOK || tok.AccessToken == "" {
		msg := strings.TrimSpace(tok.Error + " " + tok.Description)
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("%w: token request failed: %s", bikasend.ErrAuth, msg)
	}
	life := time.Duration(tok.ExpiresIn) * time.Second
	if life <= 0 {
		life = 5 * time.Minute
	}
	s.mu.Lock()
	s.tokens[b.envName] = oauthToken{value: tok.AccessToken, expires: time.Now().Add(life - 30*time.Second)}
	s.mu.Unlock()
	return tok.AccessToken, nil
}

func (s *Service) signV4(ctx context.Context, b *built) error {
	if b.env == nil || b.env.SigV4 == nil {
		return fmt.Errorf("%w: environment %q has no sigv4 settings", bikasend.ErrAuth, b.envName)
	}
	profile, region := "", b.env.SigV4.Region
	if b.env.AWS != nil {
		profile = b.env.AWS.Profile
		if region == "" {
			region = b.env.AWS.Region
		}
	}
	if region == "" {
		return fmt.Errorf("%w: sigv4 needs a region in the environment", bikasend.ErrAuth)
	}
	s.mu.Lock()
	creds, ok := s.creds[profile]
	cli := s.awsCLI
	s.mu.Unlock()
	if !ok || (!creds.Expires.IsZero() && time.Until(creds.Expires) < time.Minute) {
		var err error
		if creds, err = cli.Credentials(ctx, profile); err != nil {
			return errors.Join(bikasend.ErrAuth, err)
		}
		s.mu.Lock()
		s.creds[profile] = creds
		s.mu.Unlock()
	}
	sigv4.Sign(b.req, b.body, creds, region, b.env.SigV4.Service, time.Now())
	return nil
}

func (s *Service) SecretConsoleURL(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.session[name]; ok {
		return "", fmt.Errorf("%w: {{%s}} is a session value; it has no AWS console page", bikasend.ErrInvalidVariable, name)
	}
	region := ""
	if e := s.envConfig(s.env); e != nil && e.AWS != nil {
		region = e.AWS.Region
	}
	for _, d := range s.definitions() {
		if v, ok := d.vars[name]; ok {
			return consoleURL(v, region)
		}
	}
	return "", fmt.Errorf("%w: {{%s}} is not defined in any scope", bikasend.ErrUndefinedVariable, name)
}

// consoleURL builds the console link from the stored reference; the host is fixed, names are escaped.
func consoleURL(v varValue, region string) (string, error) {
	host := "console.aws.amazon.com"
	q := url.Values{}
	if region != "" {
		if !awsRegion.MatchString(region) {
			return "", fmt.Errorf("%w: %q is not an AWS region", bikasend.ErrInvalidConfig, region)
		}
		host = region + "." + host
		q.Set("region", region)
	}
	switch v.kind() {
	case bikasend.KindAWS:
		q.Set("name", v.AWS)
		return "https://" + host + "/secretsmanager/secret?" + q.Encode(), nil
	case bikasend.KindSSM:
		// The console expects the parameter name escaped twice in the path (/ becomes %252F).
		return "https://" + host + "/systems-manager/parameters/" + url.PathEscape(url.PathEscape(v.SSM)) + "/description?" + q.Encode(), nil
	}
	return "", fmt.Errorf("%w: only aws and ssm values live in the AWS console", bikasend.ErrInvalidVariable)
}

// LoadSecrets fetches every AWS secret and SSM parameter the active scopes reference.
func (s *Service) LoadSecrets(ctx context.Context) (bikasend.Workspace, error) {
	s.mu.Lock()
	var ids, params []string
	for _, d := range s.definitions() {
		for _, v := range d.vars {
			switch {
			case v.AWS != "" && !slices.Contains(ids, v.AWS):
				ids = append(ids, v.AWS)
			case v.SSM != "" && !slices.Contains(params, v.SSM):
				params = append(params, v.SSM)
			}
		}
	}
	slices.Sort(ids)
	slices.Sort(params)
	status := bikasend.AWSStatus{State: "none"}
	if e := s.envConfig(s.env); e != nil && e.AWS != nil {
		status.Profile, status.Region = e.AWS.Profile, e.AWS.Region
	}
	cli := s.awsCLI
	s.mu.Unlock()

	if len(ids)+len(params) == 0 {
		return s.setAWS(status, nil), nil
	}
	account, err := cli.Identity(ctx, status.Profile)
	if err != nil {
		status.State, status.Error = "error", err.Error()
		if errors.Is(err, awscli.ErrNotSignedIn) {
			status.State = "signed-out"
		}
		return s.setAWS(status, nil), errors.Join(bikasend.ErrSecrets, err)
	}
	status.Account = account
	var failed []error
	values := map[string]string{}
	if len(ids) > 0 {
		v, err := cli.Secrets(ctx, status.Profile, status.Region, ids)
		for id, val := range v {
			values["aws:"+id] = val
		}
		if err != nil {
			failed = append(failed, err)
		}
	}
	if len(params) > 0 {
		v, err := cli.Parameters(ctx, status.Profile, status.Region, params)
		for n, val := range v {
			values["ssm:"+n] = val
		}
		if err != nil {
			failed = append(failed, err)
		}
	}
	status.State, status.Count, status.LoadedAt = "loaded", len(values), time.Now().Format("15:04")
	if err := errors.Join(failed...); err != nil {
		status.Error = err.Error()
		if len(values) == 0 {
			status.State = "error"
		}
		return s.setAWS(status, values), errors.Join(bikasend.ErrSecrets, err)
	}
	return s.setAWS(status, values), nil
}

func (s *Service) setAWS(status bikasend.AWSStatus, values map[string]string) bikasend.Workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aws = status
	s.secrets = map[string]string{}
	maps.Copy(s.secrets, values)
	return s.workspace()
}

// SignIn runs "aws sso login" for the environment's profile, then loads secrets.
func (s *Service) SignIn(ctx context.Context) (bikasend.Workspace, error) {
	s.mu.Lock()
	profile := ""
	if e := s.envConfig(s.env); e != nil && e.AWS != nil {
		profile = e.AWS.Profile
	}
	cli := s.awsCLI
	s.mu.Unlock()
	if err := cli.SignIn(ctx, profile); err != nil {
		return s.Workspace(), errors.Join(bikasend.ErrSecrets, err)
	}
	return s.LoadSecrets(ctx)
}

func (s *Service) ForgetSecrets() bikasend.Workspace {
	s.mu.Lock()
	s.creds = map[string]sigv4.Credentials{}
	s.tokens = map[string]oauthToken{}
	s.mu.Unlock()
	return s.setAWS(bikasend.AWSStatus{State: "none"}, nil)
}
