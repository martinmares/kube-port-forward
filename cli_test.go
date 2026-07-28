package main

import (
	"os"
	"testing"
)

func TestParseArgsInsecure(t *testing.T) {
	cfg := parseTestArgs(t,
		"--insecure",
		"-n", "test",
		"pod/example", "9100:9100",
	)

	if !cfg.insecure {
		t.Fatal("parseArgs() did not enable insecure TLS")
	}
}

func TestParseArgsDeploymentWildcardPorts(t *testing.T) {
	cfg := parseTestArgs(t,
		"-n", "test",
		"deploy/example", "9100:*",
	)

	if len(cfg.targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(cfg.targets))
	}
	target := cfg.targets[0]
	if target.resourceKind != resourceDeployment || target.resourceName != "example" {
		t.Fatalf("unexpected target: %#v", target)
	}
	if target.localPort != 9100 || target.remotePort != 0 {
		t.Fatalf("unexpected ports: local=%d remote=%d", target.localPort, target.remotePort)
	}
}

func TestParseArgsAllServices(t *testing.T) {
	cfg := parseTestArgs(t,
		"-n", "test",
		"svc/*", "10000:*",
	)

	target := cfg.targets[0]
	if target.resourceKind != resourceService || target.resourceName != "*" {
		t.Fatalf("unexpected target: %#v", target)
	}
}

func parseTestArgs(t *testing.T, args ...string) *config {
	t.Helper()
	originalArgs := os.Args
	t.Cleanup(func() {
		os.Args = originalArgs
	})
	os.Args = append([]string{"kube-port-forward"}, args...)

	cfg, err := parseArgs()
	if err != nil {
		t.Fatalf("parseArgs() error = %v", err)
	}
	return cfg
}
