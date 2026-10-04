package postgres

import (
	"context"
	"fmt"
	"time"

	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Client owns the PostgreSQL connection pool and startup schema check.
type Client struct{ db *gorm.DB }

func Open(ctx context.Context, dsn string, maxOpen, maxIdle int) (*Client, error) {
	db, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{
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
	client := &Client{db: db}
	if err := client.CheckSchema(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) DB() *gorm.DB { return c.db }

func (c *Client) Close() error {
	db, err := c.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func (c *Client) Ping(ctx context.Context) error {
	db, err := c.db.DB()
	if err != nil {
		return err
	}
	return db.PingContext(ctx)
}

func (c *Client) CheckSchema(ctx context.Context) error {
	var migration struct {
		Version int
		Dirty   bool
	}
	if err := c.db.WithContext(ctx).Raw("SELECT version, dirty FROM schema_migrations").Scan(&migration).Error; err != nil {
		return fmt.Errorf("check schema: apply SQL migrations before starting: %w", err)
	}
	if migration.Version != 1 || migration.Dirty {
		return fmt.Errorf("expected clean schema version 1")
	}
	return nil
}
