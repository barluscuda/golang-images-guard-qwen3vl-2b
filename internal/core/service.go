package core

import (
	"context"
	"fmt"
	"time"
)

type Service struct {
	repo      ImageRepository
	storage   ImageStorage
	policy    Policy
	modelName string
}

func NewService(repo ImageRepository, storage ImageStorage, policy Policy, modelName string) *Service {
	return &Service{repo: repo, storage: storage, policy: policy, modelName: modelName}
}

func (s *Service) Upload(ctx context.Context, image Image) (string, error) {
	id, err := NewID()
	if err != nil {
		return "", fmt.Errorf("generate image id: %w", err)
	}
	key := id + ".webp"
	if err := s.storage.Put(ctx, key, image.Data); err != nil {
		return "", fmt.Errorf("save image: %w", err)
	}
	now := time.Now().UTC()
	record := ImageRecord{ID: id, StorageKey: key, SizeBytes: int64(len(image.Data)), Width: image.Width,
		Height: image.Height, Status: StatusPending, PolicyText: s.policy.Text(), PolicyHash: s.policy.Hash(),
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

func (s *Service) Get(ctx context.Context, id string) (ImageRecord, error) {
	return s.repo.Get(ctx, id)
}
