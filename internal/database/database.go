// database 负责 PostgreSQL 迁移和持久化操作。
package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"tapd-dingding/internal/config"
)

// DB 是应用使用的 PostgreSQL 句柄。
type DB struct{ Pool *pgxpool.Pool }

// Open 创建、验证并迁移 PostgreSQL 连接池。
func Open(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) (*DB, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 5
	}
	if cfg.MinConns < 0 {
		return nil, fmt.Errorf("database min connections cannot be negative")
	}
	if cfg.MinConns > cfg.MaxConns {
		return nil, fmt.Errorf("database min connections cannot exceed max connections")
	}
	if cfg.HealthCheckSecs <= 0 {
		cfg.HealthCheckSecs = 30
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	poolCfg.MaxConns, poolCfg.MinConns = cfg.MaxConns, cfg.MinConns
	poolCfg.HealthCheckPeriod = time.Duration(cfg.HealthCheckSecs) * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	db := &DB{Pool: pool}
	if err := db.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	logger.Info("database ready")
	return db, nil
}
func (db *DB) Close() {
	if db != nil && db.Pool != nil {
		db.Pool.Close()
	}
}

func (db *DB) Migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS tapd_monitor_state (
    monitor_name TEXT PRIMARY KEY,
    last_success_at TIMESTAMPTZ,
    last_error TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS tapd_bug_notifications (
    id BIGSERIAL PRIMARY KEY,
    monitor_name TEXT NOT NULL,
    bug_id TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (monitor_name, bug_id, fingerprint)
);

CREATE INDEX IF NOT EXISTS idx_tapd_bug_notifications_pending
    ON tapd_bug_notifications (monitor_name, status, updated_at);

CREATE TABLE IF NOT EXISTS tapd_connections (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    auth_type TEXT NOT NULL CHECK (auth_type IN ('mcp', 'bearer', 'client_credentials')),
    encrypted_config TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS tapd_recipient_mappings (
    id BIGSERIAL PRIMARY KEY,
    tapd_connection_id BIGINT NOT NULL REFERENCES tapd_connections(id) ON DELETE CASCADE,
    tapd_account TEXT NOT NULL,
    name TEXT NOT NULL,
    dingtalk_user_id TEXT,
    dingtalk_mobile TEXT,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (tapd_connection_id, tapd_account),
    CHECK (dingtalk_user_id IS NOT NULL OR dingtalk_mobile IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS idx_tapd_recipient_mappings_lookup
    ON tapd_recipient_mappings (tapd_connection_id, enabled);

ALTER TABLE tapd_connections DROP CONSTRAINT IF EXISTS tapd_connections_auth_type_check;
UPDATE tapd_connections SET auth_type='mcp' WHERE auth_type <> 'mcp';
ALTER TABLE tapd_connections ADD CONSTRAINT tapd_connections_auth_type_check CHECK (auth_type = 'mcp');

CREATE TABLE IF NOT EXISTS dingtalk_connections (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    encrypted_config TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS tapd_bug_observations (
    monitor_name TEXT NOT NULL,
    bug_id TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tapd_created TEXT,
    tapd_modified TEXT,
    title TEXT,
    status TEXT,
    PRIMARY KEY (monitor_name, bug_id)
);

CREATE INDEX IF NOT EXISTS idx_tapd_bug_observations_first_seen
    ON tapd_bug_observations (monitor_name, first_seen_at);

CREATE TABLE IF NOT EXISTS tapd_daily_reports (
    id BIGSERIAL PRIMARY KEY,
    monitor_name TEXT NOT NULL,
    report_date DATE NOT NULL,
    report_time TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'sending',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    sent_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (monitor_name, report_date, report_time)
);`
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin database migration: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tapd-dingding-schema', 0))`); err != nil {
		return fmt.Errorf("lock database migration: %w", err)
	}
	if _, err := tx.Exec(ctx, schema); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit database migration: %w", err)
	}
	return nil
}
