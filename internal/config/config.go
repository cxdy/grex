// Package config loads and validates the grex configuration from a YAML file
// with environment variable overrides.
package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the full grex configuration.
type Config struct {
	Listeners Listeners `yaml:"listeners"`
	TLS       TLS       `yaml:"tls"`
	Fleet     Fleet     `yaml:"fleet"`
	Metrics   Metrics   `yaml:"metrics"`
	Log       Log       `yaml:"log"`
}

// Metrics holds telemetry exposure settings.
type Metrics struct {
	// PerAgentSeriesLimit caps per-agent metric series. When the fleet
	// exceeds the limit, per-agent series are omitted entirely; aggregate
	// series always remain.
	PerAgentSeriesLimit int `yaml:"per_agent_series_limit"`
}

// Listeners holds the bind addresses for the three server listeners.
type Listeners struct {
	OpAMP     string `yaml:"opamp"`
	UI        string `yaml:"ui"`
	Telemetry string `yaml:"telemetry"`
}

// TLS holds certificate paths for the OpAMP listener. CertFile and KeyFile
// enable server TLS; ClientCAFile additionally requires and verifies client
// certificates (mTLS).
type TLS struct {
	CertFile     string `yaml:"cert_file"`
	KeyFile      string `yaml:"key_file"`
	ClientCAFile string `yaml:"client_ca_file"`
}

// Fleet holds fleet state settings.
type Fleet struct {
	// HeartbeatInterval is how often each agent is expected to check in.
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
	// StaleMissedHeartbeats is how many consecutive check-ins an agent may
	// miss before it is evicted from fleet state.
	StaleMissedHeartbeats int `yaml:"stale_missed_heartbeats"`
	// RequiredAttributes lists AgentDescription attribute keys every agent
	// must report. Empty means no enforcement.
	RequiredAttributes []string `yaml:"required_attributes"`
}

// Log holds logging settings.
type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// envOverrides maps environment variables to config fields. Values are
// applied after the file is parsed and before validation.
func (c *Config) envOverrides() []struct {
	name string
	set  func(string) error
} {
	setString := func(dst *string) func(string) error {
		return func(v string) error { *dst = v; return nil }
	}
	setDuration := func(dst *time.Duration) func(string) error {
		return func(v string) error {
			d, err := time.ParseDuration(v)
			if err != nil {
				return err
			}
			*dst = d
			return nil
		}
	}
	setInt := func(dst *int) func(string) error {
		return func(v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			*dst = n
			return nil
		}
	}
	setStringList := func(dst *[]string) func(string) error {
		return func(v string) error {
			var list []string
			for part := range strings.SplitSeq(v, ",") {
				if part = strings.TrimSpace(part); part != "" {
					list = append(list, part)
				}
			}
			*dst = list
			return nil
		}
	}
	return []struct {
		name string
		set  func(string) error
	}{
		{"GREX_LISTENERS_OPAMP", setString(&c.Listeners.OpAMP)},
		{"GREX_LISTENERS_UI", setString(&c.Listeners.UI)},
		{"GREX_LISTENERS_TELEMETRY", setString(&c.Listeners.Telemetry)},
		{"GREX_TLS_CERT_FILE", setString(&c.TLS.CertFile)},
		{"GREX_TLS_KEY_FILE", setString(&c.TLS.KeyFile)},
		{"GREX_TLS_CLIENT_CA_FILE", setString(&c.TLS.ClientCAFile)},
		{"GREX_FLEET_HEARTBEAT_INTERVAL", setDuration(&c.Fleet.HeartbeatInterval)},
		{"GREX_FLEET_STALE_MISSED_HEARTBEATS", setInt(&c.Fleet.StaleMissedHeartbeats)},
		{"GREX_FLEET_REQUIRED_ATTRIBUTES", setStringList(&c.Fleet.RequiredAttributes)},
		{"GREX_METRICS_PER_AGENT_SERIES_LIMIT", setInt(&c.Metrics.PerAgentSeriesLimit)},
		{"GREX_LOG_LEVEL", setString(&c.Log.Level)},
		{"GREX_LOG_FORMAT", setString(&c.Log.Format)},
	}
}

func defaults() *Config {
	return &Config{
		Listeners: Listeners{
			OpAMP:     ":4320",
			UI:        ":8080",
			Telemetry: ":9090",
		},
		Fleet:   Fleet{HeartbeatInterval: 30 * time.Second, StaleMissedHeartbeats: 3},
		Metrics: Metrics{PerAgentSeriesLimit: 1000},
		Log:     Log{Level: "info", Format: "text"},
	}
}

// Load reads the YAML file at path, applies GREX_* environment variable
// overrides, validates the result, and returns the configuration.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is the operator-chosen config file
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := defaults()
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	for _, ov := range cfg.envOverrides() {
		if v, ok := os.LookupEnv(ov.name); ok {
			if err := ov.set(v); err != nil {
				return nil, fmt.Errorf("%s: %w", ov.name, err)
			}
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	for _, l := range []struct{ field, addr string }{
		{"listeners.opamp", c.Listeners.OpAMP},
		{"listeners.ui", c.Listeners.UI},
		{"listeners.telemetry", c.Listeners.Telemetry},
	} {
		if _, _, err := net.SplitHostPort(l.addr); err != nil {
			return fmt.Errorf("%s: invalid address %q: %w", l.field, l.addr, err)
		}
	}
	if err := c.TLS.validate(); err != nil {
		return err
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level: %q is not one of debug, info, warn, error", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("log.format: %q is not one of text, json", c.Log.Format)
	}
	if c.Fleet.HeartbeatInterval <= 0 {
		return fmt.Errorf("fleet.heartbeat_interval: must be positive, got %v", c.Fleet.HeartbeatInterval)
	}
	if c.Fleet.StaleMissedHeartbeats <= 0 {
		return fmt.Errorf("fleet.stale_missed_heartbeats: must be positive, got %d", c.Fleet.StaleMissedHeartbeats)
	}
	if c.Metrics.PerAgentSeriesLimit < 0 {
		return fmt.Errorf("metrics.per_agent_series_limit: must be non-negative, got %d", c.Metrics.PerAgentSeriesLimit)
	}
	return nil
}

func (t *TLS) validate() error {
	if (t.CertFile == "") != (t.KeyFile == "") {
		return fmt.Errorf("tls: cert_file and key_file must be set together")
	}
	if t.ClientCAFile != "" && t.CertFile == "" {
		return fmt.Errorf("tls: client_ca_file requires cert_file and key_file")
	}
	for _, f := range []struct{ field, path string }{
		{"tls.cert_file", t.CertFile},
		{"tls.key_file", t.KeyFile},
		{"tls.client_ca_file", t.ClientCAFile},
	} {
		if f.path == "" {
			continue
		}
		if _, err := os.Stat(f.path); err != nil {
			return fmt.Errorf("%s: %w", f.field, err)
		}
	}
	return nil
}
