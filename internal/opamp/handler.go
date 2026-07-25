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
	"sync"

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

// Handler serves the OpAMP protocol and folds agent state into the registry.
type Handler struct {
	log      *slog.Logger
	registry *fleet.Registry
	srv      opampserver.OpAMPServer

	mu sync.Mutex
	// connAgents maps a connection to the instance uids reported over it so
	// a closing connection can mark its agents disconnected.
	connAgents map[servertypes.Connection]map[string]struct{}
	// gatewayConns marks connections that have sent a gateway connect
	// message; agents on them are recorded as relayed.
	gatewayConns map[servertypes.Connection]struct{}
}

// New builds a Handler feeding the given registry.
func New(logger *slog.Logger, registry *fleet.Registry) *Handler {
	return &Handler{
		log:          logger,
		registry:     registry,
		srv:          opampserver.New(slogOpAMPLogger{logger}),
		connAgents:   make(map[servertypes.Connection]map[string]struct{}),
		gatewayConns: make(map[servertypes.Connection]struct{}),
	}
}

// Attach returns the HTTP handler and ConnContext to mount on the OpAMP
// listener.
func (h *Handler) Attach() (http.HandlerFunc, func(ctx context.Context, c net.Conn) context.Context, error) {
	callbacks := servertypes.Callbacks{
		OnConnecting: func(*http.Request) servertypes.ConnectionResponse {
			return servertypes.ConnectionResponse{
				Accept: true,
				ConnectionCallbacks: servertypes.ConnectionCallbacks{
					OnMessage:         h.onMessage,
					OnConnectionClose: h.onConnectionClose,
				},
			}
		},
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

func (h *Handler) onMessage(_ context.Context, conn servertypes.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	if custom := msg.GetCustomMessage(); custom.GetCapability() == GatewayCapability &&
		custom.GetType() == gatewayConnectType {
		return h.onGatewayConnect(conn, msg)
	}

	h.trackAgent(conn, msg)
	h.registry.Report(msg, h.connMeta(conn))
	return &protobufs.ServerToAgent{
		InstanceUid:  msg.GetInstanceUid(),
		Capabilities: uint64(protobufs.ServerCapabilities_ServerCapabilities_AcceptsStatus),
	}
}

// onGatewayConnect answers a gateway's per-agent auth delegation. The
// connection itself already passed mTLS, so relayed agents are accepted;
// malformed payloads are rejected. The forwarded metadata cannot be joined to
// a specific instance_uid (the protocol carries no agent id in the connect
// message), so it is logged here and agents carry gateway-level metadata.
func (h *Handler) onGatewayConnect(conn servertypes.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	h.mu.Lock()
	h.gatewayConns[conn] = struct{}{}
	h.mu.Unlock()

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

func (h *Handler) onConnectionClose(conn servertypes.Connection) {
	h.mu.Lock()
	agents := h.connAgents[conn]
	delete(h.connAgents, conn)
	delete(h.gatewayConns, conn)
	h.mu.Unlock()
	for id := range agents {
		h.registry.SetConnected(id, false)
	}
}

func (h *Handler) trackAgent(conn servertypes.Connection, msg *protobufs.AgentToServer) {
	id, err := fleet.InstanceUID(msg.GetInstanceUid())
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.connAgents[conn] == nil {
		h.connAgents[conn] = make(map[string]struct{})
	}
	h.connAgents[conn][id] = struct{}{}
}

func (h *Handler) connMeta(conn servertypes.Connection) fleet.ConnMeta {
	meta := fleet.ConnMeta{}
	h.mu.Lock()
	_, meta.ViaGateway = h.gatewayConns[conn]
	h.mu.Unlock()

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
