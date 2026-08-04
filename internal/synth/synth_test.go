package synth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		ServerURL: "wss://gateway:4320/v1/opamp",
		Agents:    10,
		Heartbeat: 30 * time.Second,
		Duration:  5 * time.Minute,
	}
}

func TestValidateAcceptsMinimalConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"empty url", func(c *Config) { c.ServerURL = "" }, "server url"},
		{"bad scheme", func(c *Config) { c.ServerURL = "ftp://x/y" }, "scheme"},
		{"zero agents", func(c *Config) { c.Agents = 0 }, "agents"},
		{"zero heartbeat", func(c *Config) { c.Heartbeat = 0 }, "heartbeat"},
		{"zero duration", func(c *Config) { c.Duration = 0 }, "duration"},
		{"negative ramp", func(c *Config) { c.RampPerSec = -1 }, "ramp"},
		{"restart past duration", func(c *Config) { c.RestartAfter = 10 * time.Minute }, "restart"},
		{"cert without key", func(c *Config) { c.CertFile = "c.pem" }, "key"},
		{"key without cert", func(c *Config) { c.KeyFile = "k.pem" }, "cert"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate() = %q, want it to mention %q", err.Error(), tt.want)
			}
		})
	}
}

func TestTransportInferredFromScheme(t *testing.T) {
	if got := validConfig().transport(); got != transportWS {
		t.Errorf("transport() = %q, want %q", got, transportWS)
	}
	c := validConfig()
	c.ServerURL = "https://gateway/v1/opamp"
	if got := c.transport(); got != transportHTTP {
		t.Errorf("transport() = %q, want %q", got, transportHTTP)
	}
}

func TestSummarizeCountsAndPercentiles(t *testing.T) {
	results := []Result{
		{InstanceUID: "a", Connected: true, ConnectLatency: 10 * time.Millisecond},
		{InstanceUID: "b", Connected: true, ConnectLatency: 20 * time.Millisecond},
		{InstanceUID: "c", Connected: true, ConnectLatency: 100 * time.Millisecond, Restarted: true, RestartLatency: 5 * time.Millisecond},
		{InstanceUID: "d", Connected: false, Err: errors.New("dial timeout")},
	}
	r := Summarize(results)
	if r.Total != 4 {
		t.Errorf("Total = %d, want 4", r.Total)
	}
	if r.Connected != 3 {
		t.Errorf("Connected = %d, want 3", r.Connected)
	}
	if r.Failed != 1 {
		t.Errorf("Failed = %d, want 1", r.Failed)
	}
	if r.Restarted != 1 {
		t.Errorf("Restarted = %d, want 1", r.Restarted)
	}
	if r.ConnectMax != 100*time.Millisecond {
		t.Errorf("ConnectMax = %v, want 100ms", r.ConnectMax)
	}
	if r.Errors["dial timeout"] != 1 {
		t.Errorf("Errors[dial timeout] = %d, want 1", r.Errors["dial timeout"])
	}
}

func TestSummarizeEmpty(t *testing.T) {
	r := Summarize(nil)
	if r.Total != 0 || r.Connected != 0 || r.ConnectP50 != 0 {
		t.Errorf("Summarize(nil) = %+v, want zero value", r)
	}
}
