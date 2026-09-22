package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"time"
)

func run(ctx context.Context, logger *slog.Logger, cfg *config) error {
	ns := cfg.namespace
	client, err := newK8sClient(ns, cfg.insecure)
	if err != nil {
		return fmt.Errorf("failed to connect to cluster: %w", err)
	}
	if cfg.insecure {
		logger.Warn("TLS certificate verification is disabled")
	}

	namespaces := make([]string, 0, len(cfg.targets))
	seen := map[string]bool{}
	for _, t := range cfg.targets {
		if !seen[t.namespace] {
			seen[t.namespace] = true
			namespaces = append(namespaces, t.namespace)
		}
	}
	if len(namespaces) == 0 {
		namespaces = append(namespaces, cfg.namespace)
	}
	logger.Info("connected to cluster",
		"context", client.info.ContextName,
		"server", client.info.ServerURL,
		"namespaces", namespaces,
	)

	manager := newForwardManager(cfg, client, logger)
	diagnosticManager := newDiagnosticManager(ctx, cfg, client, logger, manager.notifyClients)
	defer func() {
		if err := diagnosticManager.stop(); err != nil {
			logger.Warn("diagnostic gateway cleanup failed", "err", err)
		}
	}()
	webSrv := newWebServer(manager, diagnosticManager, client, cfg, logger)

	// start web server
	webErrCh := make(chan error, 1)
	go func() {
		webErrCh <- webSrv.start(ctx)
	}()

	// give the web server a moment to bind before opening the browser
	if cfg.openBrowser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			url := fmt.Sprintf("http://127.0.0.1:%d", cfg.webPort)
			if err := openBrowser(url); err != nil {
				logger.Debug("could not open browser", "err", err)
			}
		}()
	}

	// start watching and forwarding
	fwErrCh := make(chan error, 1)
	go func() {
		fwErrCh <- manager.watchAndSync(ctx)
	}()

	select {
	case err := <-webErrCh:
		return err
	case err := <-fwErrCh:
		return err
	case <-ctx.Done():
		return context.Canceled
	}
}

func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "linux":
		cmd, args = "xdg-open", []string{url}
	case "windows":
		cmd, args = "cmd", []string{"/c", "start", url}
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
	return exec.Command(cmd, args...).Start()
}
