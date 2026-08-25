package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tapd-dingding/internal/config"
	cryptobox "tapd-dingding/internal/crypto"
	"tapd-dingding/internal/database"
	"tapd-dingding/internal/logging"
	"tapd-dingding/internal/service"
)

func main() {
	configPath := flag.String("config", "config.yaml", "configuration file path")
	command := flag.String("command", "run", "command: run or generate-key")
	flag.Parse()
	logger := logging.New()
	switch *command {
	case "generate-key":
		key, err := cryptobox.GenerateKey()
		if err != nil {
			logger.Error("generate encryption key failed", "error", err)
			os.Exit(1)
		}
		fmt.Println(key)
		return
	case "run":
	default:
		logger.Error("invalid command", "command", *command)
		os.Exit(2)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load config failed", "error", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		logger.Error("invalid config", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := database.Open(ctx, cfg.Database, logger)
	if err != nil {
		logger.Error("open database failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	box, err := cryptobox.FromEnvironment()
	if err != nil {
		logger.Error("load application encryption key failed", "error", err)
		os.Exit(1)
	}
	worker := service.New(cfg, db, box, logger)
	server := &http.Server{Addr: cfg.Server.Listen, Handler: worker.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		logger.Info("http server started", "addr", cfg.Server.Listen)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped unexpectedly", "error", err)
			stop()
		}
	}()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		worker.Run(ctx)
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server shutdown failed", "error", err)
	}
	select {
	case <-workerDone:
	case <-shutdownCtx.Done():
		logger.Error("worker shutdown timed out")
	}
	logger.Info("service stopped")
}
