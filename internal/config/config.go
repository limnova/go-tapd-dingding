// Package config loads and validates the service configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the complete application configuration.
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Monitors []Monitor      `yaml:"monitors"`
}

// ServerConfig controls the HTTP server and DingTalk sending queue.
type ServerConfig struct {
	Listen                 string   `yaml:"listen"`
	Timezone               string   `yaml:"timezone"`
	DingTalkMinInterval    Duration `yaml:"dingtalk_min_interval"`
	DingTalkQueueSize      int      `yaml:"dingtalk_queue_size"`
	DingTalkRateLimitRetry Duration `yaml:"dingtalk_rate_limit_retry"`
}

// DatabaseConfig controls the PostgreSQL connection pool.
type DatabaseConfig struct {
	DSN             string `yaml:"dsn"`
	MaxConns        int32  `yaml:"max_conns"`
	MinConns        int32  `yaml:"min_conns"`
	HealthCheckSecs int    `yaml:"health_check_seconds"`
}

// Monitor describes one periodic TAPD scan and notification policy.
type Monitor struct {
	Name                 string        `yaml:"name"`
	Enabled              bool          `yaml:"enabled"`
	Interval             Duration      `yaml:"interval"`
	BugScope             string        `yaml:"bug_scope"`
	TapdConnectionID     int64         `yaml:"tapd_connection_id"`
	DingTalkConnectionID int64         `yaml:"dingtalk_connection_id"`
	BugURLTemplate       string        `yaml:"bug_url_template"`
	NotifyExisting       bool          `yaml:"notify_existing"`
	NotifyOnChanges      bool          `yaml:"notify_on_changes"`
	Webhook              WebhookConfig `yaml:"webhook"`
	Recipients           []Recipient   `yaml:"recipients"`
	DefaultRecipients    []string      `yaml:"default_recipients"`
	MentionFields        []string      `yaml:"mention_fields"`
	TitlePrefix          string        `yaml:"title_prefix"`
	DailyReportTimes     []string      `yaml:"daily_report_times"`
}

// WebhookConfig controls the message format and size limit. URL and Secret
// are populated from the encrypted database connection at runtime.
type WebhookConfig struct {
	// URL and Secret are populated from the encrypted database connection at
	// runtime. They are intentionally not accepted from YAML.
	URL          string `yaml:"-"`
	Secret       string `yaml:"-"`
	MessageType  string `yaml:"message_type"`
	IncludeDesc  bool   `yaml:"include_description"`
	MaxBodyBytes int    `yaml:"max_body_bytes"`
}

// Recipient maps TAPD accounts to DingTalk identities.
type Recipient struct {
	TAPDAccounts []string `yaml:"tapd_accounts" json:"tapd_accounts"`
	Name         string   `yaml:"name" json:"name"`
	UserID       string   `yaml:"user_id" json:"user_id"`
	Mobile       string   `yaml:"mobile" json:"mobile"`
}

// Duration accepts human-readable YAML durations such as "5m" or an integer
// number of nanoseconds.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Tag == "!!null" {
		*d = 0
		return nil
	}
	if value.Tag == "!!str" {
		parsed, err := time.ParseDuration(value.Value)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", value.Value, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var nanos int64
	if err := value.Decode(&nanos); err != nil {
		return fmt.Errorf("duration must be a string like 5m or nanoseconds: %w", err)
	}
	*d = Duration(nanos)
	return nil
}

// Load reads a YAML configuration file and its sibling .env file.
func Load(path string) (Config, error) {
	if err := loadDotEnv(filepath.Join(filepath.Dir(path), ".env")); err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	decoder := yaml.NewDecoder(strings.NewReader(os.ExpandEnv(string(b))))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse yaml: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Config{}, errors.New("parse yaml: multiple documents are not supported")
		}
		return Config{}, fmt.Errorf("parse yaml: %w", err)
	}
	return cfg, nil
}

// loadDotEnv provides a small dependency-free .env loader for local
// development. Existing process environment variables always win.
func loadDotEnv(path string) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read .env: %w", err)
	}
	for lineNo, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return fmt.Errorf("invalid .env line %d", lineNo+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set .env variable %q: %w", key, err)
			}
		}
	}
	return nil
}

