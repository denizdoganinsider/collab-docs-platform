package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestGatewayAuth(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		userID   string
		role     string
		wantCode int
		wantRole string
	}{
		{"valid user", "secret", "42", "user", http.StatusOK, "user"},
		{"valid admin", "secret", "42", "admin", http.StatusOK, "admin"},
		{"unknown role becomes user", "secret", "42", "superuser", http.StatusOK, "user"},
		{"missing role becomes user", "secret", "42", "", http.StatusOK, "user"},
		{"missing key", "", "42", "user", http.StatusUnauthorized, ""},
		{"wrong key", "secre", "42", "user", http.StatusUnauthorized, ""},
		{"key prefix longer", "secret1", "42", "user", http.StatusUnauthorized, ""},
		{"missing user id", "secret", "", "user", http.StatusUnauthorized, ""},
		{"non-numeric user id", "secret", "abc", "user", http.StatusUnauthorized, ""},
		{"zero user id", "secret", "0", "user", http.StatusUnauthorized, ""},
		{"negative user id", "secret", "-1", "user", http.StatusUnauthorized, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/documents", nil)
			if tt.key != "" {
				req.Header.Set(GatewayKeyHeader, tt.key)
			}
			if tt.userID != "" {
				req.Header.Set(UserIDHeader, tt.userID)
			}
			if tt.role != "" {
				req.Header.Set(UserRoleHeader, tt.role)
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			reached := false
			handler := GatewayAuth("secret")(func(c echo.Context) error {
				reached = true
				if got := c.Get(UserIDKey).(int64); got != 42 {
					t.Errorf("user_id = %d, want 42", got)
				}
				if got := c.Get(RoleKey).(string); got != tt.wantRole {
					t.Errorf("role = %q, want %q", got, tt.wantRole)
				}
				return c.NoContent(http.StatusOK)
			})
			if err := handler(c); err != nil {
				t.Fatalf("handler error = %v", err)
			}

			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			if reached != (tt.wantCode == http.StatusOK) {
				t.Errorf("handler reached = %v, want %v", reached, tt.wantCode == http.StatusOK)
			}
		})
	}
}

func TestRequireAdmin(t *testing.T) {
	for _, tt := range []struct {
		role     any
		wantCode int
	}{
		{"admin", http.StatusOK},
		{"user", http.StatusForbidden},
		{nil, http.StatusForbidden},
	} {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/admin/documents", nil), rec)
		if tt.role != nil {
			c.Set(RoleKey, tt.role)
		}
		_ = RequireAdmin(func(c echo.Context) error { return c.NoContent(http.StatusOK) })(c)
		if rec.Code != tt.wantCode {
			t.Errorf("role %v: status = %d, want %d", tt.role, rec.Code, tt.wantCode)
		}
	}
}
