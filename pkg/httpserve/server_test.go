package httpserve

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const wantCSP = "default-src 'none'; style-src 'self'; img-src 'self'; font-src 'self'; manifest-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func TestServer(t *testing.T) {
	var router Router
	router.AddGenericHealthCheck()
	router.AddMiddleware(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Router", "1")
			next.ServeHTTP(w, r)
		})
	})
	group := router.AddGroup("/v1", func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Group", "1")
			next.ServeHTTP(w, r)
		})
	})
	group.Get("/item/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.PathValue("id")))
	}))
	group.Get("/panic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	handler := Handler(router)

	tests := []struct {
		name        string
		method      string
		path        string
		wantStatus  int
		wantBody    string
		wantGroup   bool
		wantNoStore bool
	}{
		{name: "healthz", method: http.MethodGet, path: "/healthz", wantStatus: http.StatusOK, wantBody: "ok", wantNoStore: true},
		{name: "older health path", method: http.MethodGet, path: "/health", wantStatus: http.StatusOK, wantBody: "ok", wantNoStore: true},
		{name: "group route with path value", method: http.MethodGet, path: "/v1/item/42", wantStatus: http.StatusOK, wantBody: "42", wantGroup: true},
		{name: "panic recovers to 500", method: http.MethodGet, path: "/v1/panic", wantStatus: http.StatusInternalServerError, wantBody: "internal server error\n", wantGroup: true},
		{name: "unknown path", method: http.MethodGet, path: "/nope", wantStatus: http.StatusNotFound},
		{name: "wrong method", method: http.MethodPost, path: "/health", wantStatus: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && w.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", w.Body.String(), tt.wantBody)
			}
			if got := w.Header().Get("Content-Security-Policy"); got != wantCSP {
				t.Errorf("CSP = %q, want %q", got, wantCSP)
			}
			if tt.wantStatus == http.StatusMethodNotAllowed || tt.wantStatus == http.StatusNotFound {
				return
			}
			h := w.Header()
			if h.Get("X-Router") != "1" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Strict-Transport-Security") == "" ||
				h.Get("Cross-Origin-Opener-Policy") != "same-origin" || h.Get("Cross-Origin-Resource-Policy") != "same-origin" || h.Get("Permissions-Policy") != permissions {
				t.Errorf("router or security headers missing: %v", h)
			}
			if (h.Get("Cache-Control") == "no-store") != tt.wantNoStore {
				t.Errorf("Cache-Control = %q, want no-store %v", h.Get("Cache-Control"), tt.wantNoStore)
			}
			if (w.Header().Get("X-Group") == "1") != tt.wantGroup {
				t.Errorf("group header = %q, want %v", w.Header().Get("X-Group"), tt.wantGroup)
			}
		})
	}
}

func TestCheckHealth(t *testing.T) {
	var healthy Router
	healthy.AddGenericHealthCheck()
	tests := []struct {
		name    string
		handler http.Handler
		wantErr bool
	}{
		{name: "healthy site", handler: Handler(healthy)},
		{name: "server error", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "ok", http.StatusInternalServerError) }), wantErr: true},
		{name: "wrong body", handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("okay")) }), wantErr: true},
		{name: "redirect is not followed", handler: http.RedirectHandler("/healthz2", http.StatusFound), wantErr: true},
		{name: "hung server", handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if err := CheckHealth(ctx, srv.Listener.Addr().String()); (err != nil) != tt.wantErr {
				t.Errorf("CheckHealth error = %v, want error %v", err, tt.wantErr)
			}
		})
	}
	srv := httptest.NewServer(Handler(healthy))
	addr := srv.Listener.Addr().String()
	srv.Close()
	if err := CheckHealth(context.Background(), addr); err == nil {
		t.Error("CheckHealth with nothing listening: want an error")
	}
}

func TestServerLimits(t *testing.T) {
	srv, ok := New(8091, "127.0.0.1", Router{}).(*httpserver)
	if !ok {
		t.Fatal("unexpected server type")
	}
	s := srv.server
	if s.ReadHeaderTimeout == 0 || s.ReadTimeout == 0 || s.WriteTimeout == 0 || s.IdleTimeout == 0 || s.MaxHeaderBytes > 64<<10 {
		t.Errorf("slow-client limits missing: %+v", s)
	}

	release, entered := make(chan struct{}), make(chan struct{}, 4)
	handler := limitInFlight(1, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		entered <- struct{}{}
		<-release
	}))
	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	<-entered
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	close(release)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Errorf("over the in-flight cap: status %d, headers %v", w.Code, w.Header())
	}
	time.Sleep(10 * time.Millisecond)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code == http.StatusServiceUnavailable {
		t.Error("a finished request must free its slot")
	}
}

func TestLogRequestsCapsPath(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	r := httptest.NewRequest(http.MethodGet, "/"+strings.Repeat("a", 5000)+"?token=secret", nil)
	LogRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), r)
	if buf.Len() > 600 || strings.Contains(buf.String(), "secret") {
		t.Errorf("log line = %d bytes: %s", buf.Len(), buf.String())
	}
}
