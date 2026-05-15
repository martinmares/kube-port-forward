package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	retriesAlways        = -1
	defaultRetryInterval = 5
	defaultWebPort       = 9999
)

type resourceType int

const (
	resourcePod resourceType = iota
	resourceService
)

// target represents one forwarding target: namespace + resource + ports.
type target struct {
	namespace    string
	resourceKind resourceType
	resourceName string
	localPort    int
	remotePort   int
}

func (t *target) resourceStr() string {
	if t.resourceKind == resourceService {
		return "svc/" + t.resourceName
	}
	return "pod/" + t.resourceName
}

type config struct {
	targets       []target
	retries       int
	retryInterval time.Duration
	logLevel      slog.Level
	webPort       int
	openBrowser   bool
}

// defaultNamespace returns the current-context namespace from kubeconfig, or "".
func defaultNamespace() string {
	ns, err := currentContextNamespace()
	if err != nil || ns == "" {
		return ""
	}
	return ns
}

func parseArgs() (*config, error) {
	var (
		namespace     string
		retriesStr    string
		retryInterval int
		logLevelStr   string
		webPort       int
		noOpen        bool
	)

	defNS := defaultNamespace()
	nsHelp := "Kubernetes namespace"
	if defNS != "" {
		nsHelp += fmt.Sprintf(" (default from kubeconfig: %q)", defNS)
	}

	cmd := &cobra.Command{
		Use:   "kube-port-forward [OPTIONS] <pod|svc>/<name> <localPort>:<remotePort> [...]",
		Short: "Port-forward manager with auto-reconnect and web UI",
		Long: `Port-forward manager with auto-reconnect and web UI.
Multiple resources can be specified by repeating <resource> <ports> pairs.`,
		Example: `  kube-port-forward -n nac-app pod/nac-out-xxx 9090:9090
  kube-port-forward -n nac-app svc/nac-out 9091:9090
  kube-port-forward -n nac-app svc/nac-out 9091:*
  kube-port-forward -n nac-app svc/foo 9091:9090 svc/bar 9093:8080
  kube-port-forward --retries 5 --retry-interval 10 -n nac-app svc/nac-out 9091:9090
  kube-port-forward -n nac-app svc/nac-out 9091:9090 --no-open`,
		Args:         cobra.ArbitraryArgs,
		SilenceUsage: true,
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", defNS, nsHelp)
	cmd.Flags().StringVar(&retriesStr, "retries", "always", "max reconnect attempts: integer ≥1 or 'always'")
	cmd.Flags().IntVar(&retryInterval, "retry-interval", defaultRetryInterval, "seconds between reconnect attempts")
	cmd.Flags().StringVar(&logLevelStr, "log-level", "info", "log verbosity: debug, info, warn, error")
	cmd.Flags().IntVar(&webPort, "web-port", defaultWebPort, "local port for the web UI")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "disable automatic browser opening on start")

	var cfg *config
	var parseErr error

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) < 2 {
			_ = cmd.Help()
			return fmt.Errorf("missing required arguments: <resource> <localPort>:<remotePort>")
		}
		if len(args)%2 != 0 {
			return fmt.Errorf("arguments must be pairs of <resource> <localPort>:<remotePort>, got %d args", len(args))
		}
		if namespace == "" {
			return fmt.Errorf("-n <namespace> is required (no default found in kubeconfig)")
		}

		var targets []target
		for i := 0; i < len(args); i += 2 {
			kind, name, err := parseResource(args[i])
			if err != nil {
				return err
			}
			localPort, remotePort, err := parsePorts(args[i+1])
			if err != nil {
				return err
			}
			if remotePort == 0 && kind != resourceService {
				return fmt.Errorf("wildcard port (*) is only supported for services, not pods")
			}
			targets = append(targets, target{
				namespace:    namespace,
				resourceKind: kind,
				resourceName: name,
				localPort:    localPort,
				remotePort:   remotePort,
			})
		}

		var retries int
		switch retriesStr {
		case "always", "0":
			retries = retriesAlways
		default:
			if _, err := fmt.Sscanf(retriesStr, "%d", &retries); err != nil || retries < 1 {
				return fmt.Errorf("--retries must be a positive integer or 'always'")
			}
		}

		logLevel, err := parseLogLevel(logLevelStr)
		if err != nil {
			return err
		}

		cfg = &config{
			targets:       targets,
			retries:       retries,
			retryInterval: time.Duration(retryInterval) * time.Second,
			logLevel:      logLevel,
			webPort:       webPort,
			openBrowser:   !noOpen,
		}
		return nil
	}

	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)

	if err := cmd.Execute(); err != nil {
		parseErr = err
	}

	if parseErr != nil {
		return nil, parseErr
	}
	if cfg == nil {
		// --help was printed, exit cleanly
		os.Exit(0)
	}
	return cfg, nil
}

func parseResource(s string) (resourceType, string, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return 0, "", fmt.Errorf("invalid resource %q: expected pod/<name> or svc/<name>", s)
	}
	switch strings.ToLower(parts[0]) {
	case "pod", "pods":
		return resourcePod, parts[1], nil
	case "svc", "service", "services":
		return resourceService, parts[1], nil
	default:
		return 0, "", fmt.Errorf("unsupported resource type %q: use pod or svc", parts[0])
	}
}

// parsePorts parses "localPort:remotePort" or "localPort:*" (all service ports).
// remotePort=0 means "all ports".
func parsePorts(s string) (int, int, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid port mapping %q: expected <localPort>:<remotePort> or <localPort>:*", s)
	}
	var local int
	if _, err := fmt.Sscanf(parts[0], "%d", &local); err != nil || local < 1 || local > 65535 {
		return 0, 0, fmt.Errorf("invalid local port %q", parts[0])
	}
	if parts[1] == "*" {
		return local, 0, nil
	}
	var remote int
	if _, err := fmt.Sscanf(parts[1], "%d", &remote); err != nil || remote < 1 || remote > 65535 {
		return 0, 0, fmt.Errorf("invalid remote port %q", parts[1])
	}
	return local, remote, nil
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
