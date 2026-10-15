package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestInstanceHeaderNamesTheProcessOnEveryResponse(t *testing.T) {
	e := echo.New()
	e.Use(InstanceHeader("doc-test"))
	e.GET("/ok", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) })

	for _, target := range []string{"/ok", "/missing"} {
		recorder := httptest.NewRecorder()
		e.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if got := recorder.Header().Get(InstanceHeaderName); got != "doc-test" {
			t.Errorf("GET %s: %s = %q, want doc-test", target, InstanceHeaderName, got)
		}
	}
}
