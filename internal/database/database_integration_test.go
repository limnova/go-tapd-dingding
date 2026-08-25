package database

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"tapd-dingding/internal/config"
	cryptobox "tapd-dingding/internal/crypto"
	"tapd-dingding/internal/dingtalk"
	"tapd-dingding/internal/tapd"
)

func TestPostgresIntegration(t *testing.T) {
	if os.Getenv("RUN_DB_INTEGRATION") != "1" {
		t.Skip("set RUN_DB_INTEGRATION=1 to run against PostgreSQL")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Fatal("DATABASE_URL is required")
	}
	db, err := Open(context.Background(), config.DatabaseConfig{DSN: dsn, MaxConns: 2, MinConns: 1, HealthCheckSecs: 5}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var table string
	if err := db.Pool.QueryRow(ctx, `SELECT to_regclass('public.tapd_bug_notifications')`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table != "tapd_bug_notifications" {
		t.Fatalf("migration table not found: %q", table)
	}
	var connectionsTable string
	if err := db.Pool.QueryRow(ctx, `SELECT to_regclass('public.tapd_connections')`).Scan(&connectionsTable); err != nil {
		t.Fatal(err)
	}
	if connectionsTable != "tapd_connections" {
		t.Fatalf("connection table not found: %q", connectionsTable)
	}
	var dingTalkTable string
	if err := db.Pool.QueryRow(ctx, `SELECT to_regclass('public.dingtalk_connections')`).Scan(&dingTalkTable); err != nil {
		t.Fatal(err)
	}
	if dingTalkTable != "dingtalk_connections" {
		t.Fatalf("DingTalk connection table not found: %q", dingTalkTable)
	}
	for _, tableName := range []string{"tapd_bug_observations", "tapd_daily_reports", "tapd_recipient_mappings"} {
		var table string
		if err := db.Pool.QueryRow(ctx, `SELECT to_regclass($1)`, "public."+tableName).Scan(&table); err != nil {
			t.Fatal(err)
		}
		if table != tableName {
			t.Fatalf("table not found: %q", tableName)
		}
	}
	box, err := cryptobox.FromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	connection := tapd.Connection{Name: "__integration_tapd_connection", AccessToken: "token-must-not-be-plaintext"}
	id, err := db.UpsertTapdConnection(ctx, connection, box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Exec(ctx, `DELETE FROM tapd_connections WHERE id=$1`, id)
	loaded, err := db.GetTapdConnection(ctx, id, box)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != connection.AccessToken {
		t.Fatalf("decrypted token mismatch")
	}
	var encrypted string
	if err := db.Pool.QueryRow(ctx, `SELECT encrypted_config FROM tapd_connections WHERE id=$1`, id).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encrypted, connection.AccessToken) {
		t.Fatal("access token was stored in plaintext")
	}
	if strings.Contains(encrypted, connection.AccessToken) {
		t.Fatal("MCP URL was stored in plaintext")
	}
	dingTalkConnection := dingtalk.Connection{Name: "__integration_dingtalk_connection", URL: "https://example.test/robot?access_token=secret-token", Secret: "sign-secret"}
	dingTalkID, err := db.UpsertDingTalkConnection(ctx, dingTalkConnection, box)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Pool.Exec(ctx, `DELETE FROM dingtalk_connections WHERE id=$1`, dingTalkID)
	loadedDingTalk, err := db.GetDingTalkConnection(ctx, dingTalkID, box)
	if err != nil {
		t.Fatal(err)
	}
	if loadedDingTalk.URL != dingTalkConnection.URL || loadedDingTalk.Secret != dingTalkConnection.Secret {
		t.Fatalf("decrypted DingTalk connection mismatch: %+v", loadedDingTalk)
	}
	var encryptedDingTalk string
	if err := db.Pool.QueryRow(ctx, `SELECT encrypted_config FROM dingtalk_connections WHERE id=$1`, dingTalkID).Scan(&encryptedDingTalk); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encryptedDingTalk, dingTalkConnection.URL) || strings.Contains(encryptedDingTalk, dingTalkConnection.Secret) {
		t.Fatal("DingTalk credentials were stored in plaintext")
	}
	bug := tapd.Bug{ID: "integration-bug", Title: "integration", Status: "open", Created: "2026-08-20 09:00:00"}
	first, err := db.ObserveBug(ctx, "__integration_monitor", bug)
	if err != nil {
		t.Fatal(err)
	}
	if !first {
		t.Fatal("first bug observation was not marked new")
	}
	second, err := db.ObserveBug(ctx, "__integration_monitor", bug)
	if err != nil {
		t.Fatal(err)
	}
	if second {
		t.Fatal("second bug observation was marked new")
	}
	defer db.Pool.Exec(ctx, `DELETE FROM tapd_bug_observations WHERE monitor_name=$1`, "__integration_monitor")
	claimedNotification, err := db.ClaimNotification(ctx, "__integration_monitor", bug.ID, "fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if !claimedNotification {
		t.Fatal("notification was not claimed")
	}
	claimedNotification, err = db.ClaimNotification(ctx, "__integration_monitor", bug.ID, "fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if !claimedNotification {
		t.Fatal("pending notification was not reclaimable")
	}
	if err := db.MarkSent(ctx, "__integration_monitor", bug.ID, "fingerprint"); err != nil {
		t.Fatal(err)
	}
	claimedNotification, err = db.ClaimNotification(ctx, "__integration_monitor", bug.ID, "fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	if claimedNotification {
		t.Fatal("sent notification was claimed twice")
	}
	defer db.Pool.Exec(ctx, `DELETE FROM tapd_bug_notifications WHERE monitor_name=$1`, "__integration_monitor")
	reportDate := time.Date(2026, 8, 20, 9, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	claimed, err := db.ClaimDailyReport(ctx, "__integration_monitor", reportDate, "09:30")
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("first daily report was not claimed")
	}
	claimed, err = db.ClaimDailyReport(ctx, "__integration_monitor", reportDate, "09:30")
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("daily report in progress was claimed twice")
	}
	if err := db.MarkDailyReportFailed(ctx, "__integration_monitor", reportDate, "09:30", "temporary failure"); err != nil {
		t.Fatal(err)
	}
	claimed, err = db.ClaimDailyReport(ctx, "__integration_monitor", reportDate, "09:30")
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("failed daily report was not claimable")
	}
	if err := db.MarkDailyReportSent(ctx, "__integration_monitor", reportDate, "09:30"); err != nil {
		t.Fatal(err)
	}
	claimed, err = db.ClaimDailyReport(ctx, "__integration_monitor", reportDate, "09:30")
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("sent daily report was claimed twice")
	}
	defer db.Pool.Exec(ctx, `DELETE FROM tapd_daily_reports WHERE monitor_name=$1`, "__integration_monitor")
}
