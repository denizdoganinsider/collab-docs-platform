package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"collab-docs-platform/gateway/internal/loadbalancer"

	"github.com/labstack/echo/v4"
)

func TestBackendsListsEveryInstanceWithItsHealth(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(up.Close)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	t.Cleanup(down.Close)

	pool, err := loadbalancer.NewPool([]string{up.URL, down.URL})
	if err != nil {
		t.Fatal(err)
	}
	pool.Start(time.Hour)
	t.Cleanup(pool.Close)

	e := echo.New()
	recorder := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/health/backends", nil), recorder)
	if err := Backends(pool)(c); err != nil {
		t.Fatal(err)
	}

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	var body struct {
		DocService []struct {
			Backend string `json:"backend"`
			Healthy bool   `json:"healthy"`
		} `json:"doc_service"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if len(body.DocService) != 2 {
		t.Fatalf("doc_service lists %d instances, want 2: %s", len(body.DocService), recorder.Body.String())
	}
	if body.DocService[0].Backend != pool.Backends()[0].String() || !body.DocService[0].Healthy {
		t.Errorf("first entry = %+v, want %s healthy", body.DocService[0], pool.Backends()[0])
	}
	if body.DocService[1].Backend != pool.Backends()[1].String() || body.DocService[1].Healthy {
		t.Errorf("second entry = %+v, want %s unhealthy", body.DocService[1], pool.Backends()[1])
	}
}
