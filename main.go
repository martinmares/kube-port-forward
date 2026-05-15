package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lmittmann/tint"
)

const (
	retriesAlways        = -1
	defaultRetryInterval = 5
)

type config struct {
	retries       int // retriesAlways or max number of total attempts
	retryInterval time.Duration
	logLevel      slog.Level
	kubectlArgs   []string
}

func main() {
	cfg, err := parseArgs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.logLevel)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, logger, cfg); err != nil && err != context.Canceled {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func parseArgs() (*config, error) {
	fs := flag.NewFlagSet("kube-port-forward", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kube-port-forward [OPTIONS] -- <kubectl port-forward args...>\n\n")
		fmt.Fprintf(os.Stderr, "Wrapper around 'kubectl port-forward' with automatic reconnect.\n")
		fmt.Fprintf(os.Stderr, "'port-forward' is added automatically — do not include it in the args.\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  kube-port-forward -- -n nac-app pod/nac-out-xxx 9090:9090\n")
		fmt.Fprintf(os.Stderr, "  kube-port-forward --retries 5 --retry-interval 10 -- -n nac-app svc/my-svc 8080:80\n")
	}

	retriesStr := fs.String("retries", "always", "max reconnect attempts: integer ≥1 or 'always' for infinite")
	retryInterval := fs.Int("retry-interval", defaultRetryInterval, "seconds to wait between reconnect attempts")
	logLevelStr := fs.String("log-level", "info", "log verbosity: debug, info, warn, error")

	// Split on "--" separator; everything after goes to kubectl.
	sep := -1
	for i, a := range os.Args[1:] {
		if a == "--" {
			sep = i + 1
			break
		}
	}

	var wrapperArgs, kubectlArgs []string
	if sep == -1 {
		kubectlArgs = os.Args[1:]
	} else {
		wrapperArgs = os.Args[1:sep]
		kubectlArgs = os.Args[sep+1:]
	}

	if err := fs.Parse(wrapperArgs); err != nil {
		return nil, err
	}

	if len(kubectlArgs) == 0 {
		fs.Usage()
		return nil, fmt.Errorf("no kubectl arguments provided")
	}

	var retries int
	switch *retriesStr {
	case "always", "0":
		retries = retriesAlways
	default:
		if _, err := fmt.Sscanf(*retriesStr, "%d", &retries); err != nil || retries < 1 {
			return nil, fmt.Errorf("--retries must be a positive integer or 'always'")
		}
	}

	logLevel, err := parseLogLevel(*logLevelStr)
	if err != nil {
		return nil, err
	}

	return &config{
		retries:       retries,
		retryInterval: time.Duration(*retryInterval) * time.Second,
		logLevel:      logLevel,
		kubectlArgs:   kubectlArgs,
	}, nil
}

func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q", s)
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

func run(ctx context.Context, logger *slog.Logger, cfg *config) error {
	for attempt := 1; ; attempt++ {
		maxLabel := "always"
		if cfg.retries != retriesAlways {
			maxLabel = fmt.Sprintf("%d", cfg.retries)
		}
		logger.Info("starting port-forward",
			"attempt", fmt.Sprintf("%d/%s", attempt, maxLabel),
			"cmd", "kubectl port-forward "+strings.Join(cfg.kubectlArgs, " "),
		)

		lostConn, err := runKubectl(ctx, logger, cfg.kubectlArgs)

		if ctx.Err() != nil {
			logger.Info("shutting down")
			return context.Canceled
		}

		if err != nil && !lostConn {
			return fmt.Errorf("kubectl exited unexpectedly: %w", err)
		}

		if lostConn {
			logger.Warn("lost connection to pod")
		} else {
			logger.Warn("kubectl exited")
		}

		if cfg.retries != retriesAlways && attempt >= cfg.retries {
			return fmt.Errorf("reached maximum attempts (%d)", cfg.retries)
		}

		logger.Info("reconnecting", "in", cfg.retryInterval)
		select {
		case <-ctx.Done():
			return context.Canceled
		case <-time.After(cfg.retryInterval):
		}
	}
}

// runKubectl runs kubectl and streams its output. Returns (lostConnection, error).
func runKubectl(ctx context.Context, logger *slog.Logger, args []string) (bool, error) {
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"port-forward"}, args...)...)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return false, err
	}

	if err := cmd.Start(); err != nil {
		return false, err
	}

	lostConn := false
	var wg sync.WaitGroup

	// kubectl stdout: informational lines (Forwarding from, Handling connection)
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "Forwarding from"):
				logger.Info("[kubectl] " + line)
			case strings.HasPrefix(line, "Handling connection"):
				logger.Info("[kubectl] " + line)
			default:
				logger.Debug("[kubectl] " + line)
			}
		}
	}()

	// kubectl stderr: errors and status messages
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.Contains(line, "error: lost connection"):
				lostConn = true
				logger.Debug("[kubectl] " + line)
			case strings.HasPrefix(line, "Forwarding from"):
				logger.Info("[kubectl] " + line)
			case strings.HasPrefix(line, "Handling connection"):
				logger.Debug("[kubectl] " + line)
			case strings.HasPrefix(line, "error:"), strings.HasPrefix(line, "Error:"):
				if !lostConn {
					logger.Error("[kubectl] " + line)
				}
			default:
				logger.Debug("[kubectl] " + line)
			}
		}
	}()

	waitErr := cmd.Wait()
	wg.Wait()

	return lostConn, waitErr
}
