package controller

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"collab-docs-platform/doc-service/internal/middleware"
)

func TestHealthAnswers503WhileDraining(t *testing.T) {
	var draining atomic.Bool
	e := NewRouter(Dependencies{GatewayKey: "k", InstanceID: "doc-test", Draining: &draining})

	recorder := httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("before SIGTERM: status = %d, want 200", recorder.Code)
	}
	if got := recorder.Body.String(); got != `{"instance":"doc-test","status":"ok"}`+"\n" {
		t.Errorf("before SIGTERM: body = %s", got)
	}

	draining.Store(true)

	recorder = httptest.NewRecorder()
	e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("while draining: status = %d, want 503", recorder.Code)
	}
	if got := recorder.Body.String(); got != `{"instance":"doc-test","status":"draining"}`+"\n" {
		t.Errorf("while draining: body = %s", got)
	}
}

// Every response names the instance, including the ones the router answers
// on its own (404, 401), so "which process said that" never depends on the
// handler remembering to.
func TestEveryResponseCarriesTheInstanceHeader(t *testing.T) {
	e := NewRouter(Dependencies{GatewayKey: "k", InstanceID: "doc-test"})

	for _, target := range []string{"/health", "/nope", "/documents"} {
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if got := recorder.Header().Get(middleware.InstanceHeaderName); got != "doc-test" {
			t.Errorf("GET %s (%d): %s = %q, want doc-test", target, recorder.Code, middleware.InstanceHeaderName, got)
		}
	}
}
