// Package logging 提供应用统一的结构化日志初始化。
package logging

import (
	"log/slog"
	"os"
	"strings"
)

// New 根据环境变量创建应用日志器。
// 默认输出 JSON；本地可设置 LOG_FORMAT=text 获得更适合人阅读的格式。
func New() *slog.Logger {
	options := &slog.HandlerOptions{Level: parseLevel(os.Getenv("LOG_LEVEL"))}
	format := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT")))
	if format == "text" || format == "pretty" {
		return slog.New(slog.NewTextHandler(os.Stdout, options))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, options))
}

func parseLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
