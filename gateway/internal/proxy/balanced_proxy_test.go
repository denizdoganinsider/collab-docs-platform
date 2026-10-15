package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"collab-docs-platform/gateway/internal/loadbalancer"
	"collab-docs-platform/gateway/internal/middleware"

	"github.com/labstack/echo/v4"
)

// A backend that answers /health with 200 and everything else with its own
// name plus the request headers it saw, so a test can tell which instance a
// request reached and what the proxy handed it.
func namedBackend(t *testing.T, name string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("X-Backend", name)
		w.Header().Set("X-Seen-Host", r.Host)
		for _, h := range []string{middleware.RequestIDHeader, GatewayKeyHeader, UserIDHeader, UserRoleHeader, "Authorization"} {
			w.Header().Set("X-Seen-"+h, r.Header.Get(h))
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	return server
}

func startedPool(t *testing.T, urls ...string) *loadbalancer.Pool {
	t.Helper()

	pool, err := loadbalancer.NewPool(urls)
	if err != nil {
		t.Fatal(err)
	}
	pool.Start(time.Hour)
	t.Cleanup(pool.Close)

	return pool
}

// serveAs runs the handler as the gateway would after JWTMiddleware: request
// id and identity in the context, the client's own headers on the request.
func serveAs(handler echo.HandlerFunc, target string, userID int64, role string) *httptest.ResponseRecorder {
	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Header.Set("Authorization", "Bearer tok")
	request.Header.Set(UserIDHeader, "999")
	request.Header.Set(UserRoleHeader, "admin")
	request.Header.Set(GatewayKeyHeader, "guess")
	recorder := httptest.NewRecorder()
	c := e.NewContext(request, recorder)
	c.Set(middleware.RequestIDKey, "req-123")
	if userID != 0 {
		c.Set(middleware.UserIDKey, userID)
		c.Set(middleware.RoleKey, role)
	}
	_ = handler(c)

	return recorder
}

func TestBalancedProxyRoundRobinAlternates(t *testing.T) {
	a := namedBackend(t, "a")
	b := namedBackend(t, "b")
	pool := startedPool(t, a.URL, b.URL)

	handler := NewBalancedProxy(pool, loadbalancer.NewRoundRobin(pool), "gateway-key")

	var seen []string
	for range 4 {
		recorder := serveAs(handler, "/documents", 42, "user")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", recorder.Code)
		}
		seen = append(seen, recorder.Header().Get("X-Backend"))
	}

	if seen[0] == seen[1] || seen[0] != seen[2] || seen[1] != seen[3] {
		t.Errorf("round-robin order = %v, want alternating", seen)
	}
}

// The trust model of NewTrustedProxy, unchanged behind a pool: identity
// overwritten from the context, the gateway key set, the token stripped,
// the request id carried across the hop.
func TestBalancedProxyKeepsTheTrustedHeaderContract(t *testing.T) {
	a := namedBackend(t, "a")
	pool := startedPool(t, a.URL)

	recorder := serveAs(NewBalancedProxy(pool, loadbalancer.NewRoundRobin(pool), "gateway-key"), "/documents/1", 42, "user")

	want := map[string]string{
		UserIDHeader:               "42",
		UserRoleHeader:             "user",
		GatewayKeyHeader:           "gateway-key",
		"Authorization":            "",
		middleware.RequestIDHeader: "req-123",
	}
	for header, value := range want {
		if got := recorder.Header().Get("X-Seen-" + header); got != value {
			t.Errorf("backend saw %s = %q, want %q", header, got, value)
		}
	}
}

// No identity in context: the client's identity headers must still not get
// through (the ws route establishes identity through the ticket middleware,
// which sets the same keys; a route wired without either forwards none).
func TestBalancedProxyWithoutIdentityForwardsNone(t *testing.T) {
	a := namedBackend(t, "a")
	pool := startedPool(t, a.URL)

	recorder := serveAs(NewBalancedProxy(pool, loadbalancer.NewRoundRobin(pool), "gateway-key"), "/documents", 0, "")

	if got := recorder.Header().Get("X-Seen-" + UserIDHeader); got != "" {
		t.Errorf("X-User-ID %q reached the backend without an identity in context", got)
	}
	if got := recorder.Header().Get("X-Seen-" + UserRoleHeader); got != "" {
		t.Errorf("X-User-Role %q reached the backend without an identity in context", got)
	}
}

