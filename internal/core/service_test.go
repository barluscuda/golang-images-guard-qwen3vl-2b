package core

import (
	"context"
	"errors"
	"testing"
)

type failingRepository struct {
	ImageRepository
	exists    bool
	lookupErr error
}

func (r failingRepository) Create(context.Context, ImageRecord) error {
	return errors.New("commit response lost")
}
func (r failingRepository) ExistsStorageKey(context.Context, string) (bool, error) {
	return r.exists, r.lookupErr
}

type cleanupStorage struct {
	ImageStorage
	deleted bool
}

func (*cleanupStorage) Put(context.Context, string, []byte) error { return nil }
func (s *cleanupStorage) Delete(context.Context, string) error    { s.deleted = true; return nil }

func TestUploadCleanupAfterAmbiguousCommit(t *testing.T) {
	policy, err := NewPolicy("RULE A | Example")
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
			service := NewService(failingRepository{exists: tc.exists, lookupErr: tc.lookupErr}, storage, policy, "thinking")
			if _, err := service.Upload(context.Background(), Image{Data: []byte("image"), Width: 1, Height: 1}); err == nil {
				t.Fatal("upload succeeded")
			}
			if storage.deleted != tc.delete {
				t.Fatalf("delete=%v want %v", storage.deleted, tc.delete)
			}
		})
	}
}
