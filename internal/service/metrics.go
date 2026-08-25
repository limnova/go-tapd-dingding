package service

import (
	"fmt"
	"net/http"
)

func (s *Service) metrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method must be GET")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = fmt.Fprintf(w, "tapd_scans_total %d\ntapd_scan_errors_total %d\ntapd_new_bugs_total %d\ntapd_notifications_sent_total %d\ntapd_notification_errors_total %d\ndingtalk_queue_depth %d\ndingtalk_rate_limit_retries_total %d\n",
		s.counters.scans.Load(),
		s.counters.scanErrors.Load(),
		s.counters.newBugs.Load(),
		s.counters.sent.Load(),
		s.counters.sendErrors.Load(),
		s.dingtalkQueue.QueueDepth(),
		s.dingtalkQueue.RateLimitRetries(),
	)
}
