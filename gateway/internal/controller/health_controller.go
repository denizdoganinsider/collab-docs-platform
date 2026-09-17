package controller

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// Health is liveness only. The gateway is the one public entry point, so this
// must not list internal instances; that view arrives in month 3 behind the
// admin token as /health/backends.
func Health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}
