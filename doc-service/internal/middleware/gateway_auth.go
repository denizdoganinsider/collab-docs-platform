package middleware

import (
	"crypto/subtle"
	"net/http"
	"strconv"

	"collab-docs-platform/doc-service/internal/domain"

	"github.com/labstack/echo/v4"
)

const (
	GatewayKeyHeader = "X-Gateway-Key"
	UserIDHeader     = "X-User-ID"
	UserRoleHeader   = "X-User-Role"

	UserIDKey = "user_id"
	RoleKey   = "role"
)

// GatewayAuth is edge auth seen from the backend: doc-service never sees a
// token. It trusts X-User-ID because X-Gateway-Key proves the gateway set it.
// The trust is exactly as good as the network boundary - anyone who can
// reach this port AND holds the key can claim any user id - which is why the
// key has no default.
func GatewayAuth(gatewayKey string) echo.MiddlewareFunc {
	expected := []byte(gatewayKey)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			headers := c.Request().Header

			// Constant-time: a byte-by-byte compare would leak the key's
			// prefix through response timing.
			if subtle.ConstantTimeCompare([]byte(headers.Get(GatewayKeyHeader)), expected) != 1 {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing or invalid gateway key"})
			}

			userID, err := strconv.ParseInt(headers.Get(UserIDHeader), 10, 64)
			if err != nil || userID <= 0 {
				return c.JSON(http.StatusUnauthorized, map[string]string{"error": "missing or invalid user id"})
			}

			role := headers.Get(UserRoleHeader)
			if role != domain.RoleAdmin {
				role = domain.RoleUser
			}

			c.Set(UserIDKey, userID)
			c.Set(RoleKey, role)

			return next(c)
		}
	}
}

// RequireAdmin re-checks the role the gateway already enforced: defence in
// depth, so a misrouted admin path is still refused here.
func RequireAdmin(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if role, _ := c.Get(RoleKey).(string); role != domain.RoleAdmin {
			return c.JSON(http.StatusForbidden, map[string]string{"error": "admin access required"})
		}
		return next(c)
	}
}
