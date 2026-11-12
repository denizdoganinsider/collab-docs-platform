package controller

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"collab-docs-platform/gateway/internal/domain"
	"collab-docs-platform/gateway/internal/ratelimit"
	"collab-docs-platform/gateway/internal/service"

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
