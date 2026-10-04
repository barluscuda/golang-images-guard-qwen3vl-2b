package port

import (
	"context"
	"errors"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/domain"
)

var ErrModelUnavailable = errors.New("model unavailable")

type Model interface {
	Assess(ctx context.Context, image domain.Image, policy domain.Policy, modelName string) (domain.Assessment, error)
}
