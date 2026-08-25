package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	cryptobox "tapd-dingding/internal/crypto"
	"tapd-dingding/internal/tapd"
)

type tapdConnectionPayload struct {
	AccessToken string   `json:"access_token"`
	Statuses    []string `json:"statuses,omitempty"`
	Fields      string   `json:"fields,omitempty"`
}

func (db *DB) UpsertTapdConnection(ctx context.Context, conn tapd.Connection, box *cryptobox.Box) (int64, error) {
	conn.Name = strings.TrimSpace(conn.Name)
	conn.AccessToken = strings.TrimSpace(conn.AccessToken)
	if err := conn.Validate(); err != nil {
		return 0, err
	}
	payload, err := json.Marshal(tapdConnectionPayload{
		AccessToken: conn.AccessToken, Statuses: conn.Statuses, Fields: conn.Fields,
	})
	if err != nil {
		return 0, fmt.Errorf("encode TAPD connection: %w", err)
	}
	encrypted, err := box.Encrypt(string(payload))
	if err != nil {
		return 0, fmt.Errorf("encrypt TAPD connection: %w", err)
	}
	var id int64
	err = db.Pool.QueryRow(ctx, `
INSERT INTO tapd_connections(name,auth_type,encrypted_config,enabled,updated_at)
VALUES($1,'mcp',$2,TRUE,NOW())
ON CONFLICT(name) DO UPDATE SET auth_type=EXCLUDED.auth_type,encrypted_config=EXCLUDED.encrypted_config,enabled=TRUE,updated_at=NOW()
RETURNING id`, conn.Name, encrypted).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("save TAPD connection: %w", err)
	}
	return id, nil
}

func (db *DB) GetTapdConnection(ctx context.Context, id int64, box *cryptobox.Box) (tapd.Connection, error) {
	var conn tapd.Connection
	var encrypted string
	if err := db.Pool.QueryRow(ctx, `SELECT name,encrypted_config FROM tapd_connections WHERE id=$1 AND enabled=TRUE`, id).Scan(&conn.Name, &encrypted); err != nil {
		return conn, fmt.Errorf("load TAPD connection %d: %w", id, err)
	}
	plaintext, err := box.Decrypt(encrypted)
	if err != nil {
		return conn, fmt.Errorf("decrypt TAPD connection %d: %w", id, err)
	}
	var payload tapdConnectionPayload
	if err := json.Unmarshal([]byte(plaintext), &payload); err != nil {
		return conn, fmt.Errorf("decode TAPD connection %d: %w", id, err)
	}
	conn.ID, conn.AccessToken = id, payload.AccessToken
	conn.Statuses, conn.Fields = payload.Statuses, payload.Fields
	if err := conn.Validate(); err != nil {
		return tapd.Connection{}, fmt.Errorf("invalid TAPD connection %d: %w", id, err)
	}
	return conn, nil
}
