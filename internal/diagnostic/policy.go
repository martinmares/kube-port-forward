package diagnostic

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

type Policy struct {
	DefaultAction string `yaml:"defaultAction" json:"defaultAction"`
	Allow         []Rule `yaml:"allow,omitempty" json:"allow"`
	Deny          []Rule `yaml:"deny,omitempty" json:"deny"`
}

type Rule struct {
	Hosts []string `yaml:"hosts,omitempty" json:"hosts"`
	CIDRs []string `yaml:"cidrs,omitempty" json:"cidrs"`
	Ports []string `yaml:"ports,omitempty" json:"ports"`
}

type Resolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type Evaluator struct {
	policy        Policy
	resolver      Resolver
	extraDeniedIP []net.IP
}

func (p Policy) Validate() error {
	switch p.DefaultAction {
	case "allow", "deny":
	default:
		return fmt.Errorf("policy.defaultAction must be allow or deny")
	}
	for group, rules := range map[string][]Rule{"allow": p.Allow, "deny": p.Deny} {
		for index, rule := range rules {
			if len(rule.Hosts) == 0 && len(rule.CIDRs) == 0 {
				return fmt.Errorf("policy.%s[%d] must contain hosts or cidrs", group, index)
			}
			for _, host := range rule.Hosts {
				if err := validateHostPattern(host); err != nil {
					return fmt.Errorf("policy.%s[%d]: %w", group, index, err)
				}
			}
			for _, cidr := range rule.CIDRs {
				if _, _, err := net.ParseCIDR(cidr); err != nil {
					return fmt.Errorf("policy.%s[%d]: invalid CIDR %q", group, index, cidr)
				}
			}
			for _, ports := range rule.Ports {
				if _, _, err := parsePortRange(ports); err != nil {
					return fmt.Errorf("policy.%s[%d]: %w", group, index, err)
				}
			}
		}
	}
	return nil
}

func NewEvaluator(policy Policy, resolver Resolver, extraDeniedIP []net.IP) (*Evaluator, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &Evaluator{policy: policy, resolver: resolver, extraDeniedIP: extraDeniedIP}, nil
}

func (e *Evaluator) ResolveAllowed(ctx context.Context, host string, port int) ([]net.IPAddr, error) {
	host = normalizeHost(host)
	if host == "" || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid target")
	}

	addresses, err := resolve(ctx, e.resolver, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}

	allowed := make([]net.IPAddr, 0, len(addresses))
	for _, address := range addresses {
		if builtinDenied(address.IP) || containsIP(e.extraDeniedIP, address.IP) {
			continue
		}
		if matchesRules(e.policy.Deny, host, address.IP, port) {
			continue
		}
		permitted := e.policy.DefaultAction == "allow" || matchesRules(e.policy.Allow, host, address.IP, port)
		if permitted {
			allowed = append(allowed, address)
		}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("target %s:%d is denied by policy", host, port)
	}
	return allowed, nil
}

func resolve(ctx context.Context, resolver Resolver, host string) ([]net.IPAddr, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IPAddr{{IP: ip}}, nil
	}
	return resolver.LookupIPAddr(ctx, host)
}

func matchesRules(rules []Rule, host string, ip net.IP, port int) bool {
	for _, rule := range rules {
		if !matchesPort(rule.Ports, port) {
			continue
		}
		for _, pattern := range rule.Hosts {
			if matchesHost(pattern, host) {
				return true
			}
		}
		for _, cidr := range rule.CIDRs {
			_, network, _ := net.ParseCIDR(cidr)
			if network != nil && network.Contains(ip) {
				return true
			}
		}
	}
	return false
}

func matchesPort(ranges []string, port int) bool {
	if len(ranges) == 0 {
		return true
	}
	for _, value := range ranges {
		start, end, err := parsePortRange(value)
		if err == nil && port >= start && port <= end {
			return true
		}
	}
	return false
}

func parsePortRange(value string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(value), "-")
	if len(parts) < 1 || len(parts) > 2 {
		return 0, 0, fmt.Errorf("invalid port range %q", value)
	}
	start, err := strconv.Atoi(parts[0])
	if err != nil || start < 1 || start > 65535 {
		return 0, 0, fmt.Errorf("invalid port range %q", value)
	}
	end := start
	if len(parts) == 2 {
		end, err = strconv.Atoi(parts[1])
		if err != nil || end < start || end > 65535 {
			return 0, 0, fmt.Errorf("invalid port range %q", value)
		}
	}
	return start, end, nil
}

func validateHostPattern(value string) error {
	value = normalizeHost(value)
	if value == "" || strings.ContainsAny(value, "/: \\") {
		return fmt.Errorf("invalid host pattern %q", value)
	}
	if strings.Contains(value, "*") && (!strings.HasPrefix(value, "*.") || strings.Count(value, "*") != 1) {
		return fmt.Errorf("invalid host wildcard %q", value)
	}
	return nil
}

func matchesHost(pattern, host string) bool {
	pattern = normalizeHost(pattern)
	host = normalizeHost(host)
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*")
		return strings.HasSuffix(host, suffix) && host != strings.TrimPrefix(suffix, ".")
	}
	return host == pattern
}

func normalizeHost(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func builtinDenied(ip net.IP) bool {
	return ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.Equal(net.IPv4bcast)
}

func containsIP(values []net.IP, candidate net.IP) bool {
	for _, value := range values {
		if value.Equal(candidate) {
			return true
		}
	}
	return false
}
