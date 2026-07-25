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

	"github.com/dennisme/grex/internal/spiffe"
)

// Config is the full grex configuration.
type Config struct {
	Listeners Listeners `yaml:"listeners"`
	// OpAMPTLS terminates TLS on the OpAMP listener; collectors and OpAMP
	// gateways are the peers.
	OpAMPTLS TLS `yaml:"opamp_tls"`
	// UITLS terminates TLS on the UI listener; human and script/automation
	// API callers are the peers.
	UITLS TLS `yaml:"ui_tls"`
	// TelemetryTLS terminates TLS on the telemetry listener; scrapers
	// (Prometheus) are the peer. /healthz and /readyz are exempt from any
	// client-cert requirement configured here, since orchestrator health
	// probes cannot present one.
	TelemetryTLS TLS     `yaml:"telemetry_tls"`
	Fleet        Fleet   `yaml:"fleet"`
	Metrics      Metrics `yaml:"metrics"`
	UI           UI      `yaml:"ui"`
	Debug        Debug   `yaml:"debug"`
	// Auth maps SPIFFE identities (from UI/telemetry client certs) to
	// roles. Only takes effect on a listener where its TLS block also sets
	// client_ca_file.
	Auth Auth `yaml:"auth"`
	Log  Log  `yaml:"log"`
}

// Auth holds the SPIFFE-identity-to-role mapping used by the UI and
// telemetry listeners' mTLS.
type Auth struct {
	// RoleMapping is consulted in order: exact matches win over prefix
	// matches regardless of position; among same-specificity rules the
	// first one wins.
	RoleMapping []spiffe.RoleRule `yaml:"role_mapping"`
	// DefaultRole applies to an authenticated caller matching no rule.
	// "none" (the default) denies access.
	DefaultRole string `yaml:"default_role"`
}

// UI holds web UI settings.
type UI struct {
	// PollInterval is how often the embedded UI refreshes fleet data via htmx.
	// Default 5s.
	PollInterval time.Duration `yaml:"poll_interval"`
}

// Debug holds settings for diagnostic endpoints that are off by default
// because they expose runtime internals and can be expensive to serve.
type Debug struct {
	// PprofEnabled mounts net/http/pprof handlers under /debug/pprof on the
	// telemetry listener. Off by default: profiles can reveal memory
	// contents and CPU profiling is itself a load an operator must opt into.
	PprofEnabled bool `yaml:"pprof_enabled"`
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

// TLS holds certificate paths for one listener. CertFile and KeyFile enable
// server TLS; ClientCAFile additionally requires and verifies client
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
	setBool := func(dst *bool) func(string) error {
		return func(v string) error {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return err
			}
			*dst = b
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
		{"GREX_OPAMP_TLS_CERT_FILE", setString(&c.OpAMPTLS.CertFile)},
		{"GREX_OPAMP_TLS_KEY_FILE", setString(&c.OpAMPTLS.KeyFile)},
		{"GREX_OPAMP_TLS_CLIENT_CA_FILE", setString(&c.OpAMPTLS.ClientCAFile)},
		{"GREX_UI_TLS_CERT_FILE", setString(&c.UITLS.CertFile)},
		{"GREX_UI_TLS_KEY_FILE", setString(&c.UITLS.KeyFile)},
		{"GREX_UI_TLS_CLIENT_CA_FILE", setString(&c.UITLS.ClientCAFile)},
		{"GREX_TELEMETRY_TLS_CERT_FILE", setString(&c.TelemetryTLS.CertFile)},
		{"GREX_TELEMETRY_TLS_KEY_FILE", setString(&c.TelemetryTLS.KeyFile)},
		{"GREX_TELEMETRY_TLS_CLIENT_CA_FILE", setString(&c.TelemetryTLS.ClientCAFile)},
		{"GREX_AUTH_DEFAULT_ROLE", setString(&c.Auth.DefaultRole)},
		{"GREX_FLEET_HEARTBEAT_INTERVAL", setDuration(&c.Fleet.HeartbeatInterval)},
		{"GREX_FLEET_STALE_MISSED_HEARTBEATS", setInt(&c.Fleet.StaleMissedHeartbeats)},
		{"GREX_FLEET_REQUIRED_ATTRIBUTES", setStringList(&c.Fleet.RequiredAttributes)},
		{"GREX_METRICS_PER_AGENT_SERIES_LIMIT", setInt(&c.Metrics.PerAgentSeriesLimit)},
		{"GREX_UI_POLL_INTERVAL", setDuration(&c.UI.PollInterval)},
		{"GREX_DEBUG_PPROF_ENABLED", setBool(&c.Debug.PprofEnabled)},
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
		UI:      UI{PollInterval: 5 * time.Second},
		Auth:    Auth{DefaultRole: "none"},
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
	if err := c.OpAMPTLS.validate("opamp_tls"); err != nil {
		return err
	}
	if err := c.UITLS.validate("ui_tls"); err != nil {
		return err
	}
	if err := c.TelemetryTLS.validate("telemetry_tls"); err != nil {
		return err
	}
	if err := c.Auth.validate(); err != nil {
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
	if c.UI.PollInterval <= 0 {
		return fmt.Errorf("ui.poll_interval: must be positive, got %v", c.UI.PollInterval)
	}
	return nil
}

// validate checks the TLS block. prefix names the config section in error
// messages (e.g. "opamp_tls", "ui_tls", "telemetry_tls").
var validRoles = map[string]bool{"none": true, "viewer": true, "admin": true}

func (a *Auth) validate() error {
	if !validRoles[a.DefaultRole] {
		return fmt.Errorf("auth.default_role: %q is not one of none, viewer, admin", a.DefaultRole)
	}
	for i, r := range a.RoleMapping {
		if r.Match != "exact" && r.Match != "prefix" {
			return fmt.Errorf("auth.role_mapping[%d].match: %q is not one of exact, prefix", i, r.Match)
		}
		if r.SpiffeID == "" {
			return fmt.Errorf("auth.role_mapping[%d].spiffe_id: must not be empty", i)
		}
		if r.Role != "viewer" && r.Role != "admin" {
			return fmt.Errorf("auth.role_mapping[%d].role: %q is not one of viewer, admin", i, r.Role)
		}
	}
	return nil
}

func (t *TLS) validate(prefix string) error {
	if (t.CertFile == "") != (t.KeyFile == "") {
		return fmt.Errorf("%s: cert_file and key_file must be set together", prefix)
	}
	if t.ClientCAFile != "" && t.CertFile == "" {
		return fmt.Errorf("%s: client_ca_file requires cert_file and key_file", prefix)
	}
	for _, f := range []struct{ field, path string }{
		{prefix + ".cert_file", t.CertFile},
		{prefix + ".key_file", t.KeyFile},
		{prefix + ".client_ca_file", t.ClientCAFile},
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
