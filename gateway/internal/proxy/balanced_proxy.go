package proxy

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"

	"collab-docs-platform/gateway/internal/loadbalancer"

	"github.com/labstack/echo/v4"
)

// NewBalancedProxy is NewTrustedProxy over a pool of instances: the strategy
// picks one per request, and the request is then proxied under the same
// trust contract. One httputil.ReverseProxy per instance, built up front,
// because the pool's membership is fixed for the life of the process.
func NewBalancedProxy(pool *loadbalancer.Pool, strategy loadbalancer.Strategy, gatewayKey string) echo.HandlerFunc {
	proxies := make(map[*loadbalancer.Backend]*httputil.ReverseProxy, len(pool.Backends()))
	for _, b := range pool.Backends() {
		proxies[b] = newReverseProxy(b.URL)
	}

	return func(c echo.Context) error {
		requestID := forwardIdentity(c, gatewayKey)

		backend, err := strategy.Pick(c.Request())
		if err != nil {
			return respondUnavailable(c, strategy, requestID, err)
		}

		// A strategy built over a different pool than this proxy would
		// return a backend with no proxy here. Nothing at compile time
		// prevents that wiring mistake, so refuse the request instead of
		// dereferencing nil.
		rp, ok := proxies[backend]
		if !ok {
			slog.Error("strategy returned a backend outside this proxy's pool",
				"service", "gateway", "strategy", strategy.Name(), "backend", backend.String(), "request_id", requestID)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "gateway misconfiguration"})
		}

		// backend is the proof the verification asks for: which instance
		// this request_id went to. doc-service logs the same id on its side
		// and names itself in X-Doc-Instance.
		slog.Info("proxying request",
			"service", "gateway",
			"strategy", strategy.Name(),
			"backend", backend.String(),
			"path", c.Request().URL.Path,
			"request_id", requestID,
		)

		rp.ServeHTTP(c.Response(), c.Request())
		return nil
	}
}

// 503, not 502: nothing was attempted upstream. The two errors are logged
// apart because they mean different things operationally - "everything is
// down" versus "this one document's instance is down and we are refusing to
// move the document" (see loadbalancer.ConsistentHash).
func respondUnavailable(c echo.Context, strategy loadbalancer.Strategy, requestID string, err error) error {
	if down, ok := errors.AsType[*loadbalancer.ErrBackendDown](err); ok {
		slog.Warn("sticky instance down, failing closed",
			"service", "gateway",
			"strategy", strategy.Name(),
			"backend", down.Backend.String(),
			"key", down.Key,
			"path", c.Request().URL.Path,
			"request_id", requestID,
		)
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"error": "the instance serving this document is unavailable",
		})
	}

	slog.Error("no healthy instance",
		"service", "gateway",
		"strategy", strategy.Name(),
		"path", c.Request().URL.Path,
		"request_id", requestID,
		"error", err,
	)
	return c.JSON(http.StatusServiceUnavailable, map[string]string{
		"error": "document service unavailable",
	})
}
