package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/VesperGlow/cloud/internal/auth"
	"github.com/VesperGlow/cloud/internal/config"
	"github.com/VesperGlow/cloud/internal/database"
	"github.com/VesperGlow/cloud/internal/server"
	"github.com/VesperGlow/cloud/internal/storage"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration invalid", "error", err)
		os.Exit(1)
	}
	db, err := database.Open(cfg.DatabasePath())
	if err != nil {
		log.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	log.Info("database ready", "path", cfg.DatabasePath())
	authService := &auth.Service{DB: db}
	if err := authService.Initialize(context.Background(), cfg.AdminUsername, cfg.AdminPassword); err != nil {
		log.Error("administrator initialization failed", "error", err)
		os.Exit(1)
	}
	store, err := storage.NewS3(context.Background(), cfg)
	if err != nil {
		log.Error("S3 client initialization failed", "error", err)
		os.Exit(1)
	}
	checkCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = store.Ping(checkCtx)
	cancel()
	if err != nil {
		log.Error("S3 connection check failed", "bucket", cfg.S3Bucket, "error", err)
		os.Exit(1)
	}
	log.Info("S3 connection ready", "bucket", cfg.S3Bucket)
	app := server.New(db, store, authService, cfg, log)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				app.CleanupExpiredUploads(context.Background())
				authService.Cleanup(context.Background())
			}
		}
	}()
	app.CleanupExpiredUploads(context.Background())
	authService.Cleanup(context.Background())
	httpServer := &http.Server{Addr: cfg.Addr, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		log.Info("server started", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server stopped unexpectedly", "error", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "error", err)
	} else {
		log.Info("server stopped")
	}
}
