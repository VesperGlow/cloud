package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/VesperGlow/revaro/internal/auth"
	"github.com/VesperGlow/revaro/internal/config"
	"github.com/VesperGlow/revaro/internal/database"
	"github.com/VesperGlow/revaro/internal/server"
	"github.com/VesperGlow/revaro/internal/storage"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if len(os.Args) > 1 {
		if os.Args[1] == "reset-admin" {
			resetAdministrator(log)
			return
		}
		log.Error("unknown command", "command", os.Args[1])
		os.Exit(2)
	}
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
	initialCredentials, err := authService.Initialize(context.Background(), cfg.AdminUsername, cfg.AdminPassword)
	if err != nil {
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
	// One-time re-store of whole objects created before block storage.
	if migrated, err := app.MigrateLegacyObjects(context.Background()); err != nil {
		log.Error("legacy object migration failed; it will be retried on the next start", "error", err)
	} else if migrated > 0 {
		log.Info("legacy objects re-stored as content-addressed blocks", "files", migrated)
	}
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
	if cfg.GCInterval > 0 {
		go func() {
			app.CollectGarbage(context.Background())
			ticker := time.NewTicker(cfg.GCInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					app.CollectGarbage(context.Background())
				}
			}
		}()
	}
	app.CleanupExpiredUploads(context.Background())
	authService.Cleanup(context.Background())
	httpServer := &http.Server{Addr: cfg.Addr, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20}
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Error("server listen failed", "addr", cfg.Addr, "error", err)
		os.Exit(1)
	}
	log.Info("server started", "addr", cfg.Addr)
	if initialCredentials.Generated {
		log.Warn("initial administrator credentials; shown once, sign in and change them immediately", "username", initialCredentials.Username, "password", initialCredentials.Password)
	} else if initialCredentials.Created {
		log.Info("administrator initialized from environment", "username", initialCredentials.Username)
	}
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

func resetAdministrator(log *slog.Logger) {
	dataDir := os.Getenv("APP_DATA_DIR")
	if dataDir == "" {
		dataDir = "/data"
	}
	db, err := database.Open(filepath.Join(dataDir, "revaro.db"))
	if err != nil {
		log.Error("database startup failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	username := os.Getenv("ADMIN_USERNAME")
	if len(os.Args) > 2 {
		username = os.Args[2]
	}
	credentials, err := (&auth.Service{DB: db}).ResetCredentials(context.Background(), username)
	if err != nil {
		log.Error("administrator reset failed", "error", err)
		os.Exit(1)
	}
	log.Warn("administrator credentials reset; shown once, sign in and change them immediately", "username", credentials.Username, "password", credentials.Password)
}
