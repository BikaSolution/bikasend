package httpserve

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

type httpserver struct {
	server *http.Server
}

// maxInFlight caps concurrent requests, so a flood from many IPs cannot pile up goroutines.
const maxInFlight = 256

const (
	healthPath    = "/healthz"
	healthBody    = "ok"
	healthTimeout = 3 * time.Second
)

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

// AddGenericHealthCheck answers /healthz with a fixed "ok". It shows no version and calls nothing upstream.
func (r *Router) AddGenericHealthCheck() {
	health := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, healthBody)
	})
	r.Get(healthPath, health)
}

// CheckHealth gets /healthz from the server on addr. The site image has no curl, so the binary
// runs its own Docker health check.
func CheckHealth(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+healthPath, nil)
	if err != nil {
		return err
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(len(healthBody))+1))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK || string(body) != healthBody {
		return fmt.Errorf("health check: status %d, body %q", resp.StatusCode, body)
	}
	return nil
}
