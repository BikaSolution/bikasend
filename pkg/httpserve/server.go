package httpserve

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type httpserver struct {
	server *http.Server
}

// maxInFlight caps concurrent requests, so a flood from many IPs cannot pile up goroutines.
const maxInFlight = 256

func New(port int, host string, router Router) HTTPServer {
	return &httpserver{
		server: &http.Server{
			Addr:              fmt.Sprintf("%s:%d", host, port),
			Handler:           Handler(router),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      45 * time.Second,
			IdleTimeout:       60 * time.Second,
			MaxHeaderBytes:    16 << 10,
		},
	}
}

// Handler wraps every route in its group and router middleware, then the global ones.
// Security headers, the in-flight cap and the client IP wrap the mux, so 404 and 405 answers get them too.
func Handler(router Router) http.Handler {
	mux := http.NewServeMux()
	for _, route := range router.Routes {
		handler := route.Handler
		if route.Group != nil {
			for _, middleware := range route.Group.Middlewares {
				handler = middleware(handler)
			}
		}
		for _, middleware := range router.Middlewares {
			handler = middleware(handler)
		}
		mux.Handle(route.Method+" "+route.Path, recoverPanic(withTimeout(handler)))
	}
	return withClientIP(router.TrustCloudflare, limitInFlight(maxInFlight, withSecurityHeaders(mux)))
}

func (s *httpserver) ListenAndServe() error {
	return s.server.ListenAndServe()
}

func (s *httpserver) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

func (r *Router) AddGenericHealthCheck() {
	r.Get("/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
}
