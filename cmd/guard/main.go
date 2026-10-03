package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	httpapi "github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapters/http"
	modelapi "github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapters/llamacpp"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapters/postgres"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapters/storage"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapters/worker"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/config"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/core"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func main() {
	path := flag.String("config", "config/config.yaml", "configuration file")
	flag.Parse()
	if err := run(*path); err != nil {
		fmt.Fprintln(os.Stderr, "guard:", err)
		os.Exit(1)
	}
}

func run(path string) error {
	cfg, policy, err := config.Load(path)
	if err != nil {
		return err
	}
	logConfig := zap.NewProductionConfig()
	if err := logConfig.Level.UnmarshalText([]byte(cfg.Log.Level)); err != nil {
		return err
	}
	log, err := logConfig.Build()
	if err != nil {
		return err
	}
	defer func() { _ = log.Sync() }()
	connectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	repo, err := postgres.Open(connectCtx, cfg.Database.DSN, cfg.Database.MaxOpen, cfg.Database.MaxIdle)
	cancel()
	if err != nil {
		return fmt.Errorf("open PostgreSQL: %w", err)
	}
	defer repo.Close()
	store, err := storage.NewLocal(cfg.Storage.Directory, cfg.Upload.MaxBytes)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer store.Close()
	model := modelapi.New(modelapi.Options{BaseURL: cfg.Model.BaseURL, Timeout: cfg.Model.Timeout,
		MaxTokens: cfg.Model.MaxTokens, MaxResponseBytes: cfg.Model.MaxResponseBytes,
		Temperature: cfg.Model.Temperature, TopP: cfg.Model.TopP, Concurrency: cfg.Worker.Concurrency})
	defer model.Close()
	service := core.NewService(repo, store, policy, cfg.Model.Name)
	processor := core.NewProcessor(repo, store, model, cfg.Worker.LeaseDuration, cfg.Model.Timeout, cfg.Worker.RetryDelay, cfg.Worker.MaxAttempts)
	background := worker.New(processor, log, cfg.Worker.Concurrency, cfg.Worker.PollInterval)
	gin.SetMode(gin.ReleaseMode)
	handler := httpapi.NewHandler(service, httpapi.UploadLimits{MaxBytes: cfg.Upload.MaxBytes,
		MaxLongSide: cfg.Upload.MaxLongSide, MaxShortSide: cfg.Upload.MaxShortSide, MaxConcurrent: cfg.Upload.MaxConcurrent})
	router, err := httpapi.NewRouter(handler, log, repo.Ping)
	if err != nil {
		return err
	}
	server := &http.Server{Addr: cfg.Server.Address, Handler: router, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: cfg.Server.ReadTimeout, WriteTimeout: cfg.Server.WriteTimeout, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); background.Run(workerCtx) }()
	go func() {
		defer wg.Done()
		// Reconcile once on startup, then hourly. Never delete files younger than an hour.
		for {
			pruneCtx, cancel := context.WithTimeout(workerCtx, 30*time.Second)
			err := store.PruneOrphans(pruneCtx, time.Now().Add(-time.Hour), repo.ExistsStorageKey)
			cancel()
			if err != nil && workerCtx.Err() == nil {
				log.Warn("orphan reconciliation failed", zap.Error(err))
			}
			timer := time.NewTimer(time.Hour)
			select {
			case <-workerCtx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	log.Info("guard started", zap.String("address", cfg.Server.Address), zap.String("model", cfg.Model.Name),
		zap.String("policy_hash", policy.Hash()), zap.Int("workers", cfg.Worker.Concurrency))
	var serveErr error
	select {
	case <-signalCtx.Done():
	case serveErr = <-serverErrors:
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shutdownCancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = server.Close()
	}
	stopWorkers()
	wg.Wait()
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return shutdownErr
}
