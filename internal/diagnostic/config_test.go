package diagnostic

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type staticResolver map[string][]net.IPAddr

func (r staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	addresses, ok := r[host]
	if !ok {
		return nil, &net.DNSError{Name: host, IsNotFound: true}
	}
	return addresses, nil
}

func TestLoadProfileWithoutVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostic.yaml")
	data := `image: registry.example/proxy:1
policy:
  defaultAction: deny
  allow:
    - hosts: ["*.example.internal"]
      ports: ["443", "5432"]
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	profile, err := LoadProfile(path)
	if err != nil {
		t.Fatalf("LoadProfile() error = %v", err)
	}
	if profile.TTL != DefaultTTL || profile.MaxConnections != 64 {
		t.Fatalf("defaults not applied: %#v", profile)
	}
}

func TestLoadProfileRejectsVersionField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diagnostic.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nimage: proxy\npolicy:\n  defaultAction: deny\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProfile(path)
	if err == nil || !strings.Contains(err.Error(), "field version not found") {
		t.Fatalf("LoadProfile() error = %v, want unknown version field", err)
	}
}

func TestEvaluatorAllowAndDeny(t *testing.T) {
	policy := Policy{
		DefaultAction: "deny",
		Allow:         []Rule{{Hosts: []string{"*.example.internal"}, Ports: []string{"443", "5400-5500"}}},
		Deny:          []Rule{{CIDRs: []string{"10.20.30.0/24"}}},
	}
	resolver := staticResolver{
		"db.example.internal":      {{IP: net.ParseIP("10.10.10.4")}},
		"blocked.example.internal": {{IP: net.ParseIP("10.20.30.4")}},
		"public.example.net":       {{IP: net.ParseIP("203.0.113.5")}},
	}
	evaluator, err := NewEvaluator(policy, resolver, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.ResolveAllowed(t.Context(), "db.example.internal", 5432); err != nil {
		t.Fatalf("allowed target rejected: %v", err)
	}
	if _, err := evaluator.ResolveAllowed(t.Context(), "blocked.example.internal", 5432); err == nil {
		t.Fatal("deny CIDR did not override hostname allow")
	}
	if _, err := evaluator.ResolveAllowed(t.Context(), "public.example.net", 443); err == nil {
		t.Fatal("default deny did not reject unmatched host")
	}
	if _, err := evaluator.ResolveAllowed(t.Context(), "db.example.internal", 22); err == nil {
		t.Fatal("port allowlist did not reject port 22")
	}
}

func TestEvaluatorAlwaysDeniesLoopback(t *testing.T) {
	policy := Policy{DefaultAction: "allow"}
	evaluator, err := NewEvaluator(policy, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.ResolveAllowed(t.Context(), "127.0.0.1", 8080); err == nil {
		t.Fatal("loopback target was allowed")
	}
}
