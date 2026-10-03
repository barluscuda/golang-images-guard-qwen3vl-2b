package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/barluscuda/golang-images-guard-qwen3vl-2b/internal/core"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type Repository struct{ db *gorm.DB }

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

func Open(ctx context.Context, dsn string, maxOpen, maxIdle int) (*Repository, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	r := &Repository{db: db}
	if err := r.CheckSchema(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return r, nil
}

func (r *Repository) Close() error {
	db, err := r.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}
func (r *Repository) Ping(ctx context.Context) error {
	db, err := r.db.DB()
	if err != nil {
		return err
	}
	return db.PingContext(ctx)
}

func (r *Repository) CheckSchema(ctx context.Context) error {
	var migration struct {
		Version int
		Dirty   bool
	}
	if err := r.db.WithContext(ctx).Raw("SELECT version, dirty FROM schema_migrations").Scan(&migration).Error; err != nil {
		return fmt.Errorf("check schema: apply SQL migrations before starting: %w", err)
	}
	if migration.Version != 1 || migration.Dirty {
		return fmt.Errorf("expected clean schema version 1")
	}
	return nil
}

func (r *Repository) Create(ctx context.Context, image core.ImageRecord) error {
	return r.db.WithContext(ctx).Create(&imageRow{ID: image.ID, StorageKey: image.StorageKey,
		SizeBytes: image.SizeBytes, Width: image.Width, Height: image.Height, Status: string(image.Status),
		PolicyText: image.PolicyText, PolicyHash: image.PolicyHash, Model: image.Model,
		AvailableAt: image.CreatedAt, CreatedAt: image.CreatedAt, UpdatedAt: image.UpdatedAt}).Error
}

func (r *Repository) Get(ctx context.Context, id string) (core.ImageRecord, error) {
	var row imageRow
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return core.ImageRecord{}, core.ErrNotFound
	}
	if err != nil {
		return core.ImageRecord{}, err
	}
	return row.record(), nil
}

func (r *Repository) ExistsStorageKey(ctx context.Context, key string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&imageRow{}).Where("storage_key = ?", key).Count(&count).Error
	return count > 0, err
}

func (r *Repository) Claim(ctx context.Context, lease time.Duration, maxAttempts int) (*core.Job, error) {
	var job *core.Job
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
		token, err := core.NewID()
		if err != nil {
			return err
		}
		if err := tx.Model(&imageRow{}).Where("id = ?", row.ID).Updates(map[string]any{
			"status": core.StatusProcessing, "claim_token": token,
			"lease_until": gorm.Expr("CURRENT_TIMESTAMP + ? * INTERVAL '1 millisecond'", lease.Milliseconds()),
			"attempts":    gorm.Expr("attempts + 1"), "updated_at": gorm.Expr("CURRENT_TIMESTAMP"),
		}).Error; err != nil {
			return err
		}
		row.Status = string(core.StatusProcessing)
		row.Attempts++
		job = &core.Job{Image: row.record(), ClaimToken: token}
		return nil
	})
	return job, err
}

func (r *Repository) Complete(ctx context.Context, job core.Job, assessment core.Assessment) error {
	var ruleID, ruleName any
	if assessment.Rule != nil {
		ruleID, ruleName = assessment.Rule.ID, assessment.Rule.Name
	}
	return r.finish(ctx, job, map[string]any{
		"status": core.StatusCompleted, "violation": assessment.Violation, "severity": assessment.Severity,
		"rule_id": ruleID, "rule_name": ruleName, "reason": assessment.Reason,
		"error_code": nil, "error_message": nil,
	})
}

func (r *Repository) RetryOrFail(ctx context.Context, job core.Job, failure core.Failure,
	maxAttempts int, delay time.Duration) error {
	values := map[string]any{"status": core.StatusPending, "error_code": nil, "error_message": nil,
		"available_at": gorm.Expr("CURRENT_TIMESTAMP + ? * INTERVAL '1 millisecond'", delay.Milliseconds())}
	if job.Image.Attempts >= maxAttempts {
		values["status"], values["error_code"], values["error_message"] = core.StatusFailed, failure.Code, failure.Message
	}
	return r.finish(ctx, job, values)
}

func (r *Repository) finish(ctx context.Context, job core.Job, values map[string]any) error {
	values["claim_token"], values["lease_until"] = nil, nil
	values["updated_at"] = gorm.Expr("CURRENT_TIMESTAMP")
	result := r.db.WithContext(ctx).Model(&imageRow{}).
		Where("id = ? AND status = 'processing' AND claim_token = ? AND lease_until > CURRENT_TIMESTAMP", job.Image.ID, job.ClaimToken).
		Updates(values)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return core.ErrLostClaim
	}
	return nil
}

func (row imageRow) record() core.ImageRecord {
	image := core.ImageRecord{ID: row.ID, StorageKey: row.StorageKey, SizeBytes: row.SizeBytes,
		Width: row.Width, Height: row.Height, Status: core.Status(row.Status), PolicyText: row.PolicyText,
		PolicyHash: row.PolicyHash, Model: row.Model, Attempts: row.Attempts, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.Violation != nil && row.Severity != nil && row.Reason != nil {
		image.Result = &core.Assessment{Violation: *row.Violation, Severity: *row.Severity, Reason: *row.Reason}
		if row.RuleID != nil && row.RuleName != nil {
			image.Result.Rule = &core.Rule{ID: *row.RuleID, Name: *row.RuleName}
		}
	}
	if row.ErrorCode != nil && row.ErrorMessage != nil {
		image.Failure = &core.Failure{Code: *row.ErrorCode, Message: *row.ErrorMessage}
	}
	return image
}

var _ core.ImageRepository = (*Repository)(nil)
