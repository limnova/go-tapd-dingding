// service 负责协调调度、持久化、消息渲染和外部通知发送。
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

// Service 是长期运行的 TAPD 通知工作器。
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

// New 根据已校验的配置和依赖创建 Service。
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
