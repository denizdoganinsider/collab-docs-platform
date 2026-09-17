package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"collab-docs-platform/gateway/internal/middleware"

	"github.com/labstack/echo/v4"
)

func captureOrigin(t *testing.T) (*httptest.Server, *http.Header) {
	t.Helper()
	var seen http.Header
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(origin.Close)
	return origin, &seen
}

// The trust model is four header operations. This pins all four against a
// request that arrives carrying attacker-chosen values for each.
func TestTrustedProxyOverwritesIdentityAndStripsToken(t *testing.T) {
	origin, seen := captureOrigin(t)

	handler, err := NewTrustedProxy(origin.URL, "gateway-key")
	if err != nil {
		t.Fatal(err)
	}

	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/documents/1", nil)
	request.Header.Set(UserIDHeader, "999")           // attacker's claim
	request.Header.Set(UserRoleHeader, "admin")       // attacker's promotion
	request.Header.Set(GatewayKeyHeader, "guess")     // attacker's guess
	request.Header.Set("Authorization", "Bearer tok") // the real token
	c := e.NewContext(request, httptest.NewRecorder())
	c.Set(middleware.RequestIDKey, "req-1")
	c.Set(middleware.UserIDKey, int64(42)) // what JWTMiddleware established
	c.Set(middleware.RoleKey, "user")

	_ = handler(c)

	if got := seen.Get(UserIDHeader); got != "42" {
		t.Errorf("X-User-ID = %q, want the gateway's 42, not the client's 999", got)
	}
	if got := seen.Get(UserRoleHeader); got != "user" {
		t.Errorf("X-User-Role = %q, want the token's user, not the client's admin", got)
	}
	if got := seen.Get(GatewayKeyHeader); got != "gateway-key" {
		t.Errorf("X-Gateway-Key = %q, want the gateway's own", got)
	}
	if got := seen.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q reached the origin; it must be stripped", got)
	}
	if got := seen.Get(middleware.RequestIDHeader); got != "req-1" {
		t.Errorf("X-Request-ID = %q, want req-1", got)
	}
}

// No identity in context (a route wired outside the auth group): the
// client's identity headers must still not get through.
func TestTrustedProxyWithoutIdentityForwardsNone(t *testing.T) {
	origin, seen := captureOrigin(t)

	handler, _ := NewTrustedProxy(origin.URL, "gateway-key")

	e := echo.New()
	request := httptest.NewRequest(http.MethodPost, "/documents", nil)
	request.Header.Set(UserIDHeader, "999")
	request.Header.Set(UserRoleHeader, "admin")
	c := e.NewContext(request, httptest.NewRecorder())

	_ = handler(c)

	if _, present := (*seen)[http.CanonicalHeaderKey(UserIDHeader)]; present {
		t.Errorf("X-User-ID = %q forwarded with no identity in context", seen.Get(UserIDHeader))
	}
	if _, present := (*seen)[http.CanonicalHeaderKey(UserRoleHeader)]; present {
		t.Errorf("X-User-Role = %q forwarded with no identity in context", seen.Get(UserRoleHeader))
	}
	if got := seen.Get(GatewayKeyHeader); got != "gateway-key" {
		t.Errorf("X-Gateway-Key = %q, want the gateway's own", got)
	}
}

// A dead backend answers with the project-wide error shape, not an empty 502.
func TestTrustedProxyBackendDownIs502JSON(t *testing.T) {
	origin := httptest.NewServer(http.NotFoundHandler())
	origin.Close()

	handler, _ := NewTrustedProxy(origin.URL, "gateway-key")

	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/documents", nil)
	recorder := httptest.NewRecorder()
	c := e.NewContext(request, recorder)

	_ = handler(c)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Errorf("body = %q, want {\"error\":...}", recorder.Body.String())
	}
}
