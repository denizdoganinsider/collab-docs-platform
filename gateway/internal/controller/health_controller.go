package controller

import (
	"net/http"

	"collab-docs-platform/gateway/internal/loadbalancer"

	"github.com/labstack/echo/v4"
)

// Health is liveness only. The gateway is the one public entry point, so this
// must not list internal instances; that view is Backends, behind the admin
// token.
func Health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// Backends is what the load balancer knows about the pool, so "which
// instance does the gateway think is down" is one curl away. Admin only:
// internal host:port pairs and which of them are degraded is topology, and
// an anonymous caller has no business with it.
func Backends(pool *loadbalancer.Pool) echo.HandlerFunc {
	return func(c echo.Context) error {
		backends := make([]map[string]any, 0, len(pool.Backends()))
		for _, b := range pool.Backends() {
			backends = append(backends, map[string]any{"backend": b.String(), "healthy": b.Healthy()})
		}
		return c.JSON(http.StatusOK, map[string]any{"doc_service": backends})
	}
}
