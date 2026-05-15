package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lmittmann/tint"
	"k8s.io/klog/v2"
)

func main() {
	cfg, err := parseArgs()
	if err != nil {
		os.Exit(1)
	}

	logger := newLogger(cfg.logLevel)
	klog.SetSlogLogger(logger.WithGroup("klog"))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, logger, cfg); err != nil && err != context.Canceled {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger(level slog.Level) *slog.Logger {
	return slog.New(
		tint.NewHandler(os.Stderr, &tint.Options{
			Level:      level,
			TimeFormat: "15:04:05",
		}),
	)
}
