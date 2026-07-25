package opamp

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/client"
	clienttypes "github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/dennisme/grex/internal/config"
	"github.com/dennisme/grex/internal/fleet"
	"github.com/dennisme/grex/internal/metrics"
	"github.com/dennisme/grex/internal/server"
	"github.com/dennisme/grex/internal/testcert"
)

// startStack runs the full grex server with the OpAMP handler attached and
// mTLS required, returning the OpAMP address and the registry.
func startStack(t *testing.T, certs testcert.Certs) (string, *fleet.Registry) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, logger, nil)
	handler := New(logger, registry, nil)
	httpHandler, connCtx, err := handler.Attach()
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}

	cfg := &config.Config{
		Listeners: config.Listeners{
			OpAMP:     "127.0.0.1:0",
			UI:        "127.0.0.1:0",
			Telemetry: "127.0.0.1:0",
		},
		TLS: config.TLS{
			CertFile:     certs.ServerCertFile,
			KeyFile:      certs.ServerKeyFile,
			ClientCAFile: certs.CAFile,
		},
		Fleet: config.Fleet{HeartbeatInterval: 30 * time.Second, StaleMissedHeartbeats: 3},
		Log:   config.Log{Level: "info", Format: "text"},
	}
	srv := server.New(cfg, logger, server.OpAMP{Handler: httpHandler, ConnContext: connCtx}, metrics.NewRegistry(), prometheus.NewRegistry())
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return srv.OpAMPAddr(), registry
}

func clientTLS(certs testcert.Certs) *tls.Config {
	return &tls.Config{
		RootCAs:      certs.CAPool,
		Certificates: []tls.Certificate{certs.ClientTLSCert},
		MinVersion:   tls.VersionTLS12,
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startClient(t *testing.T, c client.OpAMPClient, url string, certs testcert.Certs, uid uuid.UUID) {
	t.Helper()
	err := c.SetAgentDescription(&protobufs.AgentDescription{
		IdentifyingAttributes: []*protobufs.KeyValue{{
			Key:   "service.name",
			Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "e2e-agent"}},
		}},
	})
	if err != nil {
		t.Fatalf("SetAgentDescription: %v", err)
	}
	err = c.Start(context.Background(), clienttypes.StartSettings{
		OpAMPServerURL: url,
		TLSConfig:      clientTLS(certs),
		InstanceUid:    clienttypes.InstanceUid(uid),
		Callbacks: clienttypes.Callbacks{
			OnConnectFailed: func(_ context.Context, err error) {
				t.Logf("connect failed (will retry): %v", err)
			},
		},
	})
	if err != nil {
		t.Fatalf("client Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Stop(ctx)
	})
}

func TestWebSocketAgentEndToEnd(t *testing.T) {
	certs := testcert.Gen(t)
	addr, registry := startStack(t, certs)
	uid := uuid.New()

	c := client.NewWebSocket(slogOpAMPLogger{slog.New(slog.NewTextHandler(io.Discard, nil))})
	startClient(t, c, "wss://"+addr+"/v1/opamp", certs, uid)

	waitFor(t, "agent registration over WebSocket", func() bool {
		agent, ok := registry.Get(uid.String())
		return ok && agent.Identifying["service.name"] == "e2e-agent" && agent.Connected
	})

	agent, _ := registry.Get(uid.String())
	if agent.Conn.ViaGateway {
		t.Error("ViaGateway = true for direct agent")
	}
	if agent.Conn.TLSSubject == "" {
		t.Error("TLSSubject empty; client certificate identity not recorded")
	}
	if agent.Conn.Transport != "ws" {
		t.Errorf("Transport = %q, want ws", agent.Conn.Transport)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Stop(ctx); err != nil {
		t.Fatalf("client Stop: %v", err)
	}
	waitFor(t, "agent marked disconnected", func() bool {
		agent, ok := registry.Get(uid.String())
		return ok && !agent.Connected
	})
}

func TestHTTPPollingAgentEndToEnd(t *testing.T) {
	certs := testcert.Gen(t)
	addr, registry := startStack(t, certs)
	uid := uuid.New()

	c := client.NewHTTP(slogOpAMPLogger{slog.New(slog.NewTextHandler(io.Discard, nil))})
	startClient(t, c, "https://"+addr+"/v1/opamp", certs, uid)

	waitFor(t, "agent registration over HTTP polling", func() bool {
		agent, ok := registry.Get(uid.String())
		return ok && agent.Identifying["service.name"] == "e2e-agent"
	})
	agent, _ := registry.Get(uid.String())
	if agent.Conn.Transport != "http" {
		t.Errorf("Transport = %q, want http", agent.Conn.Transport)
	}
}