// The upstream must see its own host, not the gateway's (prevention rule
// SD-143569): a host-routed backend would otherwise fall through to a
// default vhost.
func TestBalancedProxySetsTheUpstreamHost(t *testing.T) {
	a := namedBackend(t, "a")
	pool := startedPool(t, a.URL)

	recorder := serveAs(NewBalancedProxy(pool, loadbalancer.NewRoundRobin(pool), "gateway-key"), "/documents", 42, "user")

	if got, want := recorder.Header().Get("X-Seen-Host"), pool.Backends()[0].String(); got != want {
		t.Errorf("backend saw Host %q, want its own %q", got, want)
	}
}

func TestBalancedProxyStickyByDocument(t *testing.T) {
	a := namedBackend(t, "a")
	b := namedBackend(t, "b")
	pool := startedPool(t, a.URL, b.URL)

	handler := NewBalancedProxy(pool, loadbalancer.NewConsistentHash(pool, loadbalancer.DocumentKey), "gateway-key")

	for _, id := range []string{"1", "2", "3", "4", "5"} {
		first := serveAs(handler, "/ws?doc="+id, 42, "user").Header().Get("X-Backend")
		for _, target := range []string{"/documents/" + id, "/documents/" + id + "/members/7", "/ws?doc=" + id + "&ticket=x"} {
			if again := serveAs(handler, target, 42, "user").Header().Get("X-Backend"); again != first {
				t.Fatalf("document %s: /ws went to %s but %s went to %s", id, first, target, again)
			}
		}
	}
}

func TestBalancedProxyAnswers503WhenAllDown(t *testing.T) {
	a := namedBackend(t, "a")
	url := a.URL
	a.Close()
	pool := startedPool(t, url)

	recorder := serveAs(NewBalancedProxy(pool, loadbalancer.NewRoundRobin(pool), "gateway-key"), "/documents", 42, "user")

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if body := errorBody(t, recorder); body == "" {
		t.Error("503 body carries no error message")
	}
}

// The sticky miss: only documents hashed to the dead instance fail, with the
// README's message, and nothing is rerouted.
func TestBalancedProxyFailsClosedForDocumentsOnDeadInstance(t *testing.T) {
	a := namedBackend(t, "a")
	b := namedBackend(t, "b")
	pool := startedPool(t, a.URL, b.URL)
	handler := NewBalancedProxy(pool, loadbalancer.NewConsistentHash(pool, loadbalancer.DocumentKey), "gateway-key")

	// Find a document on each instance while both are up.
	onA, onB := "", ""
	for i := 1; onA == "" || onB == ""; i++ {
		id := string(rune('0' + i%10))
		if i >= 10 {
			id = "1" + id
		}
		switch serveAs(handler, "/documents/"+id, 42, "user").Header().Get("X-Backend") {
		case "a":
			onA = id
		case "b":
			onB = id
		}
	}

	b.Close()
	pool.Check()

	recorder := serveAs(handler, "/documents/"+onB, 42, "user")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("document on the dead instance: status = %d, want 503", recorder.Code)
	}
	if got := errorBody(t, recorder); got != "the instance serving this document is unavailable" {
		t.Errorf("503 error = %q", got)
	}

	recorder = serveAs(handler, "/documents/"+onA, 42, "user")
	if recorder.Code != http.StatusOK || recorder.Header().Get("X-Backend") != "a" {
		t.Errorf("document on the live instance: status = %d backend = %q, want 200 from a", recorder.Code, recorder.Header().Get("X-Backend"))
	}
}

// A backend the poller still believes in but that refuses the connection:
// the request was attempted, so this is a 502 with the same JSON shape the
// single-instance proxy used.
func TestBalancedProxyAnswers502WhenTheDialFails(t *testing.T) {
	a := namedBackend(t, "a")
	url := a.URL
	pool, err := loadbalancer.NewPool([]string{url})
	if err != nil {
		t.Fatal(err)
	}
	a.Close() // after NewPool, before any health sweep: still flagged healthy

	recorder := serveAs(NewBalancedProxy(pool, loadbalancer.NewRoundRobin(pool), "gateway-key"), "/documents", 42, "user")

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", recorder.Code)
	}
	if got := errorBody(t, recorder); got != "backend unavailable" {
		t.Errorf("502 error = %q", got)
	}
}

func errorBody(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var body map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, recorder.Body.String())
	}
	return body["error"]
}
