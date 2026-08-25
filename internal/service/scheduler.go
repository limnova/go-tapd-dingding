package service

import (
	"context"
	"sync"
	"time"

	"tapd-dingding/internal/config"
)

const (
	dailyReportCheckInterval = 15 * time.Second
	dailyReportWindow        = 2 * time.Minute
)

// Run 启动通知队列，并为每个启用的监控启动扫描和日报任务。
// 上下文取消且所有监控协程退出后返回。
func (s *Service) Run(ctx context.Context) {
	go s.dingtalkQueue.Run(ctx)

	var wg sync.WaitGroup
	for _, monitor := range s.cfg.Monitors {
		if !monitor.Enabled {
			continue
		}
		monitor := monitor
		wg.Add(2)
		go func() {
			defer wg.Done()
			s.runMonitor(ctx, monitor)
		}()
		go func() {
			defer wg.Done()
			s.runDailyReports(ctx, monitor)
		}()
	}
	wg.Wait()
}

func (s *Service) runMonitor(ctx context.Context, monitor config.Monitor) {
	interval := time.Duration(monitor.Interval)
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	s.scan(ctx, monitor)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scan(ctx, monitor)
		}
	}
}

func (s *Service) runDailyReports(ctx context.Context, monitor config.Monitor) {
	ticker := time.NewTicker(dailyReportCheckInterval)
	defer ticker.Stop()

	s.checkDailyReports(ctx, monitor)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkDailyReports(ctx, monitor)
		}
	}
}

func (s *Service) checkDailyReports(ctx context.Context, monitor config.Monitor) {
	now := time.Now().In(s.location)
	for _, reportTime := range monitor.DailyReportTimes {
		scheduled, err := time.ParseInLocation("15:04", reportTime, s.location)
		if err != nil {
			s.logger.Warn("skip invalid daily report time", "monitor", monitor.Name, "report_time", reportTime, "error", err)
			continue
		}
		scheduled = time.Date(now.Year(), now.Month(), now.Day(), scheduled.Hour(), scheduled.Minute(), 0, 0, s.location)
		if now.Before(scheduled) || now.Sub(scheduled) >= dailyReportWindow {
			continue
		}
		s.sendDailyReport(ctx, monitor, reportTime, scheduled)
	}
}

func (s *Service) sendDailyReport(ctx context.Context, monitor config.Monitor, reportTime string, scheduled time.Time) {
	lock := s.monitorLock(monitor.Name)
	lock.Lock()
	defer lock.Unlock()

	claimed, err := s.db.ClaimDailyReport(ctx, monitor.Name, scheduled, reportTime)
	if err != nil {
		s.logger.Error("claim daily report failed", "monitor", monitor.Name, "report_time", reportTime, "error", err)
		return
	}
	if !claimed {
		return
	}

	reportMonitor, connection, err := s.prepareMonitor(ctx, monitor)
	if err != nil {
		s.failDailyReport(ctx, monitor, reportTime, scheduled, err)
		return
	}

	bugs, err := s.listBugs(ctx, monitor, connection)
	if err != nil {
		s.failDailyReport(ctx, monitor, reportTime, scheduled, err)
		return
	}
	for _, bug := range bugs {
		if bug.ID == "" {
			continue
		}
		newlySeen, observeErr := s.db.ObserveBug(ctx, monitor.Name, bug)
		if observeErr != nil {
			s.logger.Error("record TAPD bug observation failed", "monitor", monitor.Name, "bug_id", bug.ID, "error", observeErr)
		} else if newlySeen {
			s.counters.newBugs.Add(1)
			s.logger.Info("new TAPD bug observed", "monitor", monitor.Name, "bug_id", bug.ID, "source", "daily_report")
		}
	}

	message := buildDailyReportMessage(reportMonitor, bugs, scheduled)
	if err := s.dingtalkQueue.Send(ctx, reportMonitor.Webhook, message); err != nil {
		s.counters.sendErrors.Add(1)
		s.failDailyReport(ctx, monitor, reportTime, scheduled, err)
		return
	}
	if err := s.db.MarkDailyReportSent(ctx, monitor.Name, scheduled, reportTime); err != nil {
		s.logger.Error("mark daily report sent failed", "monitor", monitor.Name, "report_time", reportTime, "error", err)
		return
	}
	s.counters.sent.Add(1)
	s.logger.Info("daily report sent", "monitor", monitor.Name, "report_time", reportTime, "bugs", len(bugs))
}

func (s *Service) failDailyReport(ctx context.Context, monitor config.Monitor, reportTime string, reportDate time.Time, reportErr error) {
	s.logger.Error("daily report failed", "monitor", monitor.Name, "report_time", reportTime, "error", reportErr)
	if err := s.db.MarkDailyReportFailed(ctx, monitor.Name, reportDate, reportTime, reportErr.Error()); err != nil {
		s.logger.Error("mark daily report failed state failed", "monitor", monitor.Name, "report_time", reportTime, "error", err)
	}
}

func (s *Service) monitorLock(name string) *sync.Mutex {
	lockValue, _ := s.locks.LoadOrStore(name, &sync.Mutex{})
	return lockValue.(*sync.Mutex)
}
