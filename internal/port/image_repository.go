package port

import (
	"context"
	"time"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/domain"
)

type ImageRepository interface {
	Create(context.Context, domain.ImageRecord) error
	Get(context.Context, string) (domain.ImageRecord, error)
	Claim(context.Context, time.Duration, int) (*domain.Job, error)
	Complete(context.Context, domain.Job, domain.Assessment) error
	RetryOrFail(context.Context, domain.Job, domain.Failure, int, time.Duration) error
	ExistsStorageKey(context.Context, string) (bool, error)
}
