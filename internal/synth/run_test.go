package synth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	opampserver "github.com/open-telemetry/opamp-go/server"
	servertypes "github.com/open-telemetry/opamp-go/server/types"
)

// seenServer is a minimal real OpAMP server that records the instance_uids
// and service.name it sees, used to prove synth agents actually connected.
type seenServer struct {
	mu    sync.Mutex
	uids  map[string]string // instance_uid -> service.name
	conns int
}

func newSeenServer(t *testing.T) (*seenServer, string) {
	t.Helper()
	s := &seenServer{uids: map[string]string{}}
	logger := discardOpAMPLogger{}
	srv := opampserver.New(logger)
	handler, _, err := srv.Attach(opampserver.Settings{
		Callbacks: servertypes.Callbacks{OnConnecting: s.onConnecting},
	})
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/opamp", handler)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, ts.URL
}

func (s *seenServer) onConnecting(*http.Request) servertypes.ConnectionResponse {
	s.mu.Lock()
	s.conns++
	s.mu.Unlock()
	return servertypes.ConnectionResponse{
		Accept: true,
		ConnectionCallbacks: servertypes.ConnectionCallbacks{
			OnMessage: s.onMessage,
		},
	}
}

func (s *seenServer) onMessage(_ context.Context, _ servertypes.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	if desc := msg.GetAgentDescription(); desc != nil {
		for _, kv := range desc.GetIdentifyingAttributes() {
			if kv.GetKey() == "service.name" {
				id, err := instanceUID(msg.GetInstanceUid())
				if err == nil {
					s.mu.Lock()
					s.uids[id] = kv.GetValue().GetStringValue()
					s.mu.Unlock()
				}
			}
		}
	}
	return &protobufs.ServerToAgent{InstanceUid: msg.GetInstanceUid()}
}

func (s *seenServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uids)
}

func TestRunConnectsAllAgents(t *testing.T) {
	server, url := newSeenServer(t)
	cfg := Config{
		ServerURL:   strings.Replace(url, "http://", "ws://", 1) + "/v1/opamp",
		Agents:      3,
		Heartbeat:   time.Second,
		Duration:    2 * time.Second,
		ServiceName: "synth-agent",
	}
	report, err := Run(context.Background(), cfg, discardSlog())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Connected != 3 {
		t.Fatalf("Connected = %d, want 3 (errors: %v)", report.Connected, report.Errors)
	}
	if got := server.count(); got != 3 {
		t.Errorf("server saw %d distinct agents, want 3", got)
	}
}

func TestRunSimulatesRestart(t *testing.T) {
	server, url := newSeenServer(t)
	cfg := Config{
		ServerURL:    strings.Replace(url, "http://", "ws://", 1) + "/v1/opamp",
		Agents:       2,
		Heartbeat:    time.Second,
		Duration:     3 * time.Second,
		RestartAfter: time.Second,
		ServiceName:  "synth-agent",
	}
	report, err := Run(context.Background(), cfg, discardSlog())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Restarted != 2 {
		t.Errorf("Restarted = %d, want 2", report.Restarted)
	}
	// A reconnect opens a second connection per agent.
	server.mu.Lock()
	conns := server.conns
	server.mu.Unlock()
	if conns < 4 {
		t.Errorf("server saw %d connections, want >= 4 (2 agents x 2 connects)", conns)
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	_, err := Run(context.Background(), Config{}, discardSlog())
	if err == nil {
		t.Fatal("Run with empty config = nil error, want validation error")
	}
}

func discardSlog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type discardOpAMPLogger struct{}

func (discardOpAMPLogger) Debugf(context.Context, string, ...any) {}
func (discardOpAMPLogger) Errorf(context.Context, string, ...any) {}
