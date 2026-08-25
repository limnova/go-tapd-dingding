package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	cryptobox "tapd-dingding/internal/crypto"
	"tapd-dingding/internal/dingtalk"
)

type dingtalkConnectionPayload struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"`
}

func (db *DB) UpsertDingTalkConnection(ctx context.Context, conn dingtalk.Connection, box *cryptobox.Box) (int64, error) {
	conn.Name = strings.TrimSpace(conn.Name)
	conn.URL = strings.TrimSpace(conn.URL)
	if err := conn.Validate(); err != nil {
		return 0, err
	}
	payload, err := json.Marshal(dingtalkConnectionPayload{URL: conn.URL, Secret: conn.Secret})
	if err != nil {
		return 0, fmt.Errorf("encode DingTalk connection: %w", err)
	}
	encrypted, err := box.Encrypt(string(payload))
	if err != nil {
		return 0, fmt.Errorf("encrypt DingTalk connection: %w", err)
	}
	var id int64
	err = db.Pool.QueryRow(ctx, `
INSERT INTO dingtalk_connections(name,encrypted_config,enabled,updated_at)
VALUES($1,$2,TRUE,NOW())
ON CONFLICT(name) DO UPDATE SET encrypted_config=EXCLUDED.encrypted_config,enabled=TRUE,updated_at=NOW()
RETURNING id`, conn.Name, encrypted).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save DingTalk connection: %w", err)
	}
	return id, nil
}

func (db *DB) GetDingTalkConnection(ctx context.Context, id int64, box *cryptobox.Box) (dingtalk.Connection, error) {
	var conn dingtalk.Connection
	var encrypted string
	if err := db.Pool.QueryRow(ctx, `SELECT name,encrypted_config FROM dingtalk_connections WHERE id=$1 AND enabled=TRUE`, id).Scan(&conn.Name, &encrypted); err != nil {
		return conn, fmt.Errorf("load DingTalk connection %d: %w", id, err)
	}
	plaintext, err := box.Decrypt(encrypted)
	if err != nil {
		return conn, fmt.Errorf("decrypt DingTalk connection %d: %w", id, err)
	}
	var payload dingtalkConnectionPayload
	if err := json.Unmarshal([]byte(plaintext), &payload); err != nil {
		return conn, fmt.Errorf("decode DingTalk connection %d: %w", id, err)
	}
	conn.ID, conn.URL, conn.Secret = id, payload.URL, payload.Secret
	if err := conn.Validate(); err != nil {
		return dingtalk.Connection{}, fmt.Errorf("invalid DingTalk connection %d: %w", id, err)
	}
	return conn, nil
}
