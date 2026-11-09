package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const maxJSON = 5 << 20

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type Client struct {
	http        *http.Client
	releasesURL string
	token       string
	assetHost   func(host string) bool
}

// New reads releases of one repo. token is optional; a private repo needs a read-only token.
func New(httpClient *http.Client, baseURL, repo, token string) (*Client, error) {
	if !repoPattern.MatchString(repo) || strings.Contains(repo, "..") {
		return nil, fmt.Errorf("%w: %q", ErrInvalidRepo, repo)
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && (base.Scheme != "http" || !isLoopback(base.Hostname()))) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidAPI, baseURL)
	}
	owner, name, _ := strings.Cut(repo, "/")
	releasesURL, err := url.JoinPath(baseURL, "repos", url.PathEscape(owner), url.PathEscape(name), "releases")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRepo, err)
	}
	// GitHub Enterprise and test servers sign URLs on their own hosts; github.com uses these.
	assetHost := func(string) bool { return true }
	if base.Hostname() == "api.github.com" {
		assetHost = func(h string) bool { return h == "github.com" || strings.HasSuffix(h, ".githubusercontent.com") }
	}
	// Asset downloads answer with a redirect to a signed URL; hand that URL out, do not follow it.
	noRedirect := *httpClient
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &noRedirect, releasesURL: releasesURL, token: token, assetHost: assetHost}, nil
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (c *Client) Releases(ctx context.Context, limit int) ([]Release, error) {
	resp, err := c.get(ctx, c.releasesURL+"?per_page="+strconv.Itoa(limit), "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: list releases: %s", ErrUpstream, resp.Status)
	}
	releases := []Release{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSON)).Decode(&releases); err != nil {
		return nil, errors.Join(ErrUpstream, err)
	}
	return releases, nil
}

// AssetURL returns GitHub's short-lived signed download URL for an asset. It holds no token.
func (c *Client) AssetURL(ctx context.Context, id int64) (string, error) {
	resp, err := c.get(ctx, c.releasesURL+"/assets/"+strconv.FormatInt(id, 10), "application/octet-stream")
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	location, err := resp.Location()
	if resp.StatusCode != http.StatusFound || err != nil || location.Scheme != "https" || !c.assetHost(location.Hostname()) {
		return "", fmt.Errorf("%w: asset %d: %s", ErrUpstream, id, resp.Status)
	}
	return location.String(), nil
}

func (c *Client) get(ctx context.Context, u, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, errors.Join(ErrUpstream, err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "bikasend-site")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.Join(ErrUpstream, err)
	}
	return resp, nil
}
