package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

type stubRedeemer struct {
	valid  string
	userID int64
	role   string
	spent  int
}

func (s *stubRedeemer) Redeem(ticket string) (int64, string, bool) {
	if ticket != s.valid || s.spent > 0 {
		return 0, "", false
	}
	s.spent++
	return s.userID, s.role, true
}

// passThrough records what the proxied handler would see.
func passThrough(c echo.Context) error {
	userID, _ := c.Get(UserIDKey).(int64)
	role, _ := c.Get(RoleKey).(string)
	return c.JSON(http.StatusOK, map[string]any{
		"user_id": userID, "role": role, "query": c.Request().URL.RawQuery,
	})
}

func TestWSTicketMiddleware(t *testing.T) {
	cases := []struct {
		name   string
		target string
		status int
		body   string
	}{
		{"missing ticket", "/ws?doc=1", http.StatusUnauthorized, `{"error":"missing ticket"}`},
		{"unknown ticket", "/ws?doc=1&ticket=nope", http.StatusUnauthorized, `{"error":"invalid or expired ticket"}`},
		{"valid ticket sets identity and strips itself", "/ws?doc=1&ticket=good", http.StatusOK, `{"query":"doc=1","role":"admin","user_id":42}`},
		{"replayed ticket", "/ws?doc=1&ticket=good", http.StatusUnauthorized, `{"error":"invalid or expired ticket"}`},
	}
	tickets := &stubRedeemer{valid: "good", userID: 42, role: "admin"}
	h := WSTicketMiddleware(tickets)(passThrough)
	e := echo.New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			rec := httptest.NewRecorder()
			if err := h(e.NewContext(req, rec)); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d", rec.Code, tc.status)
			}
			if got := rec.Body.String(); got != tc.body+"\n" {
				t.Fatalf("body %q, want %q", got, tc.body)
			}
		})
	}
}
