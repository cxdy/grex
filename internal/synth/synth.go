// Package synth drives short-lived synthetic OpAMP agents against a grex
// server or gateway to measure connection scale. Each agent is a real
// opamp-go client: it connects, heartbeats, optionally simulates a restart
// (disconnect then reconnect), and reports how long that took and what
// failed. See cmd/grex-synth and docs/developer/scaling.md.
package synth

import (
	"fmt"
	"net/url"
	"sort"
	"time"
)

const (
	transportWS   = "ws"
	transportHTTP = "http"
)

// Config is one synth run: how many agents, where they connect, and how long
// they live. Transport (WebSocket vs HTTP polling) is inferred from the URL
// scheme, so there is no separate transport knob.
type Config struct {
	// ServerURL is the OpAMP endpoint. ws/wss select a WebSocket client,
	// http/https an HTTP-polling client.
	ServerURL string
	// Agents is how many synthetic agents to run, each its own connection and
	// instance_uid.
	Agents int
	// RampPerSec caps how many new agents start connecting each second. 0
	// starts them all at once (a connection burst).
	RampPerSec int
	// Heartbeat is the OpAMP client heartbeat interval.
	Heartbeat time.Duration
	// Duration is how long the run lasts before all agents disconnect.
	Duration time.Duration
	// RestartAfter, when non-zero, makes each agent disconnect and reconnect
	// once this far into the run, simulating a supervisor restart. Must be
	// less than Duration.
	RestartAfter time.Duration
	// ServiceName is the agent's service.name identifying attribute.
	ServiceName string
	// CertFile, KeyFile, CAFile configure mTLS. CertFile and KeyFile are a
	// pair: set both or neither. A gateway requiring mTLS needs all three.
	CertFile string
	KeyFile  string
	CAFile   string
}

// Transport reports the OpAMP transport the config's URL selects, "ws" or
// "http", or "" when the scheme is unrecognized.
func (c Config) Transport() string { return c.transport() }

// transport classifies the run by URL scheme.
func (c Config) transport() string {
	if u, err := url.Parse(c.ServerURL); err == nil {
		switch u.Scheme {
		case "ws", "wss":
			return transportWS
		case "http", "https":
			return transportHTTP
		}
	}
	return ""
}

// Validate reports the first problem with the config, or nil.
func (c Config) Validate() error {
	if c.ServerURL == "" {
		return fmt.Errorf("server url must be set")
	}
	if c.transport() == "" {
		return fmt.Errorf("server url scheme must be ws, wss, http, or https")
	}
	if c.Agents < 1 {
		return fmt.Errorf("agents must be at least 1")
	}
	if c.RampPerSec < 0 {
		return fmt.Errorf("ramp must be zero or positive")
	}
	if c.Heartbeat <= 0 {
		return fmt.Errorf("heartbeat must be positive")
	}
	if c.Duration <= 0 {
		return fmt.Errorf("duration must be positive")
	}
	if c.RestartAfter < 0 || (c.RestartAfter > 0 && c.RestartAfter >= c.Duration) {
		return fmt.Errorf("restart-after must be zero or less than duration")
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		return fmt.Errorf("cert and key must be set together")
	}
	return nil
}

// Result is one agent's outcome.
type Result struct {
	InstanceUID    string
	Connected      bool
	ConnectLatency time.Duration
	Restarted      bool
	RestartLatency time.Duration
	Err            error
}

// Report aggregates every agent's Result into run-level numbers.
type Report struct {
	Total     int
	Connected int
	Failed    int
	Restarted int

	ConnectP50 time.Duration
	ConnectP99 time.Duration
	ConnectMax time.Duration

	// Errors counts distinct failure messages so a mass failure reads as one
	// line with a count, not one line per agent.
	Errors map[string]int
}

// Summarize folds per-agent results into a Report. Connect percentiles are
// computed over connected agents only; a failed agent has no meaningful
// connect latency.
func Summarize(results []Result) Report {
	r := Report{Total: len(results), Errors: map[string]int{}}
	var latencies []time.Duration
	for _, res := range results {
		switch {
		case res.Connected:
			r.Connected++
			latencies = append(latencies, res.ConnectLatency)
		default:
			r.Failed++
			if res.Err != nil {
				r.Errors[res.Err.Error()]++
			}
		}
		if res.Restarted {
			r.Restarted++
		}
	}
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		r.ConnectP50 = percentile(latencies, 50)
		r.ConnectP99 = percentile(latencies, 99)
		r.ConnectMax = latencies[len(latencies)-1]
	}
	return r
}

// percentile returns the p-th percentile of a sorted slice using
// nearest-rank.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100 // ceil(p/100 * n)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
