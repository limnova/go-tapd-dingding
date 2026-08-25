package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"tapd-dingding/internal/config"
	"tapd-dingding/internal/dingtalk"
)

const maxDingTalkRateLimitRetries = 2

type dingtalkSendJob struct {
	ctx     context.Context
	webhook config.WebhookConfig
	message dingtalk.Message
	result  chan error
}

type dingtalkQueue struct {
	jobs             chan dingtalkSendJob
	minInterval      time.Duration
	rateLimitRetry   time.Duration
	logger           *slog.Logger
	send             func(context.Context, config.WebhookConfig, dingtalk.Message) error
	pending          atomic.Int64
	rateLimitRetries atomic.Uint64
	nextSendAt       time.Time
}

func newDingTalkQueue(size int, minInterval, rateLimitRetry time.Duration, logger *slog.Logger) *dingtalkQueue {
	if size <= 0 {
		size = 100
	}
	if minInterval <= 0 {
		minInterval = 3 * time.Second
	}
	if rateLimitRetry <= 0 {
		rateLimitRetry = 30 * time.Second
	}
	return &dingtalkQueue{
		jobs:           make(chan dingtalkSendJob, size),
		minInterval:    minInterval,
		rateLimitRetry: rateLimitRetry,
		logger:         logger,
		send: func(ctx context.Context, webhook config.WebhookConfig, message dingtalk.Message) error {
			return dingtalk.NewClient(webhook).Send(ctx, message)
		},
	}
}

func (q *dingtalkQueue) Run(ctx context.Context) {
	if ctx == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-q.jobs:
			if !ok {
				return
			}
			err := q.process(job)
			q.pending.Add(-1)
			job.result <- err
		}
	}
}

func (q *dingtalkQueue) Send(ctx context.Context, webhook config.WebhookConfig, message dingtalk.Message) error {
	if ctx == nil {
		return errors.New("DingTalk send context is nil")
	}
	job := dingtalkSendJob{ctx: ctx, webhook: webhook, message: message, result: make(chan error, 1)}
	q.pending.Add(1)
	select {
	case q.jobs <- job:
	case <-ctx.Done():
		q.pending.Add(-1)
		return ctx.Err()
	}
	select {
	case err := <-job.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *dingtalkQueue) process(job dingtalkSendJob) error {
	for attempt := 0; attempt <= maxDingTalkRateLimitRetries; attempt++ {
		if err := q.waitForTurn(job.ctx); err != nil {
			return err
		}
		send := q.send
		if send == nil {
			send = func(ctx context.Context, webhook config.WebhookConfig, message dingtalk.Message) error {
				return dingtalk.NewClient(webhook).Send(ctx, message)
			}
		}
		err := send(job.ctx, job.webhook, job.message)
		if err == nil {
			return nil
		}
		if !isDingTalkRateLimitError(err) || attempt == maxDingTalkRateLimitRetries {
			return err
		}
		q.rateLimitRetries.Add(1)
		delay := q.rateLimitRetry * time.Duration(1<<attempt)
		if delay > 2*time.Minute {
			delay = 2 * time.Minute
		}
		if q.logger != nil {
			q.logger.Warn("DingTalk rate limit reached; retrying queued message", "retry_in", delay.String(), "attempt", attempt+1)
		}
		if err := waitContext(job.ctx, delay); err != nil {
			return err
		}
	}
	return errors.New("DingTalk send queue exhausted retries")
}

func (q *dingtalkQueue) waitForTurn(ctx context.Context) error {
	if wait := time.Until(q.nextSendAt); wait > 0 {
		if err := waitContext(ctx, wait); err != nil {
			return err
		}
	}
	q.nextSendAt = time.Now().Add(q.minInterval)
	return nil
}

func (q *dingtalkQueue) QueueDepth() int64 { return q.pending.Load() }

func (q *dingtalkQueue) RateLimitRetries() uint64 { return q.rateLimitRetries.Load() }

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isDingTalkRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "660026") || strings.Contains(message, "too many messages per minute")
}

func (q *dingtalkQueue) String() string {
	return fmt.Sprintf("pending=%d retries=%d", q.QueueDepth(), q.RateLimitRetries())
}
