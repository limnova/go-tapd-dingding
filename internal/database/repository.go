package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (db *DB) ClaimNotification(ctx context.Context, monitor, bugID, fingerprint string) (bool, error) {
	const q = `INSERT INTO tapd_bug_notifications (monitor_name, bug_id, fingerprint, status, attempts) VALUES ($1,$2,$3,'pending',1) ON CONFLICT (monitor_name,bug_id,fingerprint) DO UPDATE SET status='pending',attempts=tapd_bug_notifications.attempts+1,updated_at=NOW() WHERE tapd_bug_notifications.status <> 'sent' RETURNING id`
	var id int64
	if err := db.Pool.QueryRow(ctx, q, monitor, bugID, fingerprint).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("claim notification: %w", err)
	}
	return id > 0, nil
}
func (db *DB) MarkSent(ctx context.Context, monitor, bugID, fingerprint string) error {
	tag, err := db.Pool.Exec(ctx, `UPDATE tapd_bug_notifications SET status='sent',sent_at=NOW(),updated_at=NOW(),last_error=NULL WHERE monitor_name=$1 AND bug_id=$2 AND fingerprint=$3`, monitor, bugID, fingerprint)
	if err != nil {
		return fmt.Errorf("mark notification sent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark notification sent: notification not found")
	}
	return nil
}
func (db *DB) MarkFailed(ctx context.Context, monitor, bugID, fingerprint, reason string) error {
	tag, err := db.Pool.Exec(ctx, `UPDATE tapd_bug_notifications SET status='failed',sent_at=NULL,last_error=$4,updated_at=NOW() WHERE monitor_name=$1 AND bug_id=$2 AND fingerprint=$3`, monitor, bugID, fingerprint, reason)
	if err != nil {
		return fmt.Errorf("mark notification failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark notification failed: notification not found")
	}
	return nil
}
func (db *DB) RecordScan(ctx context.Context, monitor string, scanErr error) error {
	if scanErr == nil {
		_, err := db.Pool.Exec(ctx, `INSERT INTO tapd_monitor_state(monitor_name,last_success_at,last_error,updated_at) VALUES($1,NOW(),NULL,NOW()) ON CONFLICT(monitor_name) DO UPDATE SET last_success_at=NOW(),last_error=NULL,updated_at=NOW()`, monitor)
		if err != nil {
			return fmt.Errorf("record successful scan: %w", err)
		}
		return nil
	}
	_, err := db.Pool.Exec(ctx, `INSERT INTO tapd_monitor_state(monitor_name,last_error,updated_at) VALUES($1,$2,NOW()) ON CONFLICT(monitor_name) DO UPDATE SET last_error=$2,updated_at=NOW()`, monitor, scanErr.Error())
	if err != nil {
		return fmt.Errorf("record failed scan: %w", err)
	}
	return nil
}
func (db *DB) Ready(ctx context.Context) error {
	if err := db.Pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

func (db *DB) HasSuccessfulScan(ctx context.Context, monitor string) (bool, error) {
	var exists bool
	err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tapd_monitor_state WHERE monitor_name=$1 AND last_success_at IS NOT NULL)`, monitor).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("read monitor state: %w", err)
	}
	return exists, nil
}

func (db *DB) SkipNotification(ctx context.Context, monitor, bugID, fingerprint string) error {
	_, err := db.Pool.Exec(ctx, `INSERT INTO tapd_bug_notifications(monitor_name,bug_id,fingerprint,status,sent_at,updated_at) VALUES($1,$2,$3,'sent',NOW(),NOW()) ON CONFLICT(monitor_name,bug_id,fingerprint) DO NOTHING`, monitor, bugID, fingerprint)
	if err != nil {
		return fmt.Errorf("seed notification state: %w", err)
	}
	return nil
}
