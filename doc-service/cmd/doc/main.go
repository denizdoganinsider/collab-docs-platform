package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"collab-docs-platform/doc-service/config"
	"collab-docs-platform/doc-service/internal/controller"
	"collab-docs-platform/doc-service/internal/repository"
	"collab-docs-platform/doc-service/internal/service"
	"collab-docs-platform/doc-service/internal/session"
	"collab-docs-platform/doc-service/internal/ws"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg := config.LoadConfig()

	db := config.NewDatabase(cfg.DBDSN)
	defer db.Close()

	docRepo := repository.NewDocumentRepository(db)
	memberRepo := repository.NewMemberRepository(db)
	opRepo := repository.NewOpRepository(db)

	limits := session.DefaultLimits()
	limits.MaxCodepoints = cfg.DocMaxCodepoints
	limits.RingSize = cfg.OpRingSize
	limits.SnapshotEveryOps = cfg.SnapshotEveryOps
	limits.SnapshotEvery = time.Duration(cfg.SnapshotEverySeconds) * time.Second
	limits.Idle = time.Duration(cfg.SessionIdleSeconds) * time.Second
	sessions := session.NewManager(repository.NewSessionStore(docRepo, opRepo), limits)

	docService := service.NewDocumentService(docRepo, memberRepo, opRepo, sessions)
	memberService := service.NewMemberService(memberRepo)

	var draining atomic.Bool
	e := controller.NewRouter(controller.Dependencies{
		GatewayKey: cfg.GatewayKey,
		InstanceID: cfg.InstanceID,
		Draining:   &draining,
		Documents:  controller.NewDocumentController(docService, sessions),
		Members:    controller.NewMemberController(memberService, sessions),
		WebSocket:  ws.NewHandler(sessions, docService, cfg.WSAllowedOrigins, cfg.OpMaxBytes, cfg.InstanceID).Serve,
	})

	go func() {
		slog.Info("doc-service listening", "port", cfg.ServerPort, "instance", cfg.InstanceID)
		if err := e.Start(":" + cfg.ServerPort); err != nil && err != http.ErrServerClosed {
			slog.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	// Order matters more than any single step. First /health turns 503,
	// while everything else keeps being served; then one health interval
	// passes so the gateway's poller has seen it and stopped routing new
	// connections here; only then do the sessions and the listener go. Any
	// other order has the balancer sending sockets to a process that is
	// about to close them.
	draining.Store(true)
	drain := time.Duration(cfg.ShutdownDrainSeconds) * time.Second
	slog.Info("draining: /health answers 503, sockets stay open", "instance", cfg.InstanceID, "for", drain)
	time.Sleep(drain)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Sessions first: pending ops are written, dirty documents snapshotted
	// and sockets closed with 1001 before the listener goes away.
	if err := sessions.Shutdown(ctx); err != nil {
		slog.Error("session shutdown", "error", err)
	}

	if err := e.Shutdown(ctx); err != nil {
		slog.Error("shutdown", "error", err)
		os.Exit(1)
	}
}
