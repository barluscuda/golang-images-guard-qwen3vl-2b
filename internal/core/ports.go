package core

import (
	"context"
	"time"
)

type ImageRepository interface {
	Create(context.Context, ImageRecord) error
	Get(context.Context, string) (ImageRecord, error)
	Claim(context.Context, time.Duration, int) (*Job, error)
	Complete(context.Context, Job, Assessment) error
	RetryOrFail(context.Context, Job, Failure, int, time.Duration) error
	ExistsStorageKey(context.Context, string) (bool, error)
}

type ImageStorage interface {
	Put(context.Context, string, []byte) error
	Read(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}

type Model interface {
	Assess(context.Context, Image, Policy, string) (Assessment, error)
}
