package opamp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/dennisme/grex/internal/fleet"
)

func TestNoopMetricsAndNilNew(t *testing.T) {
	t.Parallel()
	var n noopMetrics
	n.Message()
	n.MessageError()
	n.GatewayConnect(true)
	n.GatewayConnect(false)
	n.GatewayConnectionOpened()
	n.GatewayConnectionClosed()

	// nil metrics → noopMetrics
	r := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)), r, nil)
	if h == nil {
		t.Fatal("New with nil metrics")
	}
}

type fakeConn struct {
	c net.Conn
}

func (f *fakeConn) Connection() net.Conn { return f.c }
func (f *fakeConn) Send(context.Context, *protobufs.ServerToAgent) error {
	return nil
}
func (f *fakeConn) Disconnect() error { return nil }

func newFakeConn(t *testing.T) *fakeConn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return &fakeConn{c: server}
}

// recordingMetrics counts handler metric hook firings.
type recordingMetrics struct {
	messages, messageErrors              int
	accepted, rejected                   int
	connectionsOpened, connectionsClosed int
}

func (m *recordingMetrics) Message()      { m.messages++ }
func (m *recordingMetrics) MessageError() { m.messageErrors++ }
func (m *recordingMetrics) GatewayConnect(accepted bool) {
	if accepted {
		m.accepted++
	} else {
		m.rejected++
	}
}
func (m *recordingMetrics) GatewayConnectionOpened() { m.connectionsOpened++ }
func (m *recordingMetrics) GatewayConnectionClosed() { m.connectionsClosed++ }

func testHandler() (*Handler, *fleet.Registry) {
	h, registry, _ := testHandlerMetrics()
	return h, registry
}

// newConnState builds the per-connection bookkeeping the handler allocates in
// onConnecting, letting unit tests drive onMessage/onConnectionClose directly
// with a chosen transport.
func newConnState(transport string) *connState {
	return &connState{transport: transport, agents: make(map[string]struct{})}
}

func testHandlerMetrics() (*Handler, *fleet.Registry, *recordingMetrics) {
	registry := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	rec := &recordingMetrics{}
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)), registry, rec), registry, rec
}

func connectMsg(t *testing.T, requestUID string) *protobufs.AgentToServer {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"request_uid":    requestUID,
		"remote_address": "172.16.0.9:54321",
		"headers":        http.Header{"User-Agent": []string{"opamp-go"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &protobufs.AgentToServer{
		CustomMessage: &protobufs.CustomMessage{
			Capability: GatewayCapability,
			Type:       "connect",
			Data:       data,
		},
	}
}

func decodeConnectResult(t *testing.T, reply *protobufs.ServerToAgent) map[string]any {
	t.Helper()
	if reply == nil || reply.CustomMessage == nil {
		t.Fatal("reply has no custom message")
	}
	if reply.CustomMessage.Capability != GatewayCapability {
		t.Fatalf("capability = %q", reply.CustomMessage.Capability)
	}
	if reply.CustomMessage.Type != "connectResult" {
		t.Fatalf("type = %q", reply.CustomMessage.Type)
	}
	var result map[string]any
	if err := json.Unmarshal(reply.CustomMessage.Data, &result); err != nil {
		t.Fatalf("connectResult JSON: %v", err)
	}
	return result
}

func TestGatewayConnectAccepted(t *testing.T) {
	h, registry := testHandler()
	conn := newFakeConn(t)
	cs := newConnState("ws")

	reply := h.onMessage(context.Background(), cs, conn, connectMsg(t, "req-1"))
	result := decodeConnectResult(t, reply)
	if result["accept"] != true {
		t.Errorf("accept = %v", result["accept"])
	}
	if result["http_status_code"] != float64(http.StatusOK) {
		t.Errorf("http_status_code = %v", result["http_status_code"])
	}
	if result["request_uid"] != "req-1" {
		t.Errorf("request_uid = %v", result["request_uid"])
	}

	// The connection is now known to be a gateway: relayed agents get
	// ViaGateway metadata.
	uid := uuid.New()
	h.onMessage(context.Background(), cs, conn, &protobufs.AgentToServer{InstanceUid: uid[:]})
	agent, ok := registry.Get(uid.String())
	if !ok {
		t.Fatal("relayed agent not registered")
	}
	if !agent.Conn.ViaGateway {
		t.Error("ViaGateway = false for agent on gateway connection")
	}
}

func TestGatewayConnectMalformed(t *testing.T) {
	h, _ := testHandler()
	msg := connectMsg(t, "req-2")
	msg.CustomMessage.Data = []byte("{not json")

	result := decodeConnectResult(t, h.onMessage(context.Background(), newConnState("ws"), newFakeConn(t), msg))
	if result["accept"] != false {
		t.Errorf("accept = %v, want false for malformed payload", result["accept"])
	}
	if result["http_status_code"] != float64(http.StatusBadRequest) {
		t.Errorf("http_status_code = %v, want 400", result["http_status_code"])
	}
}

func TestReportFeedsRegistry(t *testing.T) {
	h, registry := testHandler()
	conn := newFakeConn(t)
	cs := newConnState("ws")
	uid := uuid.New()

	reply := h.onMessage(context.Background(), cs, conn, &protobufs.AgentToServer{
		InstanceUid: uid[:],
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key:   "service.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "otelcol"}},
			}},
		},
	})
	if !bytes.Equal(reply.InstanceUid, uid[:]) {
		t.Error("reply instance uid not echoed")
	}
	if reply.Capabilities&uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus) == 0 {
		t.Error("reply missing AcceptsStatus capability")
	}

	agent, ok := registry.Get(uid.String())
	if !ok {
		t.Fatal("agent not registered")
	}
	if agent.Identifying["service.name"] != "otelcol" {
		t.Errorf("Identifying = %v", agent.Identifying)
	}
	if agent.Conn.ViaGateway {
		t.Error("ViaGateway = true for direct connection")
	}
	if agent.Conn.RemoteAddr == "" {
		t.Error("RemoteAddr not recorded")
	}
}

