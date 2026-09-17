package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestAdminMiddleware(t *testing.T) {
	tests := []struct {
		name     string
		role     any
		wantCode int
	}{
		{"admin allowed", "admin", http.StatusOK},
		{"user forbidden", "user", http.StatusForbidden},
		{"no role forbidden", nil, http.StatusForbidden},
		{"wrong type forbidden", 42, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)
			if tt.role != nil {
				c.Set(RoleKey, tt.role)
			}

			handler := AdminMiddleware(func(c echo.Context) error {
				return c.NoContent(http.StatusOK)
			})
			if err := handler(c); err != nil {
				t.Fatalf("handler error = %v", err)
			}
			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
		})
	}
}
