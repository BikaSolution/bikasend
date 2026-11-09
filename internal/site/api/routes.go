package api

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/BikaSolution/bikasend/pkg/httpserve"
	"github.com/BikaSolution/bikasend/pkg/sdk/site"
)

const (
	pagesPerMinute     = 300
	pagesBurst         = 60
	downloadsPerMinute = 30
	downloadsBurst     = 10
)

type Options struct {
	// TrustCloudflare reads the client IP from the tunnel's header; set it only behind the tunnel.
	TrustCloudflare bool
	// PublicURL is the site's own origin for canonical links and the sitemap; "" leaves them out.
	PublicURL string
	// Repo is the GitHub owner/name that the source links point to; "" leaves them out.
	Repo string
}

func Routes(svc site.Service, opts Options) httpserve.Router {
	links := siteLinks{publicURL: opts.PublicURL, repoURL: repoURL(opts.Repo)}
	if links.repoURL != "" {
		links.repo = opts.Repo
	}
	router := httpserve.Router{TrustCloudflare: opts.TrustCloudflare}
	router.AddGenericHealthCheck()
	router.AddMiddleware(httpserve.LogRequests)
	router.AddMiddleware(httpserve.RateLimit(pagesPerMinute, pagesBurst))
	router.Get("/{$}", indexHandler(svc, links))
	router.Get("/changelog", changelogHandler(svc, links))
	router.Get("/feed.xml", feedHandler(svc))
	router.Get("/robots.txt", robotsHandler(links))
	router.Get("/sitemap.xml", sitemapHandler(svc, links))
	router.Get("/favicon.ico", staticHandler("favicon.ico"))
	router.Get("/static/{name}", staticHandler(""))

	downloads := router.AddGroup("/", httpserve.RateLimit(downloadsPerMinute, downloadsBurst))
	downloads.Get("/downloads/{tag}/{name}", downloadHandler(svc))
	downloads.Get("/latest.json", latestHandler(svc))
	return router
}

// ParsePublicURL accepts an https origin such as https://send.example.com, or http on loopback.
// Absolute links come from this setting, never from the request's Host header.
func ParsePublicURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return "", fmt.Errorf("%w: public URL %q must be scheme://host only", site.ErrInvalidConfig, raw)
	}
	loopback := u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return "", fmt.Errorf("%w: public URL %q must use https", site.ErrInvalidConfig, raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

func repoURL(repo string) string {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return ""
	}
	return "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}