// Validate applies defaults and checks that the configuration is usable.
func (c *Config) Validate() error {
	if err := c.validateServer(); err != nil {
		return err
	}
	if err := c.validateDatabase(); err != nil {
		return err
	}
	if len(c.Monitors) == 0 {
		return errors.New("monitors must contain at least one item")
	}
	seen := map[string]bool{}
	for i := range c.Monitors {
		m := &c.Monitors[i]
		m.Name = strings.TrimSpace(m.Name)
		if m.Name == "" {
			return fmt.Errorf("monitors[%d].name is required", i)
		}
		if seen[m.Name] {
			return fmt.Errorf("duplicate monitor name %q", m.Name)
		}
		seen[m.Name] = true
		if !m.Enabled {
			continue
		}
		if m.TapdConnectionID <= 0 {
			return fmt.Errorf("monitor %q requires tapd_connection_id", m.Name)
		}
		if m.DingTalkConnectionID <= 0 {
			return fmt.Errorf("monitor %q requires dingtalk_connection_id", m.Name)
		}
		if m.Interval == 0 {
			m.Interval = Duration(5 * time.Minute)
		}
		if time.Duration(m.Interval) < time.Second {
			return fmt.Errorf("monitor %q interval must be at least 1s", m.Name)
		}
		m.BugScope = strings.ToLower(strings.TrimSpace(m.BugScope))
		if m.BugScope == "" {
			m.BugScope = "all"
		}
		if m.BugScope != "all" && m.BugScope != "mine" {
			return fmt.Errorf("monitor %q bug_scope must be all or mine", m.Name)
		}
		m.BugURLTemplate = strings.TrimSpace(m.BugURLTemplate)
		if m.BugURLTemplate == "" {
			m.BugURLTemplate = "https://www.tapd.cn/{workspace_id}/bugtrace/bugs/view?bug_id={id}"
		}
		m.TitlePrefix = strings.TrimSpace(m.TitlePrefix)
		m.Webhook.MessageType = strings.ToLower(strings.TrimSpace(m.Webhook.MessageType))
		if m.Webhook.MessageType == "" {
			m.Webhook.MessageType = "markdown"
		}
		if m.Webhook.MessageType != "markdown" && m.Webhook.MessageType != "text" {
			return fmt.Errorf("monitor %q webhook.message_type must be markdown or text", m.Name)
		}
		if m.Webhook.MaxBodyBytes <= 0 {
			m.Webhook.MaxBodyBytes = 18000
		}
		if len(m.MentionFields) == 0 {
			m.MentionFields = []string{"current_owner", "reporter", "de", "fixer", "te", "confirmer", "cc"}
		}
		m.MentionFields = cleanStrings(m.MentionFields)
		m.DefaultRecipients = cleanStrings(m.DefaultRecipients)
		if len(m.DailyReportTimes) == 0 {
			m.DailyReportTimes = []string{"09:30", "18:00"}
		}
		seenReportTimes := map[string]bool{}
		for j, reportTime := range m.DailyReportTimes {
			reportTime = strings.TrimSpace(reportTime)
			if _, err := time.Parse("15:04", reportTime); err != nil {
				return fmt.Errorf("monitor %q daily_report_times contains invalid time %q; expected HH:MM", m.Name, reportTime)
			}
			if seenReportTimes[reportTime] {
				return fmt.Errorf("monitor %q contains duplicate daily report time %q", m.Name, reportTime)
			}
			seenReportTimes[reportTime] = true
			m.DailyReportTimes[j] = reportTime
		}
		for j, r := range m.Recipients {
			r.Name = strings.TrimSpace(r.Name)
			r.UserID = strings.TrimSpace(r.UserID)
			r.Mobile = strings.TrimSpace(r.Mobile)
			r.TAPDAccounts = cleanStrings(r.TAPDAccounts)
			m.Recipients[j] = r
			if r.UserID == "" && r.Mobile == "" {
				return fmt.Errorf("monitor %q recipients[%d] requires user_id or mobile", m.Name, j)
			}
		}
	}
	return nil
}

func (c *Config) validateServer() error {
	c.Server.Listen = strings.TrimSpace(c.Server.Listen)
	if c.Server.Listen == "" {
		c.Server.Listen = ":8080"
	}
	c.Server.Timezone = strings.TrimSpace(c.Server.Timezone)
	if c.Server.Timezone == "" {
		c.Server.Timezone = "Asia/Shanghai"
	}
	if c.Server.DingTalkMinInterval == 0 {
		c.Server.DingTalkMinInterval = Duration(3 * time.Second)
	}
	if time.Duration(c.Server.DingTalkMinInterval) < 100*time.Millisecond {
		return fmt.Errorf("server.dingtalk_min_interval must be at least 100ms")
	}
	if c.Server.DingTalkQueueSize <= 0 {
		c.Server.DingTalkQueueSize = 100
	}
	if c.Server.DingTalkRateLimitRetry == 0 {
		c.Server.DingTalkRateLimitRetry = Duration(30 * time.Second)
	}
	if time.Duration(c.Server.DingTalkRateLimitRetry) < time.Second {
		return fmt.Errorf("server.dingtalk_rate_limit_retry must be at least 1s")
	}
	if _, err := time.LoadLocation(c.Server.Timezone); err != nil {
		return fmt.Errorf("server.timezone is invalid: %w", err)
	}
	return nil
}

func (c *Config) validateDatabase() error {
	c.Database.DSN = strings.TrimSpace(c.Database.DSN)
	if c.Database.DSN == "" {
		return errors.New("database.dsn is required")
	}
	if c.Database.MaxConns <= 0 {
		c.Database.MaxConns = 5
	}
	if c.Database.MinConns < 0 {
		return errors.New("database.min_conns cannot be negative")
	}
	if c.Database.MinConns > c.Database.MaxConns {
		return errors.New("database.min_conns cannot exceed database.max_conns")
	}
	if c.Database.HealthCheckSecs <= 0 {
		c.Database.HealthCheckSecs = 30
	}
	return nil
}

func cleanStrings(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}
