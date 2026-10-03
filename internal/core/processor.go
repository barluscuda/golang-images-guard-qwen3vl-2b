package core

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Processor struct {
	repo                       ImageRepository
	storage                    ImageStorage
	model                      Model
	lease, timeout, retryDelay time.Duration
	maxAttempts                int
}

func NewProcessor(repo ImageRepository, storage ImageStorage, model Model,
	lease, timeout, retryDelay time.Duration, maxAttempts int) *Processor {
	return &Processor{repo: repo, storage: storage, model: model, lease: lease,
		timeout: timeout, retryDelay: retryDelay, maxAttempts: maxAttempts}
}

// ProcessNext claims durably before inference and commits using a fenced claim token.
func (p *Processor) ProcessNext(ctx context.Context) (bool, error) {
	dbCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	job, err := p.repo.Claim(dbCtx, p.lease, p.maxAttempts)
	cancel()
	if err != nil || job == nil {
		return false, err
	}
	workCtx, workCancel := context.WithTimeout(ctx, p.timeout)
	defer workCancel()
	result, failure := p.assess(workCtx, job.Image)
	// Preserve durable state even when the worker is shutting down.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer finishCancel()
	if failure != nil {
		err = p.repo.RetryOrFail(finishCtx, *job, *failure, p.maxAttempts,
			p.retryDelay*time.Duration(job.Image.Attempts))
	} else {
		err = p.repo.Complete(finishCtx, *job, result)
	}
	if err != nil {
		return true, fmt.Errorf("finish image %s: %w", job.Image.ID, err)
	}
	return true, nil
}

func (p *Processor) assess(ctx context.Context, image ImageRecord) (Assessment, *Failure) {
	policy, err := NewPolicy(image.PolicyText)
	if err != nil || policy.Hash() != image.PolicyHash {
		return Assessment{}, &Failure{Code: "invalid_policy", Message: "Saved policy is invalid."}
	}
	data, err := p.storage.Read(ctx, image.StorageKey)
	if err != nil {
		return Assessment{}, &Failure{Code: "storage_error", Message: "Image could not be read."}
	}
	result, err := p.model.Assess(ctx, Image{Data: data, Width: image.Width, Height: image.Height}, policy, image.Model)
	if err == nil {
		err = ValidateAssessment(result, policy)
	}
	if err != nil {
		code, message := "model_error", "Model assessment failed."
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code, message = "model_timeout", "Model assessment timed out."
		} else if errors.Is(err, ErrInvalidAssessment) {
			code, message = "invalid_assessment", "Model returned an invalid assessment."
		}
		return Assessment{}, &Failure{Code: code, Message: message}
	}
	return result, nil
}
