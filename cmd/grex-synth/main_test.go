package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig([]string{"-url", "wss://gw/v1/opamp"}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Agents != 1 {
		t.Errorf("Agents = %d, want 1", cfg.Agents)
	}
	if cfg.Heartbeat != 30*time.Second {
		t.Errorf("Heartbeat = %v, want 30s", cfg.Heartbeat)
	}
	if cfg.ServiceName != "otelcol-contrib" {
		t.Errorf("ServiceName = %q, want otelcol-contrib", cfg.ServiceName)
	}
}

func TestParseConfigFlags(t *testing.T) {
	cfg, err := parseConfig([]string{
		"-url", "http://gw/v1/opamp",
		"-agents", "500",
		"-ramp", "100",
		"-heartbeat", "10s",
		"-duration", "5m",
		"-restart-after", "2m",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Agents != 500 || cfg.RampPerSec != 100 {
		t.Errorf("Agents/Ramp = %d/%d, want 500/100", cfg.Agents, cfg.RampPerSec)
	}
	if cfg.Transport() != "http" {
		t.Errorf("Transport() = %q, want http", cfg.Transport())
	}
}

func TestParseConfigRejectsMissingURL(t *testing.T) {
	_, err := parseConfig([]string{"-agents", "5"}, io.Discard)
	if err == nil {
		t.Fatal("parseConfig with no url = nil, want error")
	}
	if !strings.Contains(err.Error(), "server url") {
		t.Errorf("error = %q, want it to mention server url", err.Error())
	}
}
