package repository

import (
	"context"
	"errors"
	"time"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/domain"
	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/port"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Image struct{ db *gorm.DB }

func NewImage(db *gorm.DB) *Image { return &Image{db: db} }

type imageRow struct {
	ID, StorageKey                string
	SizeBytes                     int64
	Width, Height                 int
	Status                        string
	PolicyText, PolicyHash, Model string
	Attempts                      int
	AvailableAt                   time.Time
	ClaimToken                    *string
	LeaseUntil                    *time.Time
	Violation                     *bool
	Severity                      *int
	RuleID, RuleName, Reason      *string
	ErrorCode, ErrorMessage       *string
	CreatedAt, UpdatedAt          time.Time
}

func (imageRow) TableName() string { return "images" }

func (r *Image) Create(ctx context.Context, image domain.ImageRecord) error {
	return r.db.WithContext(ctx).Create(&imageRow{ID: image.ID, StorageKey: image.StorageKey,
		SizeBytes: image.SizeBytes, Width: image.Width, Height: image.Height, Status: string(image.Status),
		PolicyText: image.PolicyText, PolicyHash: image.PolicyHash, Model: image.Model,
		AvailableAt: image.CreatedAt, CreatedAt: image.CreatedAt, UpdatedAt: image.UpdatedAt}).Error
}

func (r *Image) Get(ctx context.Context, id string) (domain.ImageRecord, error) {
	var row imageRow
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.ImageRecord{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ImageRecord{}, err
	}
	return row.record(), nil
}

// DeleteCompleted removes a processed image record and returns its storage key.
// Locking the row makes the status check and deletion atomic with worker updates.
func (r *Image) DeleteCompleted(ctx context.Context, id string) (string, error) {
	var storageKey string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row imageRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.Status != string(domain.StatusCompleted) {
			return domain.ErrNotProcessed
		}
		result := tx.Exec("DELETE FROM images WHERE id = ?", id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return domain.ErrNotFound
		}
		storageKey = row.StorageKey
		return nil
	})
	if err != nil {
		return "", err
	}
	return storageKey, nil
}

func (r *Image) ExistsStorageKey(ctx context.Context, key string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&imageRow{}).Where("storage_key = ?", key).Count(&count).Error
	return count > 0, err
}

func (r *Image) Claim(ctx context.Context, lease time.Duration, maxAttempts int) (*domain.Job, error) {
	var job *domain.Job
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Reap expired final attempts in bounded batches, including crashes during inference.
		if err := tx.Exec(`WITH exhausted AS (
			SELECT id FROM images WHERE attempts >= ? AND (status = 'pending'
			OR (status = 'processing' AND lease_until <= CURRENT_TIMESTAMP))
			ORDER BY created_at LIMIT 100 FOR UPDATE SKIP LOCKED
		) UPDATE images SET status = 'failed', claim_token = NULL, lease_until = NULL,
			error_code = 'processing_expired', error_message = 'Processing exhausted its attempts.',
			updated_at = CURRENT_TIMESTAMP WHERE id IN (SELECT id FROM exhausted)`, maxAttempts).Error; err != nil {
			return err
		}
		var row imageRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where(`attempts < ? AND ((status = 'pending' AND available_at <= CURRENT_TIMESTAMP)
			OR (status = 'processing' AND lease_until <= CURRENT_TIMESTAMP))`, maxAttempts).
			Order("created_at, id").Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		token, err := domain.NewID()
		if err != nil {
			return err
		}
		if err := tx.Model(&imageRow{}).Where("id = ?", row.ID).Updates(map[string]any{
			"status": domain.StatusProcessing, "claim_token": token,
			"lease_until": gorm.Expr("CURRENT_TIMESTAMP + ? * INTERVAL '1 millisecond'", lease.Milliseconds()),
			"attempts":    gorm.Expr("attempts + 1"), "updated_at": gorm.Expr("CURRENT_TIMESTAMP"),
		}).Error; err != nil {
			return err
		}
		row.Status = string(domain.StatusProcessing)
		row.Attempts++
		job = &domain.Job{Image: row.record(), ClaimToken: token}
		return nil
	})
	return job, err
}

func (r *Image) Complete(ctx context.Context, job domain.Job, assessment domain.Assessment) error {
	var ruleID, ruleName any
	if assessment.Rule != nil {
		ruleID, ruleName = assessment.Rule.ID, assessment.Rule.Name
	}
	return r.finish(ctx, job, map[string]any{
		"status": domain.StatusCompleted, "violation": assessment.Violation, "severity": assessment.Severity,
		"rule_id": ruleID, "rule_name": ruleName, "reason": assessment.Reason,
		"error_code": nil, "error_message": nil,
	})
}

func (r *Image) RetryOrFail(ctx context.Context, job domain.Job, failure domain.Failure,
	maxAttempts int, delay time.Duration) error {
	values := map[string]any{"status": domain.StatusPending, "error_code": nil, "error_message": nil,
		"available_at": gorm.Expr("CURRENT_TIMESTAMP + ? * INTERVAL '1 millisecond'", delay.Milliseconds())}
	if job.Image.Attempts >= maxAttempts {
		values["status"], values["error_code"], values["error_message"] = domain.StatusFailed, failure.Code, failure.Message
	}
	return r.finish(ctx, job, values)
}

func (r *Image) finish(ctx context.Context, job domain.Job, values map[string]any) error {
	values["claim_token"], values["lease_until"] = nil, nil
	values["updated_at"] = gorm.Expr("CURRENT_TIMESTAMP")
	result := r.db.WithContext(ctx).Model(&imageRow{}).
		Where("id = ? AND status = 'processing' AND claim_token = ? AND lease_until > CURRENT_TIMESTAMP", job.Image.ID, job.ClaimToken).
		Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return domain.ErrLostClaim
	}
	return nil
}

func (row imageRow) record() domain.ImageRecord {
	image := domain.ImageRecord{ID: row.ID, StorageKey: row.StorageKey, SizeBytes: row.SizeBytes,
		Width: row.Width, Height: row.Height, Status: domain.Status(row.Status), PolicyText: row.PolicyText,
		PolicyHash: row.PolicyHash, Model: row.Model, Attempts: row.Attempts, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.Violation != nil && row.Severity != nil && row.Reason != nil {
		image.Result = &domain.Assessment{Violation: *row.Violation, Severity: *row.Severity, Reason: *row.Reason}
		if row.RuleID != nil && row.RuleName != nil {
			image.Result.Rule = &domain.Rule{ID: *row.RuleID, Name: *row.RuleName}
		}
	}
	if row.ErrorCode != nil && row.ErrorMessage != nil {
		image.Failure = &domain.Failure{Code: *row.ErrorCode, Message: *row.ErrorMessage}
	}
	return image
}

var _ port.ImageRepository = (*Image)(nil)
