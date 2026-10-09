package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"collab-docs-platform/gateway/internal/middleware"
	"collab-docs-platform/gateway/internal/service"

	"github.com/labstack/echo/v4"
)

var hexTicket = regexp.MustCompile(`^[0-9a-f]{64}$`)

func issueTicket(t *testing.T, tickets *service.TicketService, ctx map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/ws-ticket", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	for k, v := range ctx {
		c.Set(k, v)
	}
	if err := NewTicketController(tickets).Issue(c); err != nil {
		t.Fatalf("handler error = %v", err)
	}
	return rec
}

func TestTicketController_IssueReturnsRedeemableTicket(t *testing.T) {
	tickets := service.NewTicketService()
	defer tickets.Close()

	rec := issueTicket(t, tickets, map[string]any{
		middleware.UserIDKey: int64(42),
		middleware.RoleKey:   "user",
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", rec.Code, rec.Body.String())
	}
	var out TicketResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if !hexTicket.MatchString(out.Ticket) {
		t.Errorf("ticket = %q, want 64 hex chars", out.Ticket)
	}
	if out.ExpiresIn != 30 {
		t.Errorf("expires_in = %d, want 30", out.ExpiresIn)
	}

	userID, role, ok := tickets.Redeem(out.Ticket)
	if !ok {
		t.Fatal("Redeem rejected the issued ticket")
	}
	if userID != 42 || role != "user" {
		t.Errorf("Redeem = %d/%q, want 42/user", userID, role)
	}
}

func TestTicketController_IssueWithoutIdentityIs401(t *testing.T) {
	tickets := service.NewTicketService()
	defer tickets.Close()

	rec := issueTicket(t, tickets, nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body %s", rec.Code, rec.Body.String())
	}
}
