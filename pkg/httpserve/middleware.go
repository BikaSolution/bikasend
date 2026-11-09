package httpserve

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

const (
	csp         = "default-src 'none'; style-src 'self'; img-src 'self'; font-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	permissions = "camera=(), microphone=(), geolocation=(), payment=(), usb=(), accelerometer=(), gyroscope=(), magnetometer=()"
	maxLogPath  = 200
)

// limitInFlight answers 503 once n requests are running; it never queues, so a flood cannot grow memory.
func limitInFlight(n int, next http.Handler) http.Handler {
	slots := make(chan struct{}, n)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "5")
			http.Error(w, "busy, try again shortly", http.StatusServiceUnavailable)
		}
	})
}

func withTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("panic caught in HTTP handler", "error", err, "path", r.URL.Path)
				w.Header().Set("Connection", "close")
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", permissions)
		next.ServeHTTP(w, r)
	})
}

// LogRequests logs method and a capped path only; never headers or query strings.
func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if len(p) > maxLogPath {
			p = p[:maxLogPath] + "…"
		}
		slog.Info("incoming HTTP request", "method", r.Method, "path", p)
		next.ServeHTTP(w, r)
	})
}
