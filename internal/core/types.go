package core

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound          = errors.New("image not found")
	ErrLostClaim         = errors.New("processing claim lost")
	ErrInvalidAssessment = errors.New("invalid assessment")
	ErrModelUnavailable  = errors.New("model unavailable")
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusProcessing Status = "processing"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

type Rule struct{ ID, Name string }

type Assessment struct {
	Violation bool
	Severity  int
	Rule      *Rule
	Reason    string
}

type Failure struct{ Code, Message string }

type Image struct {
	Data          []byte
	Width, Height int
}

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

func ValidateAssessment(a Assessment, p Policy) error {
	if a.Severity < 0 || a.Severity > 10 || strings.TrimSpace(a.Reason) == "" || len(a.Reason) > 2048 {
		return ErrInvalidAssessment
	}
	if !a.Violation {
		if a.Severity != 0 || a.Rule != nil {
			return ErrInvalidAssessment
		}
		return nil
	}
	if a.Severity == 0 || a.Rule == nil {
		return ErrInvalidAssessment
	}
	name, ok := p.rules[a.Rule.ID]
	if !ok || name != a.Rule.Name {
		return ErrInvalidAssessment
	}
	return nil
}
