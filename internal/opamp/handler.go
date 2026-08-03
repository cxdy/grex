// Package opamp bridges the opamp-go server to the fleet registry. It accepts
// direct agent connections and gateway-relayed connections speaking the
// observIQ opampgateway custom capability.
package opamp

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/open-telemetry/opamp-go/protobufs"
	opampserver "github.com/open-telemetry/opamp-go/server"
	servertypes "github.com/open-telemetry/opamp-go/server/types"

	"github.com/dennisme/grex/internal/fleet"
)

// GatewayCapability identifies the observIQ opampgateway custom capability.
const GatewayCapability = "com.bindplane.opamp-gateway"

const (
	gatewayConnectType       = "connect"
	gatewayConnectResultType = "connectResult"
)

// gatewayConnect is the JSON payload of a gateway "connect" custom message:
// the relayed agent's HTTP headers and socket address as seen by the gateway.
type gatewayConnect struct {
	RequestUID    string      `json:"request_uid,omitempty"`
	RemoteAddress string      `json:"remote_address"`
	Headers       http.Header `json:"headers"`
}

// gatewayConnectResult is the JSON payload answering a connect request.
type gatewayConnectResult struct {
	RequestUID      string            `json:"request_uid,omitempty"`
	Accept          bool              `json:"accept"`
	HTTPStatusCode  int               `json:"http_status_code"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
}

// Metrics receives handler telemetry events.
type Metrics interface {
	Message()
	MessageError()
	GatewayConnect(accepted bool)
	GatewayConnectionOpened()
	GatewayConnectionClosed()
}

type noopMetrics struct{}

func (noopMetrics) Message()                 {}
func (noopMetrics) MessageError()            {}
func (noopMetrics) GatewayConnect(bool)      {}
func (noopMetrics) GatewayConnectionOpened() {}
func (noopMetrics) GatewayConnectionClosed() {}

// Handler serves the OpAMP protocol and folds agent state into the registry.
type Handler struct {
	log      *slog.Logger
	registry *fleet.Registry
	srv      opampserver.OpAMPServer
	metrics  Metrics

	// draining is set once by Drain and never cleared — a Handler only
	// drains on the way to shutdown, it doesn't resume.
	draining atomic.Bool
}

// connState is one connection's OpAMP bookkeeping. It's allocated per
// connection in onConnecting and captured by that connection's callbacks, so
// there's no shared Handler lock on the message hot path. opamp-go processes a
// single connection's callbacks serially (verified against v0.23.0: a
// WebSocket connection is one goroutine looping over messages; a plain HTTP
// request is a single OnConnected→OnMessage→OnConnectionClose sequence), so mu
// only guards against that internal contract changing, never real cross-agent
// contention.
type connState struct {
	// transport is the connection's OpAMP transport ("ws" or "http"), fixed at
	// connect time before any message arrives and read-only after, so it needs
	// no lock.
	transport string

	mu sync.Mutex
	// viaGateway is set once the connection sends a gateway connect message;
	// agents reported over it are then recorded as relayed.
	viaGateway bool
	// agents holds the instance uids reported over this connection so a close
	// can mark them disconnected.
	agents map[string]struct{}
}

// New builds a Handler feeding the given registry. A nil metrics receives no
// telemetry events.
func New(logger *slog.Logger, registry *fleet.Registry, metrics Metrics) *Handler {
	if metrics == nil {
		metrics = noopMetrics{}
	}
	return &Handler{
		log:      logger,
		registry: registry,
		srv:      opampserver.New(slogOpAMPLogger{logger}),
		metrics:  metrics,
	}
}

// transportFor classifies an incoming OpAMP request by transport.
func transportFor(r *http.Request) string {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return "ws"
	}
	return "http"
}

// Attach returns the HTTP handler and ConnContext to mount on the OpAMP
// listener.
func (h *Handler) Attach() (http.HandlerFunc, func(ctx context.Context, c net.Conn) context.Context, error) {
	callbacks := servertypes.Callbacks{
		OnConnecting: h.onConnecting,
	}
	handler, connCtx, err := h.srv.Attach(opampserver.Settings{
		Callbacks:          callbacks,
		CustomCapabilities: []string{GatewayCapability},
	})
	if err != nil {
		return nil, nil, err
	}
	return http.HandlerFunc(handler), connCtx, nil
}

// Drain makes onConnecting refuse every new connection from here on,
// leaving already-open ones untouched. Called once, on the way to
// shutdown — a rejected client's own exponential-backoff-with-jitter
// reconnect (opamp-go) lands it on a different, still-ready replica behind
// the same load balancer, no grex-side redirect needed.
func (h *Handler) Drain() {
	h.draining.Store(true)
}

func (h *Handler) onConnecting(r *http.Request) servertypes.ConnectionResponse {
	if h.draining.Load() {
		return servertypes.ConnectionResponse{Accept: false, HTTPStatusCode: http.StatusServiceUnavailable}
	}
	cs := &connState{transport: transportFor(r), agents: make(map[string]struct{})}
	return servertypes.ConnectionResponse{
		Accept: true,
		ConnectionCallbacks: servertypes.ConnectionCallbacks{
			OnMessage: func(ctx context.Context, conn servertypes.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
				return h.onMessage(ctx, cs, conn, msg)
			},
			OnConnectionClose:  func(servertypes.Connection) { h.onConnectionClose(cs) },
			OnReadMessageError: h.onReadMessageError,
		},
	}
}

func (h *Handler) onMessage(_ context.Context, cs *connState, conn servertypes.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	h.metrics.Message()
	if custom := msg.GetCustomMessage(); custom.GetCapability() == GatewayCapability &&
		custom.GetType() == gatewayConnectType {
		return h.onGatewayConnect(cs, msg)
	}

	h.trackAgent(cs, msg)
	h.registry.Report(msg, h.connMeta(cs, conn))
	reply := &protobufs.ServerToAgent{
		InstanceUid:  msg.GetInstanceUid(),
		Capabilities: uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus),
	}
	// An agent whose entry lacks a description has state this server has
	// never seen, e.g. agents connected through a gateway when grex
	// restarts: their connections never drop, so they do not re-report on
	// their own. Ask for everything.
	if h.needsFullState(msg) {
		reply.Flags = uint64(protobufs.ServerToAgentFlags_ServerToAgentFlags_ReportFullState)
	}
	return reply
}

func (h *Handler) needsFullState(msg *protobufs.AgentToServer) bool {
	id, err := fleet.InstanceUID(msg.GetInstanceUid())
	if err != nil {
		return false
	}
	agent, ok := h.registry.Get(id)
	return ok && !agent.DescriptionReported
}

// onGatewayConnect answers a gateway's per-agent auth delegation. The
// connection itself already passed mTLS, so relayed agents are accepted;
// malformed payloads are rejected. The forwarded metadata cannot be joined to
// a specific instance_uid (the protocol carries no agent id in the connect
// message), so it is logged here and agents carry gateway-level metadata.
func (h *Handler) onGatewayConnect(cs *connState, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	cs.mu.Lock()
	if !cs.viaGateway {
		cs.viaGateway = true
		h.metrics.GatewayConnectionOpened()
	}
	cs.mu.Unlock()

	var connect gatewayConnect
	result := gatewayConnectResult{Accept: true, HTTPStatusCode: http.StatusOK}
	if err := json.Unmarshal(msg.GetCustomMessage().GetData(), &connect); err != nil {
		h.log.Warn("rejecting malformed gateway connect", "error", err)
		result = gatewayConnectResult{Accept: false, HTTPStatusCode: http.StatusBadRequest}
	} else {
		result.RequestUID = connect.RequestUID
		h.log.Debug("gateway connect accepted",
			"request_uid", connect.RequestUID,
			"agent_remote_address", connect.RemoteAddress,
			"user_agent", connect.Headers.Get("User-Agent"))
	}
	h.metrics.GatewayConnect(result.Accept)

	data, err := json.Marshal(result)
	if err != nil {
		h.log.Error("marshal gateway connect result", "error", err)
		return nil
	}
	return &protobufs.ServerToAgent{
		InstanceUid: msg.GetInstanceUid(),
		CustomMessage: &protobufs.CustomMessage{
			Capability: GatewayCapability,
			Type:       gatewayConnectResultType,
			Data:       data,
		},
	}
}

func (h *Handler) onConnectionClose(cs *connState) {
	cs.mu.Lock()
	wasGateway := cs.viaGateway
	agents := cs.agents
	cs.agents = nil
	cs.mu.Unlock()
	if wasGateway {
		h.metrics.GatewayConnectionClosed()
	}
	// Only a WebSocket close is a real disconnect. In plain HTTP mode opamp-go
	// fires OnConnectionClose at the end of every request (server/serverimpl.go),
	// so treating it as a disconnect would flap an HTTP-polling agent
	// connected/disconnected on every poll. For HTTP, liveness is left to
	// Registry.Sweep via LastSeen — the same path already relied on for
	// gateway-relayed agents grex never sees a per-agent close for.
	if cs.transport != "ws" {
		return
	}
	for id := range agents {
		h.registry.SetConnected(id, false)
	}
}

func (h *Handler) onReadMessageError(_ servertypes.Connection, _ int, _ []byte, err error) {
	h.metrics.MessageError()
	// Debug, not Warn: this is per-connection, so a mass disconnect (an AZ
	// blip, a gateway crash) fires one per dropped connection — a burst of
	// blocking log writes at fleet scale. grex_opamp_message_errors_total
	// already carries the aggregate at any level. Same reasoning as Sweep's
	// per-agent logging (docs/spec/design.md's Scaling gaps, gap 2).
	h.log.Debug("opamp message read failed", "error", err)
}

func (h *Handler) trackAgent(cs *connState, msg *protobufs.AgentToServer) {
	id, err := fleet.InstanceUID(msg.GetInstanceUid())
	if err != nil {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.agents == nil {
		cs.agents = make(map[string]struct{})
	}
	cs.agents[id] = struct{}{}
}

func (h *Handler) connMeta(cs *connState, conn servertypes.Connection) fleet.ConnMeta {
	cs.mu.Lock()
	meta := fleet.ConnMeta{ViaGateway: cs.viaGateway, Transport: cs.transport}
	cs.mu.Unlock()

	netConn := conn.Connection()
	if netConn == nil {
		return meta
	}
	if addr := netConn.RemoteAddr(); addr != nil {
		meta.RemoteAddr = addr.String()
	}
	if tlsConn, ok := netConn.(*tls.Conn); ok {
		state := tlsConn.ConnectionState()
		if len(state.PeerCertificates) > 0 {
			meta.TLSSubject = state.PeerCertificates[0].Subject.String()
		}
	}
	return meta
}

// slogOpAMPLogger adapts slog to the opamp-go logger interface.
type slogOpAMPLogger struct {
	log *slog.Logger
}

func (l slogOpAMPLogger) Debugf(_ context.Context, format string, v ...any) {
	l.log.Debug("opamp-go: " + fmt.Sprintf(format, v...))
}

func (l slogOpAMPLogger) Errorf(_ context.Context, format string, v ...any) {
	l.log.Error("opamp-go: " + fmt.Sprintf(format, v...))
}
