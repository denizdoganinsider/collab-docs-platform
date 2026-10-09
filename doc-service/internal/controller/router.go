package controller

import (
	"net/http"
	"time"

	"collab-docs-platform/doc-service/internal/middleware"

	"github.com/labstack/echo/v4"
)

// Dependencies is what the router needs; main.go builds it, and the
// permission-matrix test builds it against a real database.
type Dependencies struct {
	GatewayKey string
	InstanceID string
	Documents  *DocumentController
	Members    *MemberController
	// WebSocket upgrade handler (package ws); a handler func so this package
	// does not import ws, which imports it.
	WebSocket echo.HandlerFunc
}

// NewRouter wires every route. Only /health is reachable without the
// gateway key: everything else is edge-authenticated.
func NewRouter(deps Dependencies) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	// Slowloris and idle-keepalive bounds. No WriteTimeout: it would cut the
	// WebSocket streams that arrive in month 2.
	e.Server.ReadHeaderTimeout = 10 * time.Second
	e.Server.IdleTimeout = 120 * time.Second

	e.Use(middleware.RequestIDMiddleware)
	e.Use(middleware.LoggerMiddleware)

	e.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok", "instance": deps.InstanceID})
	})

	// Route-level middleware rather than e.Group("", ...): an empty-prefix
	// group registers catch-alls for "/" and "/*" that would answer every
	// unknown path with 401 instead of 404.
	auth := middleware.GatewayAuth(deps.GatewayKey)

	e.POST("/documents", deps.Documents.Create, auth)
	e.GET("/documents", deps.Documents.List, auth)
	e.GET("/documents/:id", deps.Documents.Get, auth)
	e.PATCH("/documents/:id", deps.Documents.Rename, auth)
	e.DELETE("/documents/:id", deps.Documents.Delete, auth)

	e.GET("/documents/:id/ops", deps.Documents.ListOps, auth)

	e.GET("/ws", deps.WebSocket, auth)

	e.GET("/documents/:id/members", deps.Members.List, auth)
	e.PUT("/documents/:id/members/:user_id", deps.Members.Set, auth)
	e.DELETE("/documents/:id/members/:user_id", deps.Members.Remove, auth)

	e.GET("/admin/documents", deps.Documents.ListAll, auth, middleware.RequireAdmin)

	return e
}
