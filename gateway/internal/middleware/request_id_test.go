package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestRequestIDMiddleware_GeneratesWhenAbsent(t *testing.T) {
	e := echo.New()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := RequestIDMiddleware(func(c echo.Context) error {
		requestID := c.Get(RequestIDKey).(string)
		if len(requestID) != 32 { // 16 bytes = 32 hex chars
			t.Errorf("request_id length = %d, want 32", len(requestID))
		}
		return c.NoContent(http.StatusOK)
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}

	if rec.Header().Get(RequestIDHeader) == "" {
		t.Error("X-Request-ID response header should be set")
	}
}

func TestRequestIDMiddleware_PreservesExisting(t *testing.T) {
	e := echo.New()

	existingID := "my-custom-request-id"
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, existingID)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	handler := RequestIDMiddleware(func(c echo.Context) error {
		if got := c.Get(RequestIDKey).(string); got != existingID {
			t.Errorf("request_id = %q, want %q", got, existingID)
		}
		return c.NoContent(http.StatusOK)
	})

	if err := handler(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}

	if got := rec.Header().Get(RequestIDHeader); got != existingID {
		t.Errorf("X-Request-ID header = %q, want %q", got, existingID)
	}
}
