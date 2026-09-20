package controller

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"collab-docs-platform/gateway/internal/middleware"
	"collab-docs-platform/gateway/internal/service"

	"github.com/labstack/echo/v4"
)

func fail(c echo.Context, status int, msg string) error {
	return c.JSON(status, map[string]string{"error": msg})
}

// respondError maps the service layer's errors to HTTP. A client mistake is a
// 4xx with its message; anything else is an infrastructure failure, logged
// with the request id and answered as a bare 500 - never as a 4xx carrying a
// driver's internals, which would hide an outage behind "bad request".
func respondError(c echo.Context, err error) error {
	var ve *service.ValidationError
	switch {
	case errors.As(err, &ve):
		return fail(c, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, service.ErrEmailTaken):
		return fail(c, http.StatusConflict, err.Error())
	case errors.Is(err, service.ErrInvalidCredentials):
		return fail(c, http.StatusUnauthorized, err.Error())
	case errors.Is(err, sql.ErrNoRows):
		return fail(c, http.StatusNotFound, "not found")
	}

	requestID, _ := c.Get(middleware.RequestIDKey).(string)
	slog.Error("request failed", "service", "gateway", "path", c.Request().URL.Path, "request_id", requestID, "error", err)
	return fail(c, http.StatusInternalServerError, "internal error")
}
