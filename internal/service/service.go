// Package service coordinates scheduling, persistence, message rendering, and
// external notification delivery.
package service

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"tapd-dingding/internal/config"
	cryptobox "tapd-dingding/internal/crypto"
	"tapd-dingding/internal/database"
)

// Service is the long-running TAPD notification worker.
type Service struct {
	cfg           config.Config
	db            *database.DB
	box           *cryptobox.Box
	logger        *slog.Logger
	location      *time.Location
	dingtalkQueue *dingtalkQueue
	counters      counters
	locks         sync.Map
}

type counters struct {
	scans      atomic.Uint64
	scanErrors atomic.Uint64
	newBugs    atomic.Uint64
	sent       atomic.Uint64
	sendErrors atomic.Uint64
}

// New constructs a Service from validated configuration and dependencies.
func New(cfg config.Config, db *database.DB, box *cryptobox.Box, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	location, err := time.LoadLocation(cfg.Server.Timezone)
	if err != nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return &Service{
		cfg:           cfg,
		db:            db,
		box:           box,
		logger:        logger,
		location:      location,
		dingtalkQueue: newDingTalkQueue(cfg.Server.DingTalkQueueSize, time.Duration(cfg.Server.DingTalkMinInterval), time.Duration(cfg.Server.DingTalkRateLimitRetry), logger),
	}
}
