package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mares/kube-port-forward/internal/diagnostic"
)

func main() {
	listen := flag.String("listen", ":1080", "proxy listen address")
	configPath := flag.String("config", "/etc/kube-port-forward/config.yaml", "runtime policy configuration")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := diagnostic.LoadRuntimeConfig(*configPath)
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}
	token := os.Getenv("KPF_PROXY_TOKEN")
	proxy, err := diagnostic.NewProxy(cfg, token, kubernetesServiceIPs(), logger)
	if err != nil {
		logger.Error("failed to configure proxy", "err", err)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:              *listen,
		Handler:           proxy.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       cfg.IdleDuration(),
		MaxHeaderBytes:    8 << 10,
	}
	logger.Info("diagnostic proxy listening", "address", *listen)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("proxy stopped", "err", err)
		os.Exit(1)
	}
}

func kubernetesServiceIPs() []net.IP {
	value := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
	if value == "" {
		return nil
	}
	ip := net.ParseIP(value)
	if ip == nil {
		fmt.Fprintf(os.Stderr, "ignoring invalid KUBERNETES_SERVICE_HOST %q\n", value)
		return nil
	}
	return []net.IP{ip}
}
