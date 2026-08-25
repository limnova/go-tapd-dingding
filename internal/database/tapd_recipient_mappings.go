package database

import (
	"context"
	"fmt"
	"strings"

	"tapd-dingding/internal/config"
)

type TapdRecipientMapping struct {
	ID               int64
	TapdConnectionID int64
	TapdAccount      string
	Name             string
	DingTalkUserID   string
	DingTalkMobile   string
	Enabled          bool
}

func (db *DB) UpsertTapdRecipient(ctx context.Context, mapping TapdRecipientMapping) (int64, error) {
	mapping.TapdAccount = strings.TrimSpace(mapping.TapdAccount)
	mapping.Name = strings.TrimSpace(mapping.Name)
	mapping.DingTalkUserID = strings.TrimSpace(mapping.DingTalkUserID)
	mapping.DingTalkMobile = strings.TrimSpace(mapping.DingTalkMobile)
	if mapping.TapdConnectionID <= 0 {
		return 0, fmt.Errorf("tapd_connection_id must be positive")
	}
	if mapping.TapdAccount == "" {
		return 0, fmt.Errorf("tapd_account is required")
	}
	if mapping.Name == "" {
		mapping.Name = mapping.TapdAccount
	}
	if mapping.DingTalkUserID == "" && mapping.DingTalkMobile == "" {
		return 0, fmt.Errorf("dingtalk_user_id or dingtalk_mobile is required")
	}
	var id int64
	err := db.Pool.QueryRow(ctx, `
INSERT INTO tapd_recipient_mappings(
    tapd_connection_id,tapd_account,name,dingtalk_user_id,dingtalk_mobile,enabled,updated_at
) VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),TRUE,NOW())
ON CONFLICT(tapd_connection_id,tapd_account) DO UPDATE SET
    name=EXCLUDED.name,
    dingtalk_user_id=EXCLUDED.dingtalk_user_id,
    dingtalk_mobile=EXCLUDED.dingtalk_mobile,
    enabled=TRUE,
    updated_at=NOW()
RETURNING id`, mapping.TapdConnectionID, mapping.TapdAccount, mapping.Name, mapping.DingTalkUserID, mapping.DingTalkMobile).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save TAPD recipient mapping: %w", err)
	}
	return id, nil
}

func (db *DB) ListTapdRecipients(ctx context.Context, tapdConnectionID int64) ([]config.Recipient, error) {
	rows, err := db.Pool.Query(ctx, `
SELECT tapd_account,name,COALESCE(dingtalk_user_id,''),COALESCE(dingtalk_mobile,'')
FROM tapd_recipient_mappings
WHERE tapd_connection_id=$1 AND enabled=TRUE
ORDER BY id`, tapdConnectionID)
	if err != nil {
		return nil, fmt.Errorf("list TAPD recipient mappings: %w", err)
	}
	defer rows.Close()
	var recipients []config.Recipient
	for rows.Next() {
		var account, name, userID, mobile string
		if err := rows.Scan(&account, &name, &userID, &mobile); err != nil {
			return nil, fmt.Errorf("scan TAPD recipient mapping: %w", err)
		}
		recipients = append(recipients, config.Recipient{
			TAPDAccounts: []string{account},
			Name:         name,
			UserID:       userID,
			Mobile:       mobile,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate TAPD recipient mappings: %w", err)
	}
	return recipients, nil
}
