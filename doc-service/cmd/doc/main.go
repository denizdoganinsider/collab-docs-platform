package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"collab-docs-platform/doc-service/config"
	"collab-docs-platform/doc-service/internal/controller"
	"collab-docs-platform/doc-service/internal/repository"
	"collab-docs-platform/doc-service/internal/service"
	"collab-docs-platform/doc-service/internal/session"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg := config.LoadConfig()

	db := config.NewDatabase(cfg.DBDSN)
	defer db.Close()

	docRepo := repository.NewDocumentRepository(db)
	memberRepo := repository.NewMemberRepository(db)
	opRepo := repository.NewOpRepository(db)

	docService := service.NewDocumentService(docRepo, memberRepo, opRepo)
	memberService := service.NewMemberService(memberRepo)

	limits := session.DefaultLimits()
	limits.MaxCodepoints = cfg.DocMaxCodepoints
	limits.RingSize = cfg.OpRingSize
	limits.SnapshotEveryOps = cfg.SnapshotEveryOps
	limits.SnapshotEvery = time.Duration(cfg.SnapshotEverySeconds) * time.Second
	limits.Idle = time.Duration(cfg.SessionIdleSeconds) * time.Second
	sessions := session.NewManager(repository.NewSessionStore(docRepo, opRepo), limits)

	e := controller.NewRouter(controller.Dependencies{
		GatewayKey: cfg.GatewayKey,
		InstanceID: cfg.InstanceID,
		Documents:  controller.NewDocumentController(docService),
		Members:    controller.NewMemberController(memberService),
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
