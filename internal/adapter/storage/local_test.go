package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalStorageAndReconciliation(t *testing.T) {
	dir := t.TempDir()
	s, err := NewLocal(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	kept := "00000000-0000-4000-8000-000000000001.webp"
	orphan := "00000000-0000-4000-8000-000000000002.webp"
	recent := "00000000-0000-4000-8000-000000000003.webp"
	for _, key := range []string{kept, orphan, recent} {
		if err := s.Put(ctx, key, []byte("image")); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{kept, orphan} {
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(filepath.Join(dir, key), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PruneOrphans(ctx, time.Now().Add(-time.Hour), func(_ context.Context, key string) (bool, error) { return key == kept, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, orphan); !os.IsNotExist(err) {
		t.Fatal("orphan was not removed")
	}
	for _, key := range []string{kept, recent} {
		data, err := s.Read(ctx, key)
		if err != nil || string(data) != "image" {
			t.Fatalf("referenced/recent file changed: %v", err)
		}
	}
	if err := s.Put(ctx, kept, make([]byte, 11)); err == nil {
		t.Fatal("oversized write accepted")
	}
	if _, err := s.Read(ctx, "../secret"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, orphan)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, orphan); err == nil {
		t.Fatal("escaping symlink accepted")
	}
}
