package controller

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"collab-docs-platform/doc-service/internal/middleware"
	"collab-docs-platform/doc-service/internal/service"

	"github.com/labstack/echo/v4"
)

func fail(c echo.Context, status int, msg string) error {
	return c.JSON(status, map[string]string{"error": msg})
}

// respondError maps the service layer's sentinel errors to HTTP. Unknown
// errors are logged with the request id and answered as a bare 500.
func respondError(c echo.Context, err error) error {
	var ve *service.ValidationError
	switch {
	case errors.Is(err, service.ErrForbidden):
		return fail(c, http.StatusForbidden, "you do not have access to this document")
	case errors.Is(err, service.ErrNotFound):
		return fail(c, http.StatusNotFound, "not found")
	case errors.As(err, &ve):
		return fail(c, http.StatusBadRequest, ve.Msg)
	}

	requestID, _ := c.Get(middleware.RequestIDKey).(string)
	slog.Error("request failed", "service", "doc-service", "path", c.Request().URL.Path, "request_id", requestID, "error", err)
	return fail(c, http.StatusInternalServerError, "internal error")
}

func currentUser(c echo.Context) (int64, string) {
	userID, _ := c.Get(middleware.UserIDKey).(int64)
	role, _ := c.Get(middleware.RoleKey).(string)
	return userID, role
}

func pathID(c echo.Context, name string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(name), 10, 64)
	return id, err == nil && id > 0
}

func queryInt(c echo.Context, name string, fallback int) (int, error) {
	raw := c.QueryParam(name)
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}

// paging parses ?page= and ?per_page=. It does NOT write the response: a
// helper that both writes and returns nil (c.JSON's success value) lets the
// handler keep going after the 400 is already on the wire. The caller maps
// the returned ValidationError with respondError and stops.
func paging(c echo.Context) (page, perPage int, err error) {
	if page, err = queryInt(c, "page", 1); err != nil {
		return 0, 0, &service.ValidationError{Msg: "invalid page parameter"}
	}
	if perPage, err = queryInt(c, "per_page", service.DefaultPerPage); err != nil {
		return 0, 0, &service.ValidationError{Msg: "invalid per_page parameter"}
	}
	return page, perPage, nil
}