func TestHandlerMetrics(t *testing.T) {
	h, _, rec := testHandlerMetrics()
	conn := newFakeConn(t)
	cs := newConnState("ws")
	uid := uuid.New()

	// Two accepted connects on one connection: the gateway gauge opens once.
	h.onMessage(context.Background(), cs, conn, connectMsg(t, "req-1"))
	h.onMessage(context.Background(), cs, conn, connectMsg(t, "req-2"))
	// One malformed connect: rejected.
	bad := connectMsg(t, "req-3")
	bad.CustomMessage.Data = []byte("{not json")
	h.onMessage(context.Background(), cs, conn, bad)
	// A regular report.
	h.onMessage(context.Background(), cs, conn, &protobufs.AgentToServer{InstanceUid: uid[:]})
	// Closing the gateway connection closes the gauge.
	h.onConnectionClose(cs)
	// Closing a non-gateway connection does not.
	h.onConnectionClose(newConnState("ws"))
	// Read errors are counted.
	h.onReadMessageError(nil, 0, nil, io.ErrUnexpectedEOF)

	if rec.messages != 4 {
		t.Errorf("messages = %d, want 4", rec.messages)
	}
	if rec.accepted != 2 || rec.rejected != 1 {
		t.Errorf("connects = %d accepted %d rejected, want 2/1", rec.accepted, rec.rejected)
	}
	if rec.connectionsOpened != 1 {
		t.Errorf("connectionsOpened = %d, want 1", rec.connectionsOpened)
	}
	if rec.connectionsClosed != 1 {
		t.Errorf("connectionsClosed = %d, want 1", rec.connectionsClosed)
	}
	if rec.messageErrors != 1 {
		t.Errorf("messageErrors = %d, want 1", rec.messageErrors)
	}
}

// A grex restart behind a gateway leaves agents connected on the agent side,
// so they never resend their full state on their own. The server must request
// it when an agent's entry has no description.
func TestRequestsFullStateFromDescriptionlessAgent(t *testing.T) {
	h, _ := testHandler()
	conn := newFakeConn(t)
	cs := newConnState("ws")
	uid := uuid.New()
	fullState := uint64(protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportFullState)

	heartbeat := &protobufs.AgentToServer{InstanceUid: uid[:]}
	reply := h.onMessage(context.Background(), cs, conn, heartbeat)
	if reply.Flags&fullState == 0 {
		t.Error("first heartbeat from unknown agent did not request full state")
	}

	reply = h.onMessage(context.Background(), cs, conn, heartbeat)
	if reply.Flags&fullState == 0 {
		t.Error("repeat heartbeat without description did not request full state")
	}

	reply = h.onMessage(context.Background(), cs, conn, &protobufs.AgentToServer{
		InstanceUid: uid[:],
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key:   "service.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "otelcol"}},
			}},
		},
	})
	if reply.Flags&fullState != 0 {
		t.Error("full-state report still flagged for full state")
	}

	reply = h.onMessage(context.Background(), cs, conn, heartbeat)
	if reply.Flags&fullState != 0 {
		t.Error("heartbeat after description recorded still flagged for full state")
	}
}

func TestTransportDetection(t *testing.T) {
	wsReq := httptest.NewRequest(http.MethodGet, "/v1/opamp", nil)
	wsReq.Header.Set("Upgrade", "websocket")
	if got := transportFor(wsReq); got != "ws" {
		t.Errorf("transportFor(upgrade) = %q, want ws", got)
	}
	if got := transportFor(httptest.NewRequest(http.MethodPost, "/v1/opamp", nil)); got != "http" {
		t.Errorf("transportFor(plain) = %q, want http", got)
	}
}

