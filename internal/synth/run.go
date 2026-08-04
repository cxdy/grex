package synth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/client"
	clienttypes "github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
)

// supervisorManagedBy is the opamp.managed_by value the OpAMP Supervisor
// injects; grex treats an agent carrying it as supervisor-managed, which job
// targeting requires. Synthetic agents declare it so they look real to grex.
const supervisorManagedBy = "opentelemetry-opampsupervisor"

// agentCapabilities are the bits a synthetic agent declares: it reports
// status and health and accepts a restart command, matching a real
// supervisor-managed collector.
const agentCapabilities = protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus |
	protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth |
	protobufs.AgentCapabilities_AgentCapabilities_AcceptsRestartCommand

// Run starts cfg.Agents synthetic agents, holds them for cfg.Duration, and
// returns a Report. Agents start at cfg.RampPerSec per second (or all at once
// when it is zero). The run stops early if ctx is cancelled.
func Run(ctx context.Context, cfg Config, logger *slog.Logger) (Report, error) {
	if err := cfg.Validate(); err != nil {
		return Report{}, err
	}
	tlsConf, err := cfg.tlsConfig()
	if err != nil {
		return Report{}, err
	}
	logger.Info("starting synthetic agents",
		"agents", cfg.Agents, "url", cfg.ServerURL, "transport", cfg.transport(),
		"duration", cfg.Duration, "ramp_per_sec", cfg.RampPerSec)

	runCtx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()

	start := time.Now()
	deadline := start.Add(cfg.Duration)
	var restartAt time.Time
	if cfg.RestartAfter > 0 {
		restartAt = start.Add(cfg.RestartAfter)
	}

	results := make([]Result, cfg.Agents)
	var wg sync.WaitGroup
	for i := range cfg.Agents {
		if cfg.RampPerSec > 0 && i > 0 {
			select {
			case <-time.After(time.Second / time.Duration(cfg.RampPerSec)):
			case <-runCtx.Done():
			}
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = runAgent(runCtx, cfg, tlsConf, deadline, restartAt)
		}(i)
	}
	wg.Wait()
	return Summarize(results), nil
}

// runAgent runs one agent's whole lifecycle: connect, optionally restart at
// restartAt, then disconnect at deadline.
func runAgent(ctx context.Context, cfg Config, tlsConf *tls.Config, deadline, restartAt time.Time) Result {
	uid := uuid.New()
	res := Result{InstanceUID: uid.String()}

	latency, c, err := connect(ctx, cfg, tlsConf, uid)
	if err != nil {
		res.Err = err
		return res
	}
	res.Connected = true
	res.ConnectLatency = latency
	// Closure, not defer stop(c): a restart replaces c, and this must stop the
	// current client, not the one bound when the defer ran.
	defer func() { stop(c) }()

	if !restartAt.IsZero() {
		if !sleepUntil(ctx, restartAt) {
			return res
		}
		stop(c)
		rlatency, rc, rerr := connect(ctx, cfg, tlsConf, uid)
		if rerr != nil {
			res.Err = rerr
			return res
		}
		res.Restarted = true
		res.RestartLatency = rlatency
		c = rc
	}

	sleepUntil(ctx, deadline)
	return res
}

// connect starts one opamp-go client and blocks until it reports a successful
// connection, ctx ends, or the deadline in ctx passes. It returns the connect
// latency and the started client (which the caller must Stop).
func connect(ctx context.Context, cfg Config, tlsConf *tls.Config, uid uuid.UUID) (time.Duration, client.OpAMPClient, error) {
	var c client.OpAMPClient
	logger := opampLogger{}
	if cfg.transport() == transportWS {
		c = client.NewWebSocket(logger)
	} else {
		c = client.NewHTTP(logger)
	}

	if err := c.SetAgentDescription(agentDescription(cfg)); err != nil {
		return 0, nil, err
	}
	if err := c.SetHealth(&protobufs.ComponentHealth{
		Healthy:            true,
		StartTimeUnixNano:  uint64(time.Now().UnixNano()), //nolint:gosec // wall-clock nanos, never negative
		StatusTimeUnixNano: uint64(time.Now().UnixNano()), //nolint:gosec // wall-clock nanos, never negative
	}); err != nil {
		return 0, nil, err
	}

	connected := make(chan struct{}, 1)
	hb := cfg.Heartbeat
	start := time.Now()
	err := c.Start(ctx, clienttypes.StartSettings{
		OpAMPServerURL:    cfg.ServerURL,
		TLSConfig:         tlsConf,
		InstanceUid:       clienttypes.InstanceUid(uid),
		Capabilities:      agentCapabilities,
		HeartbeatInterval: &hb,
		Callbacks: clienttypes.Callbacks{
			OnConnect: func(context.Context) {
				select {
				case connected <- struct{}{}:
				default:
				}
			},
		},
	})
	if err != nil {
		return 0, nil, err
	}

	select {
	case <-connected:
		return time.Since(start), c, nil
	case <-ctx.Done():
		stop(c)
		return 0, nil, errors.New("connect timed out before deadline")
	}
}

func agentDescription(cfg Config) *protobufs.AgentDescription {
	name := cfg.ServiceName
	if name == "" {
		name = "otelcol-contrib"
	}
	return &protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{
			stringKV("service.name", name),
		},
		NonIdentifyingAttributes: []*protobufs.KeyValue{
			stringKV("opamp.managed_by", supervisorManagedBy),
		},
	}
}

func stringKV(key, value string) *protobufs.KeyValue {
	return &protobufs.KeyValue{
		Key:   key,
		Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: value}},
	}
}

// stop shuts a client down with a short bounded timeout so a hung close does
// not stall the whole run.
func stop(c client.OpAMPClient) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Stop(ctx)
}

// sleepUntil blocks until t or ctx ends, reporting true if it reached t.
func sleepUntil(ctx context.Context, t time.Time) bool {
	d := time.Until(t)
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// tlsConfig builds a client TLS config from the cert files, or nil for a
// plaintext run when none are set.
func (c Config) tlsConfig() (*tls.Config, error) {
	if c.CertFile == "" && c.CAFile == "" {
		return nil, nil
	}
	conf := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("ca file %s has no certificates", c.CAFile)
		}
		conf.RootCAs = pool
	}
	if c.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load client cert: %w", err)
		}
		conf.Certificates = []tls.Certificate{cert}
	}
	return conf, nil
}

// instanceUID renders the OpAMP instance_uid bytes as a UUID string.
func instanceUID(b []byte) (string, error) {
	id, err := uuid.FromBytes(b)
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// opampLogger drops opamp-go's internal client logging; at fleet scale a log
// line per client per event is its own load. Failures surface through
// Result.Err instead.
type opampLogger struct{}

func (opampLogger) Debugf(context.Context, string, ...any) {}
func (opampLogger) Errorf(context.Context, string, ...any) {}
