package diagnostic

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultProxyPort = 1080
	DefaultTTL       = "8h"
	DefaultConnect   = "10s"
	DefaultIdle      = "15m"
)

type Profile struct {
	Image            string   `yaml:"image" json:"image"`
	PullPolicy       string   `yaml:"pullPolicy,omitempty" json:"pullPolicy"`
	ImagePullSecrets []string `yaml:"imagePullSecrets,omitempty" json:"imagePullSecrets"`
	TTL              string   `yaml:"ttl,omitempty" json:"ttl"`
	MaxConnections   int      `yaml:"maxConnections,omitempty" json:"maxConnections"`
	ConnectTimeout   string   `yaml:"connectTimeout,omitempty" json:"connectTimeout"`
	IdleTimeout      string   `yaml:"idleTimeout,omitempty" json:"idleTimeout"`
	Policy           Policy   `yaml:"policy" json:"policy"`
}

type RuntimeConfig struct {
	MaxConnections int    `yaml:"maxConnections"`
	ConnectTimeout string `yaml:"connectTimeout"`
	IdleTimeout    string `yaml:"idleTimeout"`
	Policy         Policy `yaml:"policy"`
}

func LoadProfile(path string) (Profile, error) {
	file, err := os.Open(path)
	if err != nil {
		return Profile{}, fmt.Errorf("open diagnostic config: %w", err)
	}
	defer file.Close()

	var profile Profile
	if err := decodeYAML(file, &profile); err != nil {
		return Profile{}, fmt.Errorf("parse diagnostic config: %w", err)
	}
	profile.SetDefaults()
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func LoadRuntimeConfig(path string) (RuntimeConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("open proxy config: %w", err)
	}
	defer file.Close()

	var cfg RuntimeConfig
	if err := decodeYAML(file, &cfg); err != nil {
		return RuntimeConfig{}, fmt.Errorf("parse proxy config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return RuntimeConfig{}, err
	}
	return cfg, nil
}

func decodeYAML(reader io.Reader, target any) error {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func ParsePolicyYAML(value string) (Policy, error) {
	var policy Policy
	if err := decodeYAML(strings.NewReader(value), &policy); err != nil {
		return Policy{}, fmt.Errorf("parse diagnostic policy: %w", err)
	}
	if policy.DefaultAction == "" {
		policy.DefaultAction = "deny"
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func MarshalPolicyYAML(policy Policy) (string, error) {
	data, err := yaml.Marshal(policy)
	return string(data), err
}

func (p *Profile) SetDefaults() {
	if p.PullPolicy == "" {
		p.PullPolicy = "IfNotPresent"
	}
	if p.TTL == "" {
		p.TTL = DefaultTTL
	}
	if p.MaxConnections == 0 {
		p.MaxConnections = 64
	}
	if p.ConnectTimeout == "" {
		p.ConnectTimeout = DefaultConnect
	}
	if p.IdleTimeout == "" {
		p.IdleTimeout = DefaultIdle
	}
	if p.Policy.DefaultAction == "" {
		p.Policy.DefaultAction = "deny"
	}
}

func (p Profile) Validate() error {
	if strings.TrimSpace(p.Image) == "" {
		return fmt.Errorf("diagnostic config: image is required")
	}
	switch p.PullPolicy {
	case "Always", "IfNotPresent", "Never":
	default:
		return fmt.Errorf("diagnostic config: pullPolicy must be Always, IfNotPresent, or Never")
	}
	if _, err := positiveDuration("ttl", p.TTL); err != nil {
		return err
	}
	if p.MaxConnections < 1 || p.MaxConnections > 4096 {
		return fmt.Errorf("diagnostic config: maxConnections must be between 1 and 4096")
	}
	if _, err := positiveDuration("connectTimeout", p.ConnectTimeout); err != nil {
		return err
	}
	if _, err := positiveDuration("idleTimeout", p.IdleTimeout); err != nil {
		return err
	}
	if err := p.Policy.Validate(); err != nil {
		return fmt.Errorf("diagnostic config: %w", err)
	}
	return nil
}

func (p Profile) RuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		MaxConnections: p.MaxConnections,
		ConnectTimeout: p.ConnectTimeout,
		IdleTimeout:    p.IdleTimeout,
		Policy:         p.Policy,
	}
}

func (p Profile) TTLDuration() time.Duration {
	duration, _ := time.ParseDuration(p.TTL)
	return duration
}

func (c RuntimeConfig) Validate() error {
	if c.MaxConnections < 1 || c.MaxConnections > 4096 {
		return fmt.Errorf("proxy config: maxConnections must be between 1 and 4096")
	}
	if _, err := positiveDuration("connectTimeout", c.ConnectTimeout); err != nil {
		return err
	}
	if _, err := positiveDuration("idleTimeout", c.IdleTimeout); err != nil {
		return err
	}
	if err := c.Policy.Validate(); err != nil {
		return fmt.Errorf("proxy config: %w", err)
	}
	return nil
}

func (c RuntimeConfig) ConnectDuration() time.Duration {
	duration, _ := time.ParseDuration(c.ConnectTimeout)
	return duration
}

func (c RuntimeConfig) IdleDuration() time.Duration {
	duration, _ := time.ParseDuration(c.IdleTimeout)
	return duration
}

func (c RuntimeConfig) YAML() ([]byte, error) {
	return yaml.Marshal(c)
}

func positiveDuration(field, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("diagnostic config: %s must be a positive duration", field)
	}
	return duration, nil
}
