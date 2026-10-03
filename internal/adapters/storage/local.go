package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/core"
)

type Local struct {
	root     *os.Root
	maxBytes int64
}

func NewLocal(dir string, maxBytes int64) (*Local, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("storage size limit must be positive")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Local{root: root, maxBytes: maxBytes}, nil
}

func (s *Local) Close() error { return s.root.Close() }

func validKey(key string) bool {
	return strings.HasSuffix(key, ".webp") && core.ValidID(strings.TrimSuffix(key, ".webp"))
}

func (s *Local) Put(ctx context.Context, key string, data []byte) error {
	if !validKey(key) || len(data) == 0 || int64(len(data)) > s.maxBytes {
		return fmt.Errorf("invalid image storage input")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := core.NewID()
	if err != nil {
		return err
	}
	temp := ".tmp-" + id
	f, err := s.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = s.root.Remove(temp) }()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Rename(temp, key); err != nil {
		return err
	}
	return s.syncDirectory()
}

func (s *Local) Read(ctx context.Context, key string) ([]byte, error) {
	if !validKey(key) {
		return nil, fmt.Errorf("invalid storage key")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := s.root.Open(key)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > s.maxBytes {
		return nil, fmt.Errorf("invalid stored image")
	}
	data, err := io.ReadAll(io.LimitReader(f, s.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || int64(len(data)) > s.maxBytes {
		return nil, fmt.Errorf("invalid stored image size")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Local) Delete(ctx context.Context, key string) error {
	if !validKey(key) {
		return fmt.Errorf("invalid storage key")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Remove(key); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.syncDirectory()
}

func (s *Local) syncDirectory() error {
	dir, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// PruneOrphans excludes recent writes so uploads can finish their database transaction.
func (s *Local) PruneOrphans(ctx context.Context, cutoff time.Time,
	referenced func(context.Context, string) (bool, error)) error {
	dir, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			key := entry.Name()
			isTemp := strings.HasPrefix(key, ".tmp-") && core.ValidID(strings.TrimPrefix(key, ".tmp-"))
			if !entry.Type().IsRegular() || (!isTemp && !validKey(key)) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.ModTime().Before(cutoff) {
				continue
			}
			if !isTemp {
				lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				exists, err := referenced(lookupCtx, key)
				cancel()
				if err != nil {
					return err
				}
				if exists {
					continue
				}
			}
			if err := s.root.Remove(key); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return s.syncDirectory()
		}
	}
}

var _ core.ImageStorage = (*Local)(nil)
