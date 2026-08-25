package database

import (
	"context"
	"fmt"

	"tapd-dingding/internal/tapd"
)

// ObserveBug records the first and most recent time this monitor saw a bug.
// The returned bool is true only for the first observation of this bug.
func (db *DB) ObserveBug(ctx context.Context, monitor string, bug tapd.Bug) (bool, error) {
	var firstSeen bool
	err := db.Pool.QueryRow(ctx, `
WITH inserted AS (
    INSERT INTO tapd_bug_observations(monitor_name,bug_id,tapd_created,tapd_modified,title,status)
    VALUES($1,$2,$3,$4,$5,$6)
    ON CONFLICT(monitor_name,bug_id) DO NOTHING
    RETURNING 1
)
SELECT EXISTS(SELECT 1 FROM inserted)`, monitor, bug.ID, bug.Created, bug.Modified, bug.Title, bug.Status).Scan(&firstSeen)
	if err != nil {
		return false, fmt.Errorf("observe TAPD bug %s: %w", bug.ID, err)
	}
	_, err = db.Pool.Exec(ctx, `
UPDATE tapd_bug_observations
SET last_seen_at=NOW(),tapd_created=$3,tapd_modified=$4,title=$5,status=$6
WHERE monitor_name=$1 AND bug_id=$2`, monitor, bug.ID, bug.Created, bug.Modified, bug.Title, bug.Status)
	if err != nil {
		return false, fmt.Errorf("update TAPD bug observation %s: %w", bug.ID, err)
	}
	return firstSeen, nil
}
