package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/domain"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/service"
	"go.uber.org/zap"
)

type Worker struct {
	processor    *service.Guard
	log          *zap.Logger
	concurrency  int
	pollInterval time.Duration
}

func New(processor *service.Guard, log *zap.Logger, concurrency int, pollInterval time.Duration) *Worker {
	return &Worker{processor: processor, log: log, concurrency: concurrency, pollInterval: pollInterval}
}

func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < w.concurrency; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.loop(ctx) }()
	}
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := w.processor.ProcessNext(ctx)
		if err != nil && ctx.Err() == nil {
			if errors.Is(err, domain.ErrLostClaim) {
				w.log.Warn("processing claim expired")
			} else {
				w.log.Error("worker operation failed", zap.Error(err))
			}
		}
		if worked && err == nil {
			continue
		}
		timer := time.NewTimer(w.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
