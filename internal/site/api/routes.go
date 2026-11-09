package api

import (
	"github.com/BikaSolution/bikasend/pkg/httpserve"
	"github.com/BikaSolution/bikasend/pkg/sdk/site"
)

const (
	pagesPerMinute     = 300
	pagesBurst         = 60
	downloadsPerMinute = 30
	downloadsBurst     = 10
)

// Routes builds the site. trustCloudflare reads the client IP from the tunnel's header; set it only behind the tunnel.
func Routes(svc site.Service, trustCloudflare bool) httpserve.Router {
	router := httpserve.Router{TrustCloudflare: trustCloudflare}
	router.AddGenericHealthCheck()
	router.AddMiddleware(httpserve.LogRequests)
	router.AddMiddleware(httpserve.RateLimit(pagesPerMinute, pagesBurst))
	router.Get("/{$}", indexHandler(svc))
	router.Get("/changelog", changelogHandler(svc))
	router.Get("/feed.xml", feedHandler(svc))
	router.Get("/favicon.ico", staticHandler("favicon.ico"))
	router.Get("/static/{name}", staticHandler(""))

	downloads := router.AddGroup("/", httpserve.RateLimit(downloadsPerMinute, downloadsBurst))
	downloads.Get("/downloads/{tag}/{name}", downloadHandler(svc))
	downloads.Get("/latest.json", latestHandler(svc))
	return router
}
