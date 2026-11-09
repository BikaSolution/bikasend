package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BikaSolution/bikasend/pkg/github"
	"github.com/BikaSolution/bikasend/pkg/sdk/site"
)

const (
	releaseLimit = 10
	// After a failed fetch, wait before asking GitHub again; an outage must not queue every visitor.
	retryAfter = time.Minute
	// GitHub's signed URLs live for minutes; reusing one stops repeat clicks from spending API quota.
	signedURLTTL = time.Minute
)

var platformOrder = []string{"macOS", "Windows", "Linux"}

type Service struct {
	gh  *github.Client
	ttl time.Duration

	// One lock around the fetch; it also stops parallel requests from all hitting GitHub.
	mu       sync.Mutex
	fetched  time.Time
	retryAt  time.Time
	lastErr  error
	releases []site.Release
	assetIDs map[string]int64
	signed   map[int64]signedURL
}

type signedURL struct {
	url     string
	expires time.Time
}

func NewService(gh *github.Client, ttl time.Duration) *Service {
	return &Service{gh: gh, ttl: ttl, signed: map[int64]signedURL{}}
}

func (s *Service) Releases(ctx context.Context) ([]site.Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refresh(ctx); err != nil {
		return nil, err
	}
	return s.releases, nil
}

func (s *Service) Latest(ctx context.Context) (site.Release, error) {
	releases, err := s.Releases(ctx)
	if err != nil {
		return site.Release{}, err
	}
	if len(releases) == 0 {
		return site.Release{}, site.ErrNotFound
	}
	for _, r := range releases {
		if !r.Prerelease {
			return r, nil
		}
	}
	return releases[0], nil
}

func (s *Service) DownloadURL(ctx context.Context, tag, name string) (string, error) {
	s.mu.Lock()
	err := s.refresh(ctx)
	id, ok := s.assetIDs[tag+"/"+name]
	cached, fresh := s.signed[id]
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", site.ErrNotFound
	}
	if fresh && time.Now().Before(cached.expires) {
		return cached.url, nil
	}
	u, err := s.gh.AssetURL(ctx, id)
	if err != nil {
		return "", errors.Join(site.ErrUnavailable, err)
	}
	s.mu.Lock()
	s.signed[id] = signedURL{url: u, expires: time.Now().Add(signedURLTTL)}
	s.mu.Unlock()
	return u, nil
}

// refresh keeps serving the last good list when GitHub fails, and backs off before trying again.
func (s *Service) refresh(ctx context.Context) error {
	now := time.Now()
	if s.releases != nil && now.Sub(s.fetched) < s.ttl {
		return nil
	}
	if now.Before(s.retryAt) {
		if s.releases != nil {
			return nil
		}
		return s.lastErr
	}
	// The fetch serves every waiting visitor, so one visitor who disconnects must not cancel it.
	raw, err := s.gh.Releases(context.WithoutCancel(ctx), releaseLimit)
	if err != nil {
		s.retryAt, s.lastErr = now.Add(retryAfter), errors.Join(site.ErrUnavailable, err)
		if s.releases != nil {
			slog.Warn("serve cached releases", "error", err)
			return nil
		}
		return s.lastErr
	}

	releases := []site.Release{}
	ids := map[string]int64{}
	for _, r := range raw {
		if r.Draft {
			continue
		}
		rel := site.Release{
			Tag:        r.TagName,
			Name:       r.Name,
			URL:        r.HTMLURL,
			Prerelease: r.Prerelease,
			Published:  r.PublishedAt,
			Notes:      notes(r.Body),
			Downloads:  []site.Download{},
			Files:      []site.Download{},
		}
		for _, a := range r.Assets {
			ids[r.TagName+"/"+a.Name] = a.ID
			d := site.Download{
				Name:     a.Name,
				Platform: platform(a.Name),
				Size:     fmt.Sprintf("%.1f MB", float64(a.Size)/(1<<20)),
				SHA256:   strings.TrimPrefix(a.Digest, "sha256:"),
			}
			if slices.Contains(platformOrder, d.Platform) {
				rel.Downloads = append(rel.Downloads, d)
			} else {
				rel.Files = append(rel.Files, d)
			}
		}
		slices.SortStableFunc(rel.Downloads, func(a, b site.Download) int {
			return slices.Index(platformOrder, a.Platform) - slices.Index(platformOrder, b.Platform)
		})
		releases = append(releases, rel)
	}
	s.releases, s.assetIDs, s.fetched = releases, ids, time.Now()
	s.retryAt, s.lastErr, s.signed = time.Time{}, nil, map[int64]signedURL{}
	return nil
}

func platform(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "macos") || strings.Contains(n, "darwin"):
		return "macOS"
	case strings.Contains(n, "windows") || strings.HasSuffix(n, ".exe"):
		return "Windows"
	case strings.Contains(n, "linux"):
		return "Linux"
	}
	return "Other"
}

var markdown = strings.NewReplacer("**", "", "__", "", "`", "")

// Headings, bullets and paragraphs only; links stay as plain text.
func notes(body string) []site.Note {
	out := []site.Note{}
	var para []string
	flush := func() {
		if len(para) > 0 {
			out = append(out, site.Note{Kind: "p", Text: strings.Join(para, " ")})
			para = nil
		}
	}
	for line := range strings.Lines(body) {
		t := markdown.Replace(strings.TrimSpace(line))
		switch {
		case t == "":
			flush()
		case strings.HasPrefix(t, "#"):
			flush()
			out = append(out, site.Note{Kind: "h", Text: strings.TrimSpace(strings.TrimLeft(t, "#"))})
		case strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* "):
			flush()
			if n := len(out); n > 0 && out[n-1].Kind == "ul" {
				out[n-1].Items = append(out[n-1].Items, t[2:])
			} else {
				out = append(out, site.Note{Kind: "ul", Items: []string{t[2:]}})
			}
		default:
			para = append(para, t)
		}
	}
	flush()
	return out
}
