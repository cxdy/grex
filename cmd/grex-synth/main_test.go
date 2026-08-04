package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/synth"
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

func TestRunRejectsBadArgs(t *testing.T) {
	err := run(context.Background(), []string{"-agents", "0", "-url", "ws://x/y"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("run with agents=0 = nil, want validation error")
	}
}

// TestRunReportsAgainstDeadEndpoint drives run() end to end: valid config, a
// short duration, and a URL nothing listens on. Agents fail to connect, the
// run still completes, and the report is written to out.
func TestRunReportsAgainstDeadEndpoint(t *testing.T) {
	var out bytes.Buffer
	args := []string{
		"-url", "ws://127.0.0.1:1/v1/opamp",
		"-agents", "2",
		"-duration", "300ms",
	}
	if err := run(context.Background(), args, &out, io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "synth report") {
		t.Errorf("output missing report header:\n%s", got)
	}
	if !strings.Contains(got, "connected:  0") {
		t.Errorf("output = %q, want connected 0 against a dead endpoint", got)
	}
}

func TestPrintReportRendersErrors(t *testing.T) {
	var out bytes.Buffer
	r := synth.Report{Total: 3, Connected: 2, Failed: 1, Errors: map[string]int{"dial timeout": 1}}
	if err := printReport(&out, r); err != nil {
		t.Fatalf("printReport: %v", err)
	}
	got := out.String()
	for _, want := range []string{"agents:     3", "connected:  2", "failed:     1", "dial timeout"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}
