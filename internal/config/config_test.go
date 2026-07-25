package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaults(t *testing.T) {
	path := writeFile(t, "config.yaml", "{}\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listeners.OpAMP != ":4320" {
		t.Errorf("OpAMP = %q, want %q", cfg.Listeners.OpAMP, ":4320")
	}
	if cfg.Listeners.UI != ":8080" {
		t.Errorf("UI = %q, want %q", cfg.Listeners.UI, ":8080")
	}
	if cfg.Listeners.Telemetry != ":9090" {
		t.Errorf("Telemetry = %q, want %q", cfg.Listeners.Telemetry, ":9090")
	}
	if cfg.Fleet.HeartbeatInterval != 30*time.Second {
		t.Errorf("HeartbeatInterval = %v, want %v", cfg.Fleet.HeartbeatInterval, 30*time.Second)
	}
	if cfg.Fleet.StaleMissedHeartbeats != 3 {
		t.Errorf("StaleMissedHeartbeats = %d, want 3", cfg.Fleet.StaleMissedHeartbeats)
	}
	if len(cfg.Fleet.RequiredAttributes) != 0 {
		t.Errorf("RequiredAttributes = %v, want empty", cfg.Fleet.RequiredAttributes)
	}
	if cfg.Metrics.PerAgentSeriesLimit != 1000 {
		t.Errorf("PerAgentSeriesLimit = %d, want 1000", cfg.Metrics.PerAgentSeriesLimit)
	}
	if cfg.Log.Level != "info" {
		t.Errorf("Level = %q, want %q", cfg.Log.Level, "info")
	}
	if cfg.Log.Format != "text" {
		t.Errorf("Format = %q, want %q", cfg.Log.Format, "text")
	}
	if cfg.Debug.PprofEnabled {
		t.Error("PprofEnabled = true, want false by default (sensitive, must opt in)")
	}
	if cfg.UI.PollInterval != 5*time.Second {
		t.Errorf("UI.PollInterval = %v, want 5s", cfg.UI.PollInterval)
	}
}

