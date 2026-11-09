package httpserve

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestRateLimit(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	l := newLimiter(30, 3, c.now)
	handler := withClientIP(true, l.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	get := func(cfIP string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/latest.json", nil)
		r.RemoteAddr = "172.18.0.3:4100"
		if cfIP != "" {
			r.Header.Set("CF-Connecting-IP", cfIP)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	steps := []struct {
		name      string
		advance   time.Duration
		ip        string
		want      int
		wantRetry string
	}{
		{name: "burst 1", ip: "203.0.113.7", want: http.StatusOK},
		{name: "burst 2", ip: "203.0.113.7", want: http.StatusOK},
		{name: "burst 3", ip: "203.0.113.7", want: http.StatusOK},
		{name: "over the burst", ip: "203.0.113.7", want: http.StatusTooManyRequests, wantRetry: "2"},
		{name: "another client has its own bucket", ip: "203.0.113.8", want: http.StatusOK},
		{name: "without the header the remote address counts", want: http.StatusOK},
		{name: "one token back after 2s at 30/min", advance: 2 * time.Second, ip: "203.0.113.7", want: http.StatusOK},
		{name: "and spent again", ip: "203.0.113.7", want: http.StatusTooManyRequests, wantRetry: "2"},
		{name: "same IPv6 /64 shares a bucket", ip: "2001:db8:1:2::1", want: http.StatusOK},
		{name: "/64 second", ip: "2001:db8:1:2::2", want: http.StatusOK},
		{name: "/64 third", ip: "2001:db8:1:2:ffff::3", want: http.StatusOK},
		{name: "/64 over the burst", ip: "2001:db8:1:2::4", want: http.StatusTooManyRequests, wantRetry: "2"},
		{name: "another /64", ip: "2001:db8:1:3::1", want: http.StatusOK},
	}
	for _, s := range steps {
		c.t = c.t.Add(s.advance)
		w := get(s.ip)
		if w.Code != s.want || w.Header().Get("Retry-After") != s.wantRetry {
			t.Fatalf("%s: status = %d, Retry-After = %q; want %d, %q", s.name, w.Code, w.Header().Get("Retry-After"), s.want, s.wantRetry)
		}
	}

	c.t = c.t.Add(sweepEvery)
	get("203.0.113.9")
	if len(l.buckets) != 1 {
		t.Errorf("after a sweep %d buckets remain, want only the new one", len(l.buckets))
	}
}

func TestRateLimitCap(t *testing.T) {
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	l := newLimiter(30, 3, c.now)
	for i := range maxBuckets + 5 {
		c.t = c.t.Add(time.Microsecond)
		l.take(strconv.Itoa(i))
	}
	if len(l.buckets) != maxBuckets {
		t.Errorf("buckets = %d, want the cap %d", len(l.buckets), maxBuckets)
	}
	if _, ok := l.buckets[strconv.Itoa(maxBuckets+4)]; !ok {
		t.Error("the newest client must get a bucket")
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		trust      bool
		cf         string
		remoteAddr string
		want       string
	}{
		{name: "cloudflare header from the docker network", trust: true, cf: "198.51.100.4", remoteAddr: "172.18.0.3:4100", want: "198.51.100.4"},
		{name: "header ignored unless trusted", cf: "198.51.100.4", remoteAddr: "172.18.0.3:4100", want: "172.18.0.3"},
		{name: "header ignored from a public peer", trust: true, cf: "198.51.100.4", remoteAddr: "192.0.2.1:1234", want: "192.0.2.1"},
		{name: "remote address", remoteAddr: "192.0.2.1:1234", want: "192.0.2.1"},
		{name: "garbage header falls back", trust: true, cf: "not-an-ip", remoteAddr: "10.0.0.2:1234", want: "10.0.0.2"},
		{name: "IPv4-mapped IPv6", trust: true, cf: "::ffff:198.51.100.4", remoteAddr: "127.0.0.1:1", want: "198.51.100.4"},
		{name: "IPv6 by /64", trust: true, cf: "2001:db8:aa:bb:cc::1", remoteAddr: "127.0.0.1:1", want: "2001:db8:aa:bb::/64"},
		{name: "IPv6 remote address", remoteAddr: "[2001:db8::1]:443", want: "2001:db8::/64"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.cf != "" {
				r.Header.Set("CF-Connecting-IP", tt.cf)
			}
			if got := clientIP(r, tt.trust); got != tt.want {
				t.Errorf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}
