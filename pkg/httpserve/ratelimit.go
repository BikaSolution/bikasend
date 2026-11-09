package httpserve

import (
	"context"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

const (
	maxBuckets = 10_000
	sweepEvery = time.Minute
)

type bucket struct {
	tokens float64
	last   time.Time
}

type limiter struct {
	rate  float64 // tokens per second
	burst float64
	idle  time.Duration
	now   func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

// RateLimit allows each client perMinute requests on average and burst at once; extra requests get 429.
func RateLimit(perMinute, burst int) Middleware {
	return newLimiter(perMinute, burst, time.Now).middleware
}

func newLimiter(perMinute, burst int, now func() time.Time) *limiter {
	rate := float64(perMinute) / 60
	return &limiter{
		rate:  rate,
		burst: float64(burst),
		// A bucket idle this long is full again, so removing it changes nothing.
		idle:      time.Duration(float64(burst) / rate * float64(time.Second)),
		now:       now,
		buckets:   map[string]*bucket{},
		lastSweep: now(),
	}
}

func (l *limiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wait := l.take(clientKey(r)); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// take spends one token of key's bucket and returns 0, or returns how long to wait for one.
func (l *limiter) take(key string) time.Duration {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastSweep) >= sweepEvery {
		l.sweep(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		// O(1) on purpose: scanning for the oldest bucket on every new IP would let a flood burn CPU.
		if len(l.buckets) >= maxBuckets {
			for k := range l.buckets {
				delete(l.buckets, k)
				break
			}
		}
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens < 1 {
		return time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	}
	b.tokens--
	return 0
}

func (l *limiter) sweep(now time.Time) {
	for key, b := range l.buckets {
		if now.Sub(b.last) >= l.idle {
			delete(l.buckets, key)
		}
	}
	l.lastSweep = now
}

type clientKeyCtx struct{}

func withClientIP(trustCloudflare bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), clientKeyCtx{}, clientIP(r, trustCloudflare))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func clientKey(r *http.Request) string {
	if k, ok := r.Context().Value(clientKeyCtx{}).(string); ok {
		return k
	}
	return clientIP(r, false)
}

// clientIP keys IPv6 clients by /64, the block one subscriber usually holds.
func clientIP(r *http.Request, trustCloudflare bool) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	// cloudflared reaches the site over the Docker network; a public peer could forge the header.
	if trustCloudflare && (peer.IsPrivate() || peer.IsLoopback()) {
		if addr, err := netip.ParseAddr(r.Header.Get("CF-Connecting-IP")); err == nil {
			return key(addr)
		}
	}
	return key(peer)
}

func key(addr netip.Addr) string {
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	prefix, _ := addr.Prefix(64)
	return prefix.String()
}
