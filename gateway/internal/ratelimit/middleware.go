package ratelimit

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

// KeyFunc names the bucket a request counts against. An empty key skips
// the limiter for that request.
type KeyFunc func(c echo.Context) string

// limiterTimeout bounds the Redis round trip a request waits for before
// the limiter gives up and admits it.
const limiterTimeout = 250 * time.Millisecond

// Middleware answers 429 with Retry-After once key's limit is spent. A
// limiter error (Redis down or slow) admits the request and logs it: this
// system chooses availability over strictness when its limiter store fails.
func Middleware(name string, limiter Limiter, key KeyFunc) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			k := key(c)
			if k == "" {
				return next(c)
			}

			ctx, cancel := context.WithTimeout(c.Request().Context(), limiterTimeout)
			decision, err := limiter.Allow(ctx, k)
			cancel()
			if err != nil {
				slog.Warn("rate limiter unavailable, failing open", "limiter", name, "error", err)
				return next(c)
			}
			if !decision.Allowed {
				return TooManyRequests(c, decision.RetryAfter)
			}
			return next(c)
		}
	}
}

// TooManyRequests writes the 429. Retry-After is whole seconds, rounded up
// and at least 1, so a client that honours it is never early.
func TooManyRequests(c echo.Context, retryAfter time.Duration) error {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	c.Response().Header().Set("Retry-After", strconv.Itoa(seconds))
	return c.JSON(http.StatusTooManyRequests, map[string]string{"error": "rate limit exceeded"})
}
