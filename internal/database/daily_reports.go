package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const dailyReportLease = 5 * time.Minute

// ClaimDailyReport 让定时报表在服务重启和多实例运行时保持幂等。
// 失败的报表允许再次认领。
func (db *DB) ClaimDailyReport(ctx context.Context, monitor string, reportDate time.Time, reportTime string) (bool, error) {
	const q = `
INSERT INTO tapd_daily_reports(monitor_name,report_date,report_time,status,attempts,updated_at)
VALUES($1,$2,$3,'sending',1,NOW())
ON CONFLICT(monitor_name,report_date,report_time) DO UPDATE
SET status='sending',attempts=tapd_daily_reports.attempts+1,last_error=NULL,updated_at=NOW()
WHERE tapd_daily_reports.status = 'failed'
   OR (tapd_daily_reports.status = 'sending' AND tapd_daily_reports.updated_at < NOW() - ($4 * INTERVAL '1 second'))
RETURNING id`
	var id int64
	err := db.Pool.QueryRow(ctx, q, monitor, reportDate.Format("2006-01-02"), reportTime, int64(dailyReportLease/time.Second)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim daily report: %w", err)
	}
	return id > 0, nil
}

func (db *DB) MarkDailyReportSent(ctx context.Context, monitor string, reportDate time.Time, reportTime string) error {
	tag, err := db.Pool.Exec(ctx, `UPDATE tapd_daily_reports SET status='sent',sent_at=NOW(),updated_at=NOW(),last_error=NULL WHERE monitor_name=$1 AND report_date=$2 AND report_time=$3`, monitor, reportDate.Format("2006-01-02"), reportTime)
	if err != nil {
		return fmt.Errorf("mark daily report sent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark daily report sent: report not found")
	}
	return nil
}

func (db *DB) MarkDailyReportFailed(ctx context.Context, monitor string, reportDate time.Time, reportTime, reason string) error {
	tag, err := db.Pool.Exec(ctx, `UPDATE tapd_daily_reports SET status='failed',sent_at=NULL,last_error=$4,updated_at=NOW() WHERE monitor_name=$1 AND report_date=$2 AND report_time=$3`, monitor, reportDate.Format("2006-01-02"), reportTime, reason)
	if err != nil {
		return fmt.Errorf("mark daily report failed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("mark daily report failed: report not found")
	}
	return nil
}
