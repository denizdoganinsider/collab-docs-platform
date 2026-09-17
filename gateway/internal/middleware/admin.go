package middleware

import (
	"net/http"

	"collab-docs-platform/gateway/internal/domain"

	"github.com/labstack/echo/v4"
)

// AdminMiddleware runs after JWTMiddleware and gates on the role claim. The
// gateway enforces this before proxying; backends re-check X-User-Role for
// defence in depth.
func AdminMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		role, ok := c.Get(RoleKey).(string)
		if !ok || role != domain.RoleAdmin {
			return c.JSON(http.StatusForbidden, map[string]string{
				"error": "admin access required",
			})
		}

		return next(c)
	}
}
