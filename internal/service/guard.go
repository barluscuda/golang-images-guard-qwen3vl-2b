package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/domain"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/port"
)

type Guard struct {
	repo                       port.ImageRepository
	storage                    port.ImageStorage
	model                      port.Model
	policy                     domain.Policy
	modelName                  string
	lease, timeout, retryDelay time.Duration
	maxAttempts                int
}

type Options struct {
	Lease, Timeout, RetryDelay time.Duration
	MaxAttempts                int
}

func NewGuard(repo port.ImageRepository, storage port.ImageStorage, model port.Model,
	policy domain.Policy, modelName string, options Options) *Guard {
	return &Guard{repo: repo, storage: storage, model: model, policy: policy, modelName: modelName,
		lease: options.Lease, timeout: options.Timeout, retryDelay: options.RetryDelay, maxAttempts: options.MaxAttempts}
}

func (s *Guard) Upload(ctx context.Context, image domain.Image) (string, error) {
	id, err := domain.NewID()
	if err != nil {
		return "", fmt.Errorf("generate image id: %w", err)
	}
	key := id + ".webp"
	if err := s.storage.Put(ctx, key, image.Data); err != nil {
		return "", fmt.Errorf("save image: %w", err)
	}
	now := time.Now().UTC()
	record := domain.ImageRecord{ID: id, StorageKey: key, SizeBytes: int64(len(image.Data)), Width: image.Width,
		Height: image.Height, Status: domain.StatusPending, PolicyText: s.policy.Text(), PolicyHash: s.policy.Hash(),
		Model: s.modelName, CreatedAt: now, UpdatedAt: now}
	if err := s.repo.Create(ctx, record); err != nil {
		// A failed response may follow a successful commit. Delete only when absence is confirmed.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		exists, lookupErr := s.repo.ExistsStorageKey(cleanupCtx, key)
		if lookupErr == nil && !exists {
			_ = s.storage.Delete(cleanupCtx, key)
		}
		return "", fmt.Errorf("record image: %w", err)
	}
	return id, nil
}

func (s *Guard) Get(ctx context.Context, id string) (domain.ImageRecord, error) {
	return s.repo.Get(ctx, id)
}

// GetImageFile returns the stored image bytes for a persisted image ID.
func (s *Guard) GetImageFile(ctx context.Context, id string) ([]byte, error) {
	image, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.storage.Read(ctx, image.StorageKey)
}

func (s *Guard) Delete(ctx context.Context, id string) error {
	key, err := s.repo.DeleteCompleted(ctx, id)
	if err != nil {
		return err
	}
	if err := s.storage.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete image file: %w", err)
	}
	return nil
}

// ProcessNext claims durably before inference and commits using a fenced claim token.
func (s *Guard) ProcessNext(ctx context.Context) (bool, error) {
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	job, err := s.repo.Claim(dbCtx, s.lease, s.maxAttempts)
	cancel()
	if err != nil || job == nil {
		return false, err
	}
	workCtx, workCancel := context.WithTimeout(ctx, s.timeout)
	defer workCancel()
	result, failure := s.assess(workCtx, job.Image)
	// Preserve durable state even when the worker is shutting down.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	if failure != nil {
		err = s.repo.RetryOrFail(finishCtx, *job, *failure, s.maxAttempts,
			s.retryDelay*time.Duration(job.Image.Attempts))
	} else {
		err = s.repo.Complete(finishCtx, *job, result)
	}
	if err != nil {
		return true, fmt.Errorf("finish image %s: %w", job.Image.ID, err)
	}
	return true, nil
}

func (s *Guard) assess(ctx context.Context, image domain.ImageRecord) (domain.Assessment, *domain.Failure) {
	policy, err := domain.NewPolicy(image.PolicyText)
	if err != nil || policy.Hash() != image.PolicyHash {
		return domain.Assessment{}, &domain.Failure{Code: "invalid_policy", Message: "Saved policy is invalid."}
	}
	data, err := s.storage.Read(ctx, image.StorageKey)
	if err != nil {
		return domain.Assessment{}, &domain.Failure{Code: "storage_error", Message: "Image could not be read."}
	}
	result, err := s.model.Assess(ctx, domain.Image{Data: data, Width: image.Width, Height: image.Height}, policy, image.Model)
	if err == nil {
		err = domain.ValidateAssessment(result, policy)
	}
	if err != nil {
		code, message := "model_error", "Model assessment failed."
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code, message = "model_timeout", "Model assessment timed out."
		} else if errors.Is(err, domain.ErrInvalidAssessment) {
			code, message = "invalid_assessment", "Model returned an invalid assessment."
		}
		return domain.Assessment{}, &domain.Failure{Code: code, Message: message}
	}
	return result, nil
}
