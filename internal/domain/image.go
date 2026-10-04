package domain

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound  = errors.New("image not found")
	ErrLostClaim = errors.New("processing claim lost")
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

// Image is the original image content and its decoded dimensions.
type Image struct {
	Data          []byte
	Width, Height int
}

// ImageRecord is the persisted moderation job and its public assessment state.
type ImageRecord struct {
	ID, StorageKey         string
	SizeBytes              int64
	Width, Height          int
	Status                 Status
	PolicyText, PolicyHash string
	Model                  string
	Attempts               int
	Result                 *Assessment
	Failure                *Failure
	CreatedAt, UpdatedAt   time.Time
}

// Job is an image record claimed by a worker. ClaimToken fences stale workers.
type Job struct {
	Image      ImageRecord
	ClaimToken string
}

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func ValidID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
