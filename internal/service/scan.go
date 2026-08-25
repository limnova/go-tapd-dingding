package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"tapd-dingding/internal/config"
	"tapd-dingding/internal/tapd"
)

func (s *Service) listBugs(ctx context.Context, monitor config.Monitor, connection tapd.Connection) ([]tapd.Bug, error) {
	client := tapd.NewClient(connection)
	if monitor.BugScope == "mine" {
		return client.ListMyBugs(ctx)
	}
	return client.ListBugs(ctx)
}

// prepareMonitor 加载数据库中的外部连接，并将运行时凭据填充到监控配置。
// 静态配置只保存策略，敏感连接始终从数据库解密读取。
func (s *Service) prepareMonitor(ctx context.Context, monitor config.Monitor) (config.Monitor, tapd.Connection, error) {
	connection, err := s.db.GetTapdConnection(ctx, monitor.TapdConnectionID, s.box)
	if err != nil {
		return monitor, tapd.Connection{}, fmt.Errorf("load TAPD connection: %w", err)
	}

	dingtalkConnection, err := s.db.GetDingTalkConnection(ctx, monitor.DingTalkConnectionID, s.box)
	if err != nil {
		return monitor, tapd.Connection{}, fmt.Errorf("load DingTalk connection: %w", err)
	}

	monitor.Webhook.URL = dingtalkConnection.URL
	monitor.Webhook.Secret = dingtalkConnection.Secret
	monitor, err = s.loadDatabaseRecipients(ctx, monitor, connection.ID)
	if err != nil {
		return monitor, tapd.Connection{}, fmt.Errorf("load TAPD recipient mappings: %w", err)
	}
	return monitor, connection, nil
}

func (s *Service) scan(ctx context.Context, monitor config.Monitor) {
	start := time.Now()
	s.counters.scans.Add(1)
	lock := s.monitorLock(monitor.Name)
	lock.Lock()
	defer lock.Unlock()

	monitor, connection, err := s.prepareMonitor(ctx, monitor)
	if err != nil {
		s.recordScanFailure(ctx, monitor, "prepare monitor failed", err, "tapd_connection_id", monitor.TapdConnectionID, "dingtalk_connection_id", monitor.DingTalkConnectionID)
		return
	}
	logger := s.logger.With("monitor", monitor.Name, "tapd_connection_id", connection.ID)

	bugs, err := s.listBugs(ctx, monitor, connection)
	if err != nil {
		s.recordScanFailure(ctx, monitor, "scan TAPD bugs failed", err)
		return
	}
	initialized, err := s.db.HasSuccessfulScan(ctx, monitor.Name)
	if err != nil {
		logger.Error("read monitor state failed", "error", err)
		return
	}

	sent, skipped, failed := 0, 0, 0
	for _, bug := range bugs {
		if bug.ID == "" {
			continue
		}
		newlySeen, observeErr := s.db.ObserveBug(ctx, monitor.Name, bug)
		if observeErr != nil {
			logger.Error("record TAPD bug observation failed", "bug_id", bug.ID, "error", observeErr)
		} else if newlySeen {
			s.counters.newBugs.Add(1)
			logger.Info("new TAPD bug observed", "bug_id", bug.ID, "source", "scan")
		}

		fingerprint := fingerprintOf(bug, monitor.NotifyOnChanges)
		if !initialized && !monitor.NotifyExisting {
			if err := s.db.SkipNotification(ctx, monitor.Name, bug.ID, fingerprint); err != nil {
				failed++
				logger.Error("seed notification state failed", "bug_id", bug.ID, "error", err)
			} else {
				skipped++
			}
			continue
		}

		claimed, err := s.db.ClaimNotification(ctx, monitor.Name, bug.ID, fingerprint)
		if err != nil {
			failed++
			logger.Error("claim notification failed", "bug_id", bug.ID, "error", err)
			continue
		}
		if !claimed {
			continue
		}
		if err := s.dingtalkQueue.Send(ctx, monitor.Webhook, buildMessage(monitor, bug)); err != nil {
			failed++
			s.counters.sendErrors.Add(1)
			logger.Error("send DingTalk message failed", "bug_id", bug.ID, "error", err)
			if markErr := s.db.MarkFailed(ctx, monitor.Name, bug.ID, fingerprint, err.Error()); markErr != nil {
				logger.Error("mark notification failed state failed", "bug_id", bug.ID, "error", markErr)
			}
			continue
		}
		sent++
		s.counters.sent.Add(1)
		if err := s.db.MarkSent(ctx, monitor.Name, bug.ID, fingerprint); err != nil {
			logger.Error("mark notification sent failed", "bug_id", bug.ID, "error", err)
		}
	}

	if err := s.db.RecordScan(ctx, monitor.Name, nil); err != nil {
		logger.Error("record scan state failed", "error", err)
	}
	logger.Info("scan completed", "bugs", len(bugs), "sent", sent, "skipped", skipped, "failed", failed, "duration", time.Since(start).String())
}

func (s *Service) recordScanFailure(ctx context.Context, monitor config.Monitor, message string, scanErr error, args ...any) {
	s.counters.scanErrors.Add(1)
	logger := s.logger.With("monitor", monitor.Name)
	if len(args)%2 == 0 {
		logger = logger.With(args...)
	}
	logger.Error(message, "error", scanErr)
	if err := s.db.RecordScan(ctx, monitor.Name, scanErr); err != nil {
		logger.Error("record scan error state failed", "error", err)
	}
}

func (s *Service) loadDatabaseRecipients(ctx context.Context, monitor config.Monitor, tapdConnectionID int64) (config.Monitor, error) {
	recipients, err := s.db.ListTapdRecipients(ctx, tapdConnectionID)
	if err != nil {
		return monitor, err
	}
	if len(recipients) == 0 {
		return monitor, nil
	}

	monitor.Recipients = recipients
	if monitor.BugScope != "mine" {
		return monitor, nil
	}
	seen := make(map[string]bool, len(monitor.DefaultRecipients)+len(recipients))
	defaults := make([]string, 0, len(monitor.DefaultRecipients)+len(recipients))
	for _, name := range monitor.DefaultRecipients {
		if name = strings.TrimSpace(name); name != "" {
			defaults = append(defaults, name)
			seen[name] = true
		}
	}
	for _, recipient := range recipients {
		if recipient.Name != "" && !seen[recipient.Name] {
			defaults = append(defaults, recipient.Name)
			seen[recipient.Name] = true
		}
	}
	monitor.DefaultRecipients = defaults
	return monitor, nil
}

func fingerprintOf(b tapd.Bug, includeChanges bool) string {
	h := sha256.New()
	if includeChanges {
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", b.ID, b.Modified, b.Status, b.Title)
	} else {
		_, _ = fmt.Fprint(h, b.ID)
	}
	return hex.EncodeToString(h.Sum(nil))
}