// TestOnConnectingAcceptsBeforeDrain and TestDrainRefusesNewConnections
// cover the shutdown-drain fix (docs/spec/design.md): once Drain is
// called, new connection attempts are refused with 503 so their own
// exponential-backoff-with-jitter reconnect (opamp-go) lands them on a
// different, still-ready replica — existing already-open connections are
// untouched, only new ones are refused.
func TestOnConnectingAcceptsBeforeDrain(t *testing.T) {
	h, _ := testHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/opamp", nil)
	resp := h.onConnecting(req)
	if !resp.Accept {
		t.Errorf("Accept = false before Drain, want true")
	}
}

func TestDrainRefusesNewConnections(t *testing.T) {
	h, _ := testHandler()
	h.Drain()
	req := httptest.NewRequest(http.MethodGet, "/v1/opamp", nil)
	resp := h.onConnecting(req)
	if resp.Accept {
		t.Error("Accept = true after Drain, want false")
	}
	if resp.HTTPStatusCode != http.StatusServiceUnavailable {
		t.Errorf("HTTPStatusCode = %d, want %d", resp.HTTPStatusCode, http.StatusServiceUnavailable)
	}
}

// A read error is per-connection: a mass disconnect (AZ blip, gateway crash)
// at fleet scale fires one for every dropped connection. It logs at Debug so
// that burst costs zero log I/O at the default info level — the aggregate
// signal lives in grex_opamp_message_errors_total. Same reasoning as Sweep's
// per-agent logging (docs/spec/design.md's Scaling gaps, gap 2).
func TestReadMessageErrorLogsAtDebug(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	registry := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	rec := &recordingMetrics{}
	h := New(logger, registry, rec)

	h.onReadMessageError(nil, 0, nil, io.ErrUnexpectedEOF)

	var entry struct {
		Level string `json:"level"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("decode log line %q: %v", buf.String(), err)
	}
	if entry.Level != "DEBUG" {
		t.Errorf("read error logged at %q, want DEBUG", entry.Level)
	}
	if rec.messageErrors != 1 {
		t.Errorf("messageErrors = %d, want 1", rec.messageErrors)
	}
}

// Concurrent messages from distinct agents on distinct connections must not
// contend on any shared Handler lock (each connection owns its connState) and
// must all land in the registry. Run under -race to catch a reintroduced
// shared-state race.
func TestConcurrentMessagesDistinctConnections(t *testing.T) {
	// nil metrics → noopMetrics: the recording double is a plain int counter,
	// racy under concurrent use and irrelevant to what this test checks.
	registry := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	h := New(slog.New(slog.NewTextHandler(io.Discard, nil)), registry, nil)
	const agents = 200

	uids := make([]uuid.UUID, agents)
	for i := range uids {
		uids[i] = uuid.New()
	}

	var wg sync.WaitGroup
	for i := range uids {
		wg.Add(1)
		go func(uid uuid.UUID) {
			defer wg.Done()
			cs := newConnState("ws")
			h.onMessage(context.Background(), cs, newFakeConn(t), &protobufs.AgentToServer{InstanceUid: uid[:]})
		}(uids[i])
	}
	wg.Wait()

	for _, uid := range uids {
		if _, ok := registry.Get(uid.String()); !ok {
			t.Fatalf("agent %s not registered after concurrent reports", uid)
		}
	}
}

// A WebSocket close is a real disconnect: opamp-go delivers it once, when the
// single per-connection goroutine's read loop ends. The agent stays registered
// (until Sweep evicts it) but flips to not connected.
func TestWSConnectionCloseMarksDisconnected(t *testing.T) {
	h, registry := testHandler()
	conn := newFakeConn(t)
	cs := newConnState("ws")
	uid := uuid.New()

	h.onMessage(context.Background(), cs, conn, &protobufs.AgentToServer{InstanceUid: uid[:]})
	h.onConnectionClose(cs)

	agent, ok := registry.Get(uid.String())
	if !ok {
		t.Fatal("agent evicted on disconnect; should stay until sweep")
	}
	if agent.Connected {
		t.Error("Connected = true after connection close")
	}
}

// A plain-HTTP close fires at the end of every request (opamp-go
// serverimpl.go), so it must NOT flip the agent to disconnected — that would
// flap an HTTP-polling agent connected/disconnected each poll. Liveness for
// HTTP agents is owned by Registry.Sweep via LastSeen, same as gateway-relayed
// agents with no per-agent close signal.
func TestHTTPConnectionCloseKeepsConnected(t *testing.T) {
	h, registry := testHandler()
	conn := newFakeConn(t)
	cs := newConnState("http")
	uid := uuid.New()

	h.onMessage(context.Background(), cs, conn, &protobufs.AgentToServer{InstanceUid: uid[:]})
	h.onConnectionClose(cs)

	agent, ok := registry.Get(uid.String())
	if !ok {
		t.Fatal("agent missing after HTTP request close")
	}
	if !agent.Connected {
		t.Error("Connected = false after HTTP request close; HTTP close must not disconnect")
	}
}
