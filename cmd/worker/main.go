package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	modelapi "github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapter/llamacpp"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapter/postgres"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapter/storage"
	workeradapter "github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/adapter/worker"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/config"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/repository"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/service"
	"go.uber.org/zap"
)

func main() {
	path := flag.String("config", "config/config.yaml", "configuration file")
	flag.Parse()
	if err := run(*path); err != nil {
		fmt.Fprintln(os.Stderr, "guard-worker:", err)
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
	db, err := postgres.Open(connectCtx, cfg.Database.DSN, cfg.Database.MaxOpen, cfg.Database.MaxIdle)
	cancel()
	if err != nil {
		return fmt.Errorf("open PostgreSQL: %w", err)
	}
	defer db.Close()

	repo := repository.NewImage(db.DB())
	store, err := storage.NewLocal(cfg.Storage.Directory, cfg.Upload.MaxBytes)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer store.Close()
	model := modelapi.New(modelapi.Options{BaseURL: cfg.Model.BaseURL, Timeout: cfg.Model.Timeout,
		MaxTokens: cfg.Model.MaxTokens, MaxResponseBytes: cfg.Model.MaxResponseBytes,
		Temperature: cfg.Model.Temperature, TopP: cfg.Model.TopP, Concurrency: cfg.Worker.Concurrency})
	defer model.Close()
	guard := service.NewGuard(repo, store, model, policy, cfg.Model.Name, service.Options{
		Lease: cfg.Worker.LeaseDuration, Timeout: cfg.Model.Timeout,
		RetryDelay: cfg.Worker.RetryDelay, MaxAttempts: cfg.Worker.MaxAttempts,
	})
	background := workeradapter.New(guard, log, cfg.Worker.Concurrency, cfg.Worker.PollInterval)

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		background.Run(signalCtx)
	}()
	go func() {
		defer wg.Done()
		reconcileOrphans(signalCtx, store, repo, log)
	}()
	log.Info("guard worker started", zap.String("model", cfg.Model.Name), zap.Int("workers", cfg.Worker.Concurrency))
	<-signalCtx.Done()
	wg.Wait()
	return nil
}

func reconcileOrphans(ctx context.Context, store *storage.Local, repo *repository.Image, log *zap.Logger) {
	for ctx.Err() == nil {
		pruneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := store.PruneOrphans(pruneCtx, time.Now().Add(-time.Hour), repo.ExistsStorageKey)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Warn("orphan reconciliation failed", zap.Error(err))
		}
		timer := time.NewTimer(time.Hour)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
