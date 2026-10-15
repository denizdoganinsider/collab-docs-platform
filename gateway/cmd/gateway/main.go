package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"collab-docs-platform/gateway/config"
	"collab-docs-platform/gateway/internal/controller"
	"collab-docs-platform/gateway/internal/loadbalancer"
	"collab-docs-platform/gateway/internal/proxy"
	"collab-docs-platform/gateway/internal/repository"
	"collab-docs-platform/gateway/internal/service"

	gatewayMiddleware "collab-docs-platform/gateway/internal/middleware"

	"github.com/labstack/echo/v4"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg := config.LoadConfig()

	gatewayMiddleware.InitJWT(cfg.JWTSecret)

	db := config.NewDatabase(cfg.DBDSN)
	defer db.Close()

	userRepo := repository.NewUserRepository(db)
	userService := service.NewUserService(userRepo)
	authController := controller.NewAuthController(userService)
	adminController := controller.NewAdminController(userService)
	tickets := service.NewTicketService()
	defer tickets.Close()
	ticketController := controller.NewTicketController(tickets)

	// Every doc-service instance the gateway may route to. The pool is the
	// load balancer's view of the world: fixed membership, live health. The
	// first sweep runs before the listener opens, so no request is ever
	// routed on the optimistic default.
	docPool, err := loadbalancer.NewPool(cfg.DocServiceURLs)
	if err != nil {
		slog.Error("failed to build doc-service pool", "error", err)
		os.Exit(1)
	}
	docPool.Start(time.Duration(cfg.LBHealthIntervalSeconds) * time.Second)
	defer docPool.Close()

	// Two strategies for two kinds of request. The user-scoped list and
	// create are database operations any instance can serve, so they
	// round-robin. Everything about one document - the socket, the live
	// reads, the membership changes that evict a socket - must reach the
	// one process whose memory holds the session, so it hashes on the id.
	docAnyProxy := proxy.NewBalancedProxy(docPool, loadbalancer.NewRoundRobin(docPool), cfg.GatewayKey)
	docStickyProxy := proxy.NewBalancedProxy(docPool, loadbalancer.NewConsistentHash(docPool, loadbalancer.DocumentKey), cfg.GatewayKey)

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	// Slowloris and idle-keepalive bounds. No WriteTimeout: it would cut
	// long-lived proxied streams (WebSocket, month 2).
	e.Server.ReadHeaderTimeout = 10 * time.Second
	e.Server.IdleTimeout = 120 * time.Second

	e.Use(gatewayMiddleware.RequestIDMiddleware)
	e.Use(gatewayMiddleware.LoggerMiddleware)

	e.GET("/health", controller.Health)

	// The editor is a single static file served from the gateway, so the
	// browser and the API share an origin: no CORS, and the WebSocket
	// Origin check in month 2 has exactly one value to allow.
	editorPath := filepath.Join(cfg.StaticDir, "editor.html")
	if _, err := os.Stat(editorPath); err != nil {
		slog.Warn("editor.html not found; GET / will 404", "path", editorPath, "error", err)
	}
	e.File("/", editorPath)

	e.POST("/register", authController.Register)
	e.POST("/login", authController.Login)

	// Everything below validates the Bearer token here, once. This is the
	// only JWT validator in the system; backends get identity headers.
	//
	// Route-level middleware rather than e.Group("", ...): an Echo group with
	// an empty prefix registers catch-all handlers for "/" and "/*" that
	// would shadow GET / above and turn every unknown path into a 401.
	user := []echo.MiddlewareFunc{gatewayMiddleware.JWTMiddleware}
	// Admin routes are user routes that additionally require role=admin in
	// the token, checked before anything is proxied.
	admin := []echo.MiddlewareFunc{gatewayMiddleware.JWTMiddleware, gatewayMiddleware.AdminMiddleware}

	e.GET("/me", authController.Me, user...)
	e.POST("/ws-ticket", ticketController.Issue, user...)
	e.GET("/admin/users", adminController.ListUsers, admin...)
	e.GET("/health/backends", controller.Backends(docPool), admin...)

	// doc-service. Authentication happened above; authorization (per-document
	// roles) is doc-service's job, because the gateway does not know what a
	// document is. It receives X-User-ID / X-User-Role and decides.
	//
	// /documents/* is sticky, not just /ws: a membership removal handled on
	// the wrong instance would leave the evicted user's socket open on the
	// right one, and GET /documents/:id answers from the live session.
	e.Any("/documents", docAnyProxy, user...)
	e.Any("/documents/*", docStickyProxy, user...)
	e.GET("/admin/documents", docAnyProxy, admin...)

	// A browser cannot put a Bearer token on a WebSocket handshake, so /ws
	// takes the one-shot ticket from POST /ws-ticket instead. The middleware
	// establishes the identity the same way JWTMiddleware does, the proxy
	// forwards it as headers on the upgrade request, and the ticket never
	// travels past this process. httputil.ReverseProxy pipes the upgraded
	// connection both ways. Routed by consistent hash on ?doc=, to the
	// instance that also serves that document's HTTP routes.
	e.GET("/ws", docStickyProxy, gatewayMiddleware.WSTicketMiddleware(tickets))

	go func() {
		slog.Info("gateway listening", "port", cfg.ServerPort)
		if err := e.Start(":" + cfg.ServerPort); err != nil && err != http.ErrServerClosed {
			slog.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := e.Shutdown(ctx); err != nil {
		slog.Error("shutdown", "error", err)
		os.Exit(1)
	}
}
