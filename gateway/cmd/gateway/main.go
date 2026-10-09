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

	// Month 1: a single doc-service instance behind a plain reverse proxy.
	// Month 3 replaces this with a pool and per-route balancing strategies.
	if len(cfg.DocServiceURLs) > 1 {
		slog.Warn("DOC_SERVICE_URLS lists several instances; month 1 proxies to the first only",
			"using", cfg.DocServiceURLs[0], "ignored", cfg.DocServiceURLs[1:])
	}
	docProxy, err := proxy.NewTrustedProxy(cfg.DocServiceURLs[0], cfg.GatewayKey)
	if err != nil {
		slog.Error("failed to build doc-service proxy", "error", err)
		os.Exit(1)
	}

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

	// doc-service. Authentication happened above; authorization (per-document
	// roles) is doc-service's job, because the gateway does not know what a
	// document is. It receives X-User-ID / X-User-Role and decides.
	e.Any("/documents", docProxy, user...)
	e.Any("/documents/*", docProxy, user...)
	e.GET("/admin/documents", docProxy, admin...)

	// A browser cannot put a Bearer token on a WebSocket handshake, so /ws
	// takes the one-shot ticket from POST /ws-ticket instead. The middleware
	// establishes the identity the same way JWTMiddleware does, the proxy
	// forwards it as headers on the upgrade request, and the ticket never
	// travels past this process. httputil.ReverseProxy pipes the upgraded
	// connection both ways.
	e.GET("/ws", docProxy, gatewayMiddleware.WSTicketMiddleware(tickets))

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
