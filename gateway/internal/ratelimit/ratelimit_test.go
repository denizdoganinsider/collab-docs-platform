package ratelimit

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/redis/go-redis/v9"
)

// testRedis needs DOCS_TEST_REDIS (host:port of the compose Redis) and uses
// database 15 so a run never touches the services' keys.
func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("DOCS_TEST_REDIS")
	if addr == "" {
		t.Skip("DOCS_TEST_REDIS not set")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DB: 15})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis at %s: %v", addr, err)
	}
	t.Cleanup(func() { client.FlushDB(context.Background()); client.Close() })
	return client
}

func TestSlidingWindowAdmitsExactlyLimitPerWindow(t *testing.T) {
	client := testRedis(t)
	l := NewSlidingWindow(client, "t:sw:", 5, time.Second)
	ctx := context.Background()

	for i := range 5 {
		d, err := l.Allow(ctx, "k")
		if err != nil || !d.Allowed {
			t.Fatalf("request %d = %+v, %v; want allowed", i+1, d, err)
		}
	}
	d, err := l.Allow(ctx, "k")
	if err != nil || d.Allowed {
		t.Fatalf("6th = %+v, %v; want refused", d, err)
	}
	if d.RetryAfter <= 0 || d.RetryAfter > time.Second {
		t.Fatalf("RetryAfter = %v, want in (0, 1s]", d.RetryAfter)
	}
	// A refused request is not logged: it must not extend the lockout.
	if n := client.ZCard(ctx, "t:sw:k").Val(); n != 5 {
		t.Fatalf("set size = %d, want 5", n)
	}

	time.Sleep(d.RetryAfter + 50*time.Millisecond)
	if d, _ := l.Allow(ctx, "k"); !d.Allowed {
		t.Fatal("not admitted after RetryAfter")
	}
	if ttl := client.PTTL(ctx, "t:sw:k").Val(); ttl <= 0 {
		t.Fatalf("PTTL = %v, want an expiry", ttl)
	}
}

func TestSlidingWindowKeysAreIndependent(t *testing.T) {
	l := NewSlidingWindow(testRedis(t), "t:sw:", 1, time.Minute)
	ctx := context.Background()
	if d, _ := l.Allow(ctx, "a"); !d.Allowed {
		t.Fatal("a refused")
	}
	if d, _ := l.Allow(ctx, "b"); !d.Allowed {
		t.Fatal("b refused because of a")
	}
}

func TestFixedWindowRefusesOverLimit(t *testing.T) {
	client := testRedis(t)
	l := NewFixedWindow(client, "t:fw:", 3, time.Minute)
	ctx := context.Background()
	for i := range 3 {
		if d, err := l.Allow(ctx, "k"); err != nil || !d.Allowed {
			t.Fatalf("request %d = %+v, %v", i+1, d, err)
		}
	}
	d, err := l.Allow(ctx, "k")
	if err != nil || d.Allowed || d.RetryAfter <= 0 || d.RetryAfter > time.Minute {
		t.Fatalf("4th = %+v, %v; want refused with RetryAfter in (0, 1m]", d, err)
	}
}

// SD-154318: a counter that lost its TTL must get one back on the next hit,
// or its key is locked out forever.
func TestFixedWindowRepairsAKeyWithoutTTL(t *testing.T) {
	client := testRedis(t)
	ctx := context.Background()
	client.Set(ctx, "t:fw:stuck", 100, 0)

	l := NewFixedWindow(client, "t:fw:", 3, time.Minute)
	if _, err := l.Allow(ctx, "stuck"); err != nil {
		t.Fatal(err)
	}
	if ttl := client.PTTL(ctx, "t:fw:stuck").Val(); ttl <= 0 {
		t.Fatalf("PTTL = %v, want the window restored", ttl)
	}
}

func TestFixedWindowKeyExpiresAndResets(t *testing.T) {
	client := testRedis(t)
	l := NewFixedWindow(client, "t:fw:", 1, 200*time.Millisecond)
	ctx := context.Background()
	l.Allow(ctx, "k")
	if d, _ := l.Allow(ctx, "k"); d.Allowed {
		t.Fatal("2nd admitted")
	}
	time.Sleep(300 * time.Millisecond)
	if d, _ := l.Allow(ctx, "k"); !d.Allowed {
		t.Fatal("not reset after the window")
	}
}

type stubLimiter struct {
	d   Decision
	err error
}

func (s stubLimiter) Allow(context.Context, string) (Decision, error) { return s.d, s.err }

func serve(t *testing.T, l Limiter, key KeyFunc) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	e.GET("/", func(c echo.Context) error { return c.String(http.StatusOK, "ok") }, Middleware("test", l, key))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec
}

func constKey(c echo.Context) string { return "k" }

func TestMiddlewareRefusesWith429AndRetryAfter(t *testing.T) {
	rec := serve(t, stubLimiter{d: Decision{RetryAfter: 1500 * time.Millisecond}}, constKey)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("got %d Retry-After=%q, want 429 and 2 (rounded up)", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestMiddlewareRetryAfterIsAtLeastOneSecond(t *testing.T) {
	rec := serve(t, stubLimiter{d: Decision{RetryAfter: time.Millisecond}}, constKey)
	if rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", rec.Header().Get("Retry-After"))
	}
}

func TestMiddlewareFailsOpenOnLimiterError(t *testing.T) {
	rec := serve(t, stubLimiter{err: errors.New("redis down")}, constKey)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (fail open)", rec.Code)
	}
}

func TestMiddlewareSkipsEmptyKey(t *testing.T) {
	rec := serve(t, stubLimiter{d: Decision{}}, func(echo.Context) string { return "" })
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 for an unkeyed request", rec.Code)
	}
}
