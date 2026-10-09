package controller

import (
	"log/slog"
	"net/http"

	"collab-docs-platform/gateway/internal/middleware"
	"collab-docs-platform/gateway/internal/service"

	"github.com/labstack/echo/v4"
)

type TicketResponse struct {
	Ticket    string `json:"ticket"`
	ExpiresIn int    `json:"expires_in"`
}

type TicketController struct {
	tickets *service.TicketService
}

func NewTicketController(tickets *service.TicketService) *TicketController {
	return &TicketController{tickets: tickets}
}

// Issue handles POST /ws-ticket. Guarded by the same Bearer token as /me: the
// ticket is a delegation of an identity the caller already proved.
func (tc *TicketController) Issue(c echo.Context) error {
	userID, ok := c.Get(middleware.UserIDKey).(int64)
	if !ok {
		return fail(c, http.StatusUnauthorized, "unauthorized")
	}
	role, _ := c.Get(middleware.RoleKey).(string)

	ticket, err := tc.tickets.Issue(userID, role)
	if err != nil {
		slog.Error("failed to issue websocket ticket", "service", "gateway", "user_id", userID, "error", err)
		return fail(c, http.StatusInternalServerError, "failed to issue ticket")
	}

	return c.JSON(http.StatusCreated, TicketResponse{
		Ticket:    ticket,
		ExpiresIn: int(service.TicketTTL.Seconds()),
	})
}
