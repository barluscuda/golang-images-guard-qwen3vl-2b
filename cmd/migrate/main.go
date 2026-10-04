package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const migrationLockID int64 = 719284163205

var migrationFilePattern = regexp.MustCompile(`^([0-9]+)_.+\.up\.sql$`)

type migration struct {
	version int64
	name    string
	sql     string
}

func main() {
	path := flag.String("path", "migrations", "directory containing SQL migrations")
	dsn := flag.String("database", os.Getenv("GUARD_DATABASE_DSN"), "PostgreSQL database URL")
	flag.Parse()
	if err := run(*path, *dsn, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "guard-migrate:", err)
		os.Exit(1)
	}
}

func run(path, dsn string, args []string) error {
	if len(args) > 1 || (len(args) == 1 && args[0] != "up") {
		return fmt.Errorf("only the 'up' command is supported")
	}
	if strings.TrimSpace(dsn) == "" {
		return fmt.Errorf("-database or GUARD_DATABASE_DSN is required")
	}
	migrations, err := loadMigrations(path)
	if err != nil {
		return err
	}

	db, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return fmt.Errorf("open PostgreSQL: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	// A single connection keeps the session-level advisory lock held throughout
	// the migration run.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = sqlDB.PingContext(connectCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	if _, err := sqlDB.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = sqlDB.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	if err := applyMigrations(ctx, db, sqlDB, migrations); err != nil {
		return err
	}
	return nil
}

func loadMigrations(path string) ([]migration, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}
	migrations := make([]migration, 0, len(entries))
	versions := make(map[int64]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := migrationFilePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		if _, exists := versions[version]; exists {
			return nil, fmt.Errorf("duplicate migration version %d", version)
		}
		versions[version] = struct{}{}
		contents, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{version: version, name: entry.Name(), sql: string(contents)})
	}
	if len(migrations) == 0 {
		return nil, fmt.Errorf("no up migrations found in %q", path)
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}

func applyMigrations(ctx context.Context, db *gorm.DB, sqlDB *sql.DB, migrations []migration) error {
	if _, err := sqlDB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version bigint NOT NULL PRIMARY KEY,
		dirty boolean NOT NULL
	)`); err != nil {
		return fmt.Errorf("create migration version table: %w", err)
	}

	var version int64
	var dirty bool
	err := sqlDB.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&version, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := sqlDB.ExecContext(ctx, "INSERT INTO schema_migrations (version, dirty) VALUES (0, false)"); err != nil {
			return fmt.Errorf("initialize migration version: %w", err)
		}
		version = 0
	} else if err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	if dirty {
		return fmt.Errorf("database is marked dirty at migration version %d", version)
	}

	for _, migration := range migrations {
		if migration.version <= version {
			continue
		}
		if _, err := sqlDB.ExecContext(ctx, "UPDATE schema_migrations SET version = $1, dirty = true", migration.version); err != nil {
			return fmt.Errorf("mark migration %q dirty: %w", migration.name, err)
		}
		if err := db.WithContext(ctx).Exec(migration.sql).Error; err != nil {
			return fmt.Errorf("apply migration %q: %w", migration.name, err)
		}
		if _, err := sqlDB.ExecContext(ctx, "UPDATE schema_migrations SET version = $1, dirty = false", migration.version); err != nil {
			return fmt.Errorf("record migration %q: %w", migration.name, err)
		}
		version = migration.version
	}
	return nil
}
