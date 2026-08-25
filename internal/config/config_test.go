package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadParsesDurationAndEnvironment(t *testing.T) {
	t.Setenv("CONFIG_ENV_TEST", "loaded-value")
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `database:
  dsn: postgres://localhost/test
monitors:
  - name: test
    enabled: true
    interval: 15s
    tapd_connection_id: 1
    dingtalk_connection_id: 1
    webhook:
      message_type: markdown
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if time.Duration(cfg.Monitors[0].Interval) != 15*time.Second {
		t.Fatalf("unexpected interval: %v", cfg.Monitors[0].Interval)
	}
	if cfg.Server.Timezone != "Asia/Shanghai" {
		t.Fatalf("unexpected timezone: %q", cfg.Server.Timezone)
	}
	if len(cfg.Monitors[0].DailyReportTimes) != 2 || cfg.Monitors[0].DailyReportTimes[0] != "09:30" || cfg.Monitors[0].DailyReportTimes[1] != "18:00" {
		t.Fatalf("unexpected daily report times: %v", cfg.Monitors[0].DailyReportTimes)
	}
	if os.Getenv("CONFIG_ENV_TEST") != "loaded-value" {
		t.Fatalf("environment was not loaded")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `database:
  dsn: postgres://localhost/test
  unexpected: true
monitors: []
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unknown configuration field was accepted")
	}
}

func TestValidateRejectsInvalidPoolAndDuplicateReportTimes(t *testing.T) {
	base := Config{
		Database: DatabaseConfig{DSN: "postgres://localhost/test", MaxConns: 1, MinConns: 1},
		Monitors: []Monitor{{
			Name:                 "monitor",
			Enabled:              true,
			TapdConnectionID:     1,
			DingTalkConnectionID: 1,
			DailyReportTimes:     []string{"09:30", "09:30"},
		}},
	}
	if err := base.Validate(); err == nil {
		t.Fatal("duplicate daily report time was accepted")
	}

	base.Monitors[0].DailyReportTimes = nil
	base.Database.MinConns = 2
	if err := base.Validate(); err == nil {
		t.Fatal("min_conns greater than max_conns was accepted")
	}
}
