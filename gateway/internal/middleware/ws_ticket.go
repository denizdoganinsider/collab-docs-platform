package middleware

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// TicketRedeemer is the consumer-side view of service.TicketService, declared
// here so middleware does not import service.
type TicketRedeemer interface {
	Redeem(ticket string) (userID int64, role string, ok bool)
}

// WSTicketMiddleware is the JWT middleware's twin for the one route a browser
// cannot put a header on. It redeems ?ticket=<one-shot> and establishes the
// identity in the context exactly as JWTMiddleware does, so the trusted proxy
// forwards X-User-ID / X-User-Role on the outbound upgrade request. The spent
// ticket is stripped from the query: nothing downstream - doc-service, its
// logs, an error page - ever sees a credential in a URL.
func WSTicketMiddleware(tickets TicketRedeemer) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ticket := c.QueryParam("ticket")
			if ticket == "" {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing ticket"})
			}

			userID, role, ok := tickets.Redeem(ticket)
			if !ok {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid or expired ticket"})
			}

			c.Set(UserIDKey, userID)
			c.Set(RoleKey, role)

			query := c.Request().URL.Query()
			query.Del("ticket")
			c.Request().URL.RawQuery = query.Encode()

			return next(c)
		}
	}
}
