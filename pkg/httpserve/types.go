package httpserve

import (
	"fmt"
	"net/http"
	"net/url"
)

type Route struct {
	Method  string
	Path    string
	Handler http.Handler
	Group   *RouteGroup
}

type Router struct {
	Routes      []Route
	Middlewares []Middleware
	// TrustCloudflare reads the client IP from CF-Connecting-IP. Set it only behind a Cloudflare tunnel.
	TrustCloudflare bool
}

type Middleware func(http.Handler) http.Handler

func (r *Router) AddGroup(prefix string, middlewares ...Middleware) *RouteGroup {
	return &RouteGroup{Prefix: prefix, Middlewares: middlewares, Router: r}
}

func (r *Router) AddMiddleware(handler func(http.Handler) http.Handler) {
	r.Middlewares = append(r.Middlewares, Middleware(handler))
}

func (r *Router) AddRoute(method string, path string, handler http.Handler) {
	r.Routes = append(r.Routes, Route{Method: method, Path: path, Handler: handler})
}

func (r *Router) Get(path string, handler http.Handler) {
	r.AddRoute(http.MethodGet, path, handler)
}

func (r *Router) Post(path string, handler http.Handler) {
	r.AddRoute(http.MethodPost, path, handler)
}

type RouteGroup struct {
	Prefix      string
	Middlewares []Middleware
	Router      *Router
}

func (rg *RouteGroup) AddMiddleware(handler func(http.Handler) http.Handler) {
	rg.Middlewares = append(rg.Middlewares, Middleware(handler))
}

func (rg *RouteGroup) AddRoute(method string, path string, handler http.Handler) {
	fullPath, err := url.JoinPath(rg.Prefix, path) // #nosec G125 -- route patterns come from code at startup, never from a request.
	if err != nil {
		// Refuse to start with an invalid route.
		panic(fmt.Sprintf("invalid route path %q + %q: %v", rg.Prefix, path, err))
	}
	fullPath, _ = url.PathUnescape(fullPath)
	rg.Router.Routes = append(rg.Router.Routes, Route{Method: method, Path: fullPath, Handler: handler, Group: rg})
}

func (rg *RouteGroup) Get(path string, handler http.Handler) {
	rg.AddRoute(http.MethodGet, path, handler)
}

func (rg *RouteGroup) Post(path string, handler http.Handler) {
	rg.AddRoute(http.MethodPost, path, handler)
}
