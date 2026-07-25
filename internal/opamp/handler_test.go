package opamp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/dennisme/grex/internal/fleet"
)

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

func testHandler() (*Handler, *fleet.Registry) {
	registry := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)), registry), registry
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

	reply := h.onMessage(context.Background(), conn, connectMsg(t, "req-1"))
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
	h.onMessage(context.Background(), conn, &protobufs.AgentToServer{InstanceUid: uid[:]})
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

	result := decodeConnectResult(t, h.onMessage(context.Background(), newFakeConn(t), msg))
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
	uid := uuid.New()

	reply := h.onMessage(context.Background(), conn, &protobufs.AgentToServer{
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

func TestConnectionCloseMarksDisconnected(t *testing.T) {
	h, registry := testHandler()
	conn := newFakeConn(t)
	uid := uuid.New()

	h.onMessage(context.Background(), conn, &protobufs.AgentToServer{InstanceUid: uid[:]})
	h.onConnectionClose(conn)

	agent, ok := registry.Get(uid.String())
	if !ok {
		t.Fatal("agent evicted on disconnect; should stay until sweep")
	}
	if agent.Connected {
		t.Error("Connected = true after connection close")
	}
}