func TestLoadFullFile(t *testing.T) {
	path := writeFile(t, "config.yaml", `
listeners:
  opamp: "127.0.0.1:14320"
  ui: "127.0.0.1:18080"
  telemetry: "127.0.0.1:19090"
fleet:
  heartbeat_interval: 10s
  stale_missed_heartbeats: 5
  required_attributes:
    - deployment.environment
    - service.namespace
metrics:
  per_agent_series_limit: 50
debug:
  pprof_enabled: true
log:
  level: debug
  format: json
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listeners.OpAMP != "127.0.0.1:14320" {
		t.Errorf("OpAMP = %q", cfg.Listeners.OpAMP)
	}
	if cfg.Fleet.HeartbeatInterval != 10*time.Second {
		t.Errorf("HeartbeatInterval = %v", cfg.Fleet.HeartbeatInterval)
	}
	if cfg.Fleet.StaleMissedHeartbeats != 5 {
		t.Errorf("StaleMissedHeartbeats = %d", cfg.Fleet.StaleMissedHeartbeats)
	}
	want := []string{"deployment.environment", "service.namespace"}
	if !slices.Equal(cfg.Fleet.RequiredAttributes, want) {
		t.Errorf("RequiredAttributes = %v, want %v", cfg.Fleet.RequiredAttributes, want)
	}
	if cfg.Metrics.PerAgentSeriesLimit != 50 {
		t.Errorf("PerAgentSeriesLimit = %d, want 50", cfg.Metrics.PerAgentSeriesLimit)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "json" {
		t.Errorf("Log = %+v", cfg.Log)
	}
	if !cfg.Debug.PprofEnabled {
		t.Error("PprofEnabled = false, want true")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestLoadBadYAML(t *testing.T) {
	path := writeFile(t, "config.yaml", "listeners: [oops\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want error for bad yaml")
	}
}

func TestLoadUnknownField(t *testing.T) {
	path := writeFile(t, "config.yaml", "bogus_field:\n  opamp: \":1\"\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want error for unknown field")
	}
}

func TestLoadBadAddress(t *testing.T) {
	path := writeFile(t, "config.yaml", "listeners:\n  opamp: \"no-port\"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error for address without port")
	}
	if !strings.Contains(err.Error(), "listeners.opamp") {
		t.Errorf("error %q does not name the field", err)
	}
}

func TestLoadTLSCertWithoutKey(t *testing.T) {
	cert := writeFile(t, "cert.pem", "dummy")
	path := writeFile(t, "config.yaml", "tls:\n  cert_file: "+cert+"\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want error for cert without key")
	}
}

func TestLoadTLSFileMissing(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, "config.yaml",
		"tls:\n  cert_file: "+filepath.Join(dir, "no.pem")+"\n  key_file: "+filepath.Join(dir, "no.key")+"\n")
	_, err := Load(path)
	if err == nil {
		t.Fatal("want error for missing TLS files")
	}
	if !strings.Contains(err.Error(), "tls.cert_file") {
		t.Errorf("error %q does not name the field", err)
	}
}

func TestLoadClientCARequiresServerTLS(t *testing.T) {
	ca := writeFile(t, "ca.pem", "dummy")
	path := writeFile(t, "config.yaml", "tls:\n  client_ca_file: "+ca+"\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want error for client CA without server cert/key")
	}
}

func TestLoadTLSValid(t *testing.T) {
	cert := writeFile(t, "cert.pem", "dummy")
	key := writeFile(t, "key.pem", "dummy")
	ca := writeFile(t, "ca.pem", "dummy")
	path := writeFile(t, "config.yaml",
		"tls:\n  cert_file: "+cert+"\n  key_file: "+key+"\n  client_ca_file: "+ca+"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TLS.CertFile != cert || cfg.TLS.KeyFile != key || cfg.TLS.ClientCAFile != ca {
		t.Errorf("TLS = %+v", cfg.TLS)
	}
}

func TestLoadBadLogLevel(t *testing.T) {
	path := writeFile(t, "config.yaml", "log:\n  level: loud\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want error for bad log level")
	}
}

func TestLoadBadLogFormat(t *testing.T) {
	path := writeFile(t, "config.yaml", "log:\n  format: xml\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want error for bad log format")
	}
}

func TestLoadBadHeartbeatInterval(t *testing.T) {
	path := writeFile(t, "config.yaml", "fleet:\n  heartbeat_interval: -1m\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want error for non-positive heartbeat interval")
	}
}

func TestLoadBadStaleMissedHeartbeats(t *testing.T) {
	path := writeFile(t, "config.yaml", "fleet:\n  stale_missed_heartbeats: 0\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want error for non-positive missed heartbeat count")
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv("GREX_LISTENERS_OPAMP", "127.0.0.1:24320")
	t.Setenv("GREX_LOG_LEVEL", "warn")
	t.Setenv("GREX_FLEET_HEARTBEAT_INTERVAL", "90s")
	t.Setenv("GREX_FLEET_STALE_MISSED_HEARTBEATS", "7")
	t.Setenv("GREX_FLEET_REQUIRED_ATTRIBUTES", "team, deployment.environment")
	t.Setenv("GREX_METRICS_PER_AGENT_SERIES_LIMIT", "25")
	t.Setenv("GREX_DEBUG_PPROF_ENABLED", "true")
	path := writeFile(t, "config.yaml", "listeners:\n  opamp: \"127.0.0.1:14320\"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listeners.OpAMP != "127.0.0.1:24320" {
		t.Errorf("OpAMP = %q, env should win over file", cfg.Listeners.OpAMP)
	}
	if cfg.Log.Level != "warn" {
		t.Errorf("Level = %q, want %q", cfg.Log.Level, "warn")
	}
	if cfg.Fleet.HeartbeatInterval != 90*time.Second {
		t.Errorf("HeartbeatInterval = %v, want 90s", cfg.Fleet.HeartbeatInterval)
	}
	if cfg.Fleet.StaleMissedHeartbeats != 7 {
		t.Errorf("StaleMissedHeartbeats = %d, want 7", cfg.Fleet.StaleMissedHeartbeats)
	}
	want := []string{"team", "deployment.environment"}
	if !slices.Equal(cfg.Fleet.RequiredAttributes, want) {
		t.Errorf("RequiredAttributes = %v, want %v (env, comma separated, trimmed)", cfg.Fleet.RequiredAttributes, want)
	}
	if cfg.Metrics.PerAgentSeriesLimit != 25 {
		t.Errorf("PerAgentSeriesLimit = %d, want 25", cfg.Metrics.PerAgentSeriesLimit)
	}
	if !cfg.Debug.PprofEnabled {
		t.Error("PprofEnabled = false, want true from env")
	}
}

func TestLoadEnvBadBool(t *testing.T) {
	t.Setenv("GREX_DEBUG_PPROF_ENABLED", "sorta")
	path := writeFile(t, "config.yaml", "{}\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want error for bad bool in env")
	}
}

func TestLoadNegativePerAgentSeriesLimit(t *testing.T) {
	path := writeFile(t, "config.yaml", "metrics:\n  per_agent_series_limit: -1\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want error for negative per-agent series limit")
	}
}

func TestLoadEnvBadDuration(t *testing.T) {
	t.Setenv("GREX_FLEET_HEARTBEAT_INTERVAL", "soon")
	path := writeFile(t, "config.yaml", "{}\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want error for bad duration in env")
	}
}
