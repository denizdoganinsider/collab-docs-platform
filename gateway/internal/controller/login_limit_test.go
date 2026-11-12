package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"collab-docs-platform/gateway/internal/domain"
	"collab-docs-platform/gateway/internal/ratelimit"
	"collab-docs-platform/gateway/internal/service"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

type stubLoginLimiter struct {
	d    ratelimit.Decision
	err  error
	keys []string
}

func (s *stubLoginLimiter) Allow(_ context.Context, key string) (ratelimit.Decision, error) {
	s.keys = append(s.keys, key)
	return s.d, s.err
}

func knownUserRepo(t *testing.T) *stubRepo {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("Passw0rd1"), bcrypt.MinCost)
	return &stubRepo{user: &domain.User{ID: 7, Email: "a@x.com", PasswordHash: string(hash), Role: "user"}}
}

// Once the window is spent, the right password gets the same 429 as a wrong
// one: the answer must not confirm a guess.
func TestLogin_RateLimitedEvenWithTheRightPassword(t *testing.T) {
	limiter := &stubLoginLimiter{d: ratelimit.Decision{RetryAfter: 30 * time.Second}}
	ac := NewAuthController(service.NewUserService(knownUserRepo(t)), limiter)

	for _, pw := range []string{"Passw0rd1", "wrong"} {
		code, body := call(t, ac.Login, http.MethodPost, `{"email":"a@x.com","password":"`+pw+`"}`, nil)
		if code != http.StatusTooManyRequests || body["error"] != "rate limit exceeded" {
			t.Fatalf("password %q: %d %v, want 429", pw, code, body)
		}
	}
}

func TestLogin_LimiterKeyHidesTheEmailAndIgnoresCase(t *testing.T) {
	limiter := &stubLoginLimiter{d: ratelimit.Decision{Allowed: true}}
	ac := NewAuthController(service.NewUserService(knownUserRepo(t)), limiter)

	call(t, ac.Login, http.MethodPost, `{"email":"a@x.com","password":"Passw0rd1"}`, nil)
	call(t, ac.Login, http.MethodPost, `{"email":" A@X.com ","password":"Passw0rd1"}`, nil)
	if len(limiter.keys) != 2 || limiter.keys[0] != limiter.keys[1] {
		t.Fatalf("keys = %v, want one key for both spellings", limiter.keys)
	}
	if len(limiter.keys[0]) != 64 {
		t.Fatalf("key %q is not a sha256 hex digest", limiter.keys[0])
	}
}

func TestLogin_LimiterErrorFailsOpen(t *testing.T) {
	limiter := &stubLoginLimiter{err: errors.New("redis down")}
	ac := NewAuthController(service.NewUserService(knownUserRepo(t)), limiter)
	if code, _ := call(t, ac.Login, http.MethodPost, `{"email":"a@x.com","password":"Passw0rd1"}`, nil); code != http.StatusOK {
		t.Fatalf("limiter down: %d, want 200", code)
	}
}

func TestLogin_InvalidBodyIsNotCounted(t *testing.T) {
	limiter := &stubLoginLimiter{d: ratelimit.Decision{Allowed: true}}
	ac := NewAuthController(service.NewUserService(knownUserRepo(t)), limiter)
	call(t, ac.Login, http.MethodPost, `{"email":"","password":""}`, nil)
	if len(limiter.keys) != 0 {
		t.Fatal("a request without credentials was counted")
	}
}

// loginFrom posts a login from remoteAddr through an Echo that reads the
// client IP from the socket peer, as the gateway's main does.
func loginFrom(t *testing.T, ac *AuthController, remoteAddr, xff string) {
	t.Helper()
	e := echo.New()
	e.IPExtractor = echo.ExtractIPDirect()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"email":"a@x.com","password":"Passw0rd1"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	if err := ac.Login(e.NewContext(req, httptest.NewRecorder())); err != nil {
		t.Fatal(err)
	}
}

// The guard is per email AND client IP: one address cannot lock the
// account out for everybody, and a forged X-Forwarded-For is not an address.
func TestLogin_LimiterKeyIsPerClientIP(t *testing.T) {
	limiter := &stubLoginLimiter{d: ratelimit.Decision{Allowed: true}}
	ac := NewAuthController(service.NewUserService(knownUserRepo(t)), limiter)

	loginFrom(t, ac, "198.51.100.7:4000", "")
	loginFrom(t, ac, "198.51.100.7:4001", "")
	loginFrom(t, ac, "203.0.113.9:4000", "")
	loginFrom(t, ac, "198.51.100.7:4002", "192.0.2.55")

	k := limiter.keys
	if len(k) != 4 {
		t.Fatalf("keys = %v, want 4", k)
	}
	if k[0] != k[1] {
		t.Error("the same IP on another source port got another key")
	}
	if k[0] == k[2] {
		t.Error("two client IPs share one key")
	}
	if k[0] != k[3] {
		t.Error("X-Forwarded-For changed the key")
	}
}
