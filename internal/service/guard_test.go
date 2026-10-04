package service

import (
	"context"
	"errors"
	"testing"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/domain"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/port"
)

type failingRepository struct {
	port.ImageRepository
	exists    bool
	lookupErr error
}

func (r failingRepository) Create(context.Context, domain.ImageRecord) error {
	return errors.New("commit response lost")
}
func (r failingRepository) ExistsStorageKey(context.Context, string) (bool, error) {
	return r.exists, r.lookupErr
}

type cleanupStorage struct {
	port.ImageStorage
	deleted bool
}

func (*cleanupStorage) Put(context.Context, string, []byte) error { return nil }
func (s *cleanupStorage) Delete(context.Context, string) error    { s.deleted = true; return nil }

func TestUploadCleanupAfterAmbiguousCommit(t *testing.T) {
	policy, err := domain.NewPolicy("RULE A | Example")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		exists    bool
		lookupErr error
		delete    bool
	}{
		{"confirmed absent", false, nil, true},
		{"committed", true, nil, false},
		{"unknown", false, errors.New("database unavailable"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &cleanupStorage{}
			guard := NewGuard(failingRepository{exists: tc.exists, lookupErr: tc.lookupErr}, storage, nil, policy, "thinking", Options{})
			if _, err := guard.Upload(context.Background(), domain.Image{Data: []byte("image"), Width: 1, Height: 1}); err == nil {
				t.Fatal("upload succeeded")
			}
			if storage.deleted != tc.delete {
				t.Fatalf("delete=%v want %v", storage.deleted, tc.delete)
			}
		})
	}
}
