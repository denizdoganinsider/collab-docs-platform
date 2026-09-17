package middleware

import (
	"log/slog"
	"time"

	"github.com/labstack/echo/v4"
)

// LoggerMiddleware writes one structured line per request. It logs
// URL.Path and never the query string: tickets (month 2) travel in the query,
// and a log file is the wrong place for a credential.
func LoggerMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()

		err := next(c)

		duration := time.Since(start)
		status := c.Response().Status

		requestID, _ := c.Get(RequestIDKey).(string)

		attrs := []slog.Attr{
			slog.String("service", "doc-service"),
			slog.String("method", c.Request().Method),
			slog.String("path", c.Request().URL.Path),
			slog.Int("status", status),
			slog.String("duration", duration.String()),
			slog.String("ip", c.RealIP()),
			slog.String("request_id", requestID),
			slog.String("user_agent", c.Request().UserAgent()),
		}

		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		}
		slog.LogAttrs(c.Request().Context(), level, "request", attrs...)

		return err
	}
}
