// Package server runs the three grex listeners: OpAMP, UI, and telemetry.
// The OpAMP and UI listeners are placeholders until their features land;
// the telemetry listener serves health probes and Prometheus metrics.
package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/dennisme/grex/internal/config"
)

// OpAMP carries the protocol handler mounted on the OpAMP listener. A zero
// value leaves the listener as a 501 stub.
type OpAMP struct {
	// Handler serves /v1/opamp.
	Handler http.Handler
	// ConnContext is installed on the listener's http.Server so the plain
	// HTTP OpAMP transport can reach the underlying net.Conn.
	ConnContext func(ctx context.Context, c net.Conn) context.Context
}

// UI carries the root handler mounted on the UI listener. A zero value leaves
// the listener as a 501 stub. The handler typically serves the read API,
// embedded web UI, and static assets.
type UI struct {
	// Handler is the root handler for the UI listener.
	Handler http.Handler
}

// Server owns the grex listeners and their lifecycles.
type Server struct {
	log      *slog.Logger
	cfg      *config.Config
	registry *prometheus.Registry
	ready    *atomic.Bool

	listeners map[string]*listener
	fatal     chan error
}

type listener struct {
	addr string
	srv  *http.Server
	lis  net.Listener
}

// New builds a Server from the configuration. The serverRegistry backs the
// telemetry listener's /metrics endpoint (server internals); fleetRegistry
// backs /metrics/fleet (per-fleet series), kept separate so operators can
// scrape them as independent jobs with independent limits. Call Start to
// bind and serve.
func New(cfg *config.Config, logger *slog.Logger, opamp OpAMP, ui UI, serverRegistry, fleetRegistry *prometheus.Registry) *Server {
	// Zero value false: not ready until Start binds and begins serving.
	ready := &atomic.Bool{}

	telemetryMux := http.NewServeMux()
	telemetryMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		// Liveness only: is the process alive and its handlers responsive.
		// Never reflects readiness or downstream state, so it does not flip
		// during a graceful drain and cannot flap on transient dependency
		// issues.
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})
	telemetryMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			http.Error(w, "draining", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})
	telemetryMux.Handle("/metrics", promhttp.HandlerFor(serverRegistry, promhttp.HandlerOpts{}))
	telemetryMux.Handle("/metrics/fleet", promhttp.HandlerFor(fleetRegistry, promhttp.HandlerOpts{}))
	if cfg.Debug.PprofEnabled {
		// Registered on our own mux, not http.DefaultServeMux, and gated
		// behind an explicit opt-in: pprof exposes memory contents and its
		// profiling handlers are themselves a load an operator must choose
		// to accept.
		telemetryMux.HandleFunc("/debug/pprof/", pprof.Index)
		telemetryMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		telemetryMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		telemetryMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		telemetryMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		logger.Warn("pprof endpoints enabled", "path", "/debug/pprof")
	}

	notImplemented := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	})

	opampHandler := http.Handler(notImplemented)
	if opamp.Handler != nil {
		opampMux := http.NewServeMux()
		opampMux.Handle("/v1/opamp", opamp.Handler)
		opampMux.Handle("/", notImplemented)
		opampHandler = opampMux
	}

	uiHandler := http.Handler(notImplemented)
	if ui.Handler != nil {
		uiHandler = ui.Handler
	}

	return &Server{
		log:      logger,
		cfg:      cfg,
		registry: serverRegistry,
		ready:    ready,
		listeners: map[string]*listener{
			"opamp": {addr: cfg.Listeners.OpAMP, srv: &http.Server{
				Handler:           opampHandler,
				ConnContext:       opamp.ConnContext,
				ReadHeaderTimeout: 10 * time.Second,
			}},
			"ui":        {addr: cfg.Listeners.UI, srv: &http.Server{Handler: uiHandler, ReadHeaderTimeout: 10 * time.Second}},
			"telemetry": {addr: cfg.Listeners.Telemetry, srv: &http.Server{Handler: telemetryMux, ReadHeaderTimeout: 10 * time.Second}},
		},
		fatal: make(chan error, 3),
	}
}

// Start binds all listeners and begins serving. The OpAMP listener terminates
// TLS when certificates are configured. Start returns an error if the TLS
// material cannot be loaded or any address cannot be bound, closing whatever
// was already bound.
func (s *Server) Start() error {
	opampTLS, err := opampTLSConfig(s.cfg.TLS)
	if err != nil {
		return err
	}
	for name, l := range s.listeners {
		lis, err := net.Listen("tcp", l.addr)
		if err != nil {
			s.closeListeners()
			return fmt.Errorf("bind %s listener on %s: %w", name, l.addr, err)
		}
		if name == "opamp" && opampTLS != nil {
			lis = tls.NewListener(lis, opampTLS)
		}
		l.lis = lis
		s.log.Info("listener started", "name", name, "addr", lis.Addr().String(),
			"tls", name == "opamp" && opampTLS != nil)
	}
	for name, l := range s.listeners {
		go func(name string, l *listener) {
			if err := l.srv.Serve(l.lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.fatal <- fmt.Errorf("%s listener: %w", name, err)
			}
		}(name, l)
	}
	s.ready.Store(true)
	return nil
}

// BeginDraining marks the server not ready so /readyz starts returning 503,
// before any listener closes. Callers should give orchestrators a window to
// observe this (their readiness probe interval) before calling Shutdown, so
// new traffic stops arriving before in-flight connections are cut.
func (s *Server) BeginDraining() { s.ready.Store(false) }

// Fatal reports the first unrecoverable serve error.
func (s *Server) Fatal() <-chan error { return s.fatal }

// OpAMPAddr returns the bound OpAMP listener address. Valid after Start.
func (s *Server) OpAMPAddr() string { return s.boundAddr("opamp") }

// UIAddr returns the bound UI listener address. Valid after Start.
func (s *Server) UIAddr() string { return s.boundAddr("ui") }

// TelemetryAddr returns the bound telemetry listener address. Valid after Start.
func (s *Server) TelemetryAddr() string { return s.boundAddr("telemetry") }

func (s *Server) boundAddr(name string) string {
	l := s.listeners[name]
	if l.lis == nil {
		return ""
	}
	return l.lis.Addr().String()
}

// Shutdown gracefully stops all listeners, waiting up to the context
// deadline. Marks the server not ready first (idempotent with BeginDraining)
// so a caller that skips the explicit drain step still fails /readyz before
// listeners close.
func (s *Server) Shutdown(ctx context.Context) error {
	s.BeginDraining()
	var errs []error
	for name, l := range s.listeners {
		if l.lis == nil {
			continue
		}
		if err := l.srv.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown %s listener: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// opampTLSConfig builds the OpAMP listener TLS configuration, or nil when TLS
// is not configured. A client CA turns on required client certificate
// verification (mTLS).
func opampTLSConfig(c config.TLS) (*tls.Config, error) {
	if c.CertFile == "" {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS keypair: %w", err)
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if c.ClientCAFile != "" {
		caPEM, err := os.ReadFile(c.ClientCAFile) //nolint:gosec // path is the operator-chosen CA bundle
		if err != nil {
			return nil, fmt.Errorf("read client CA bundle: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("client CA bundle %s contains no certificates", c.ClientCAFile)
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

func (s *Server) closeListeners() {
	for _, l := range s.listeners {
		if l.lis != nil {
			_ = l.lis.Close()
			l.lis = nil
		}
	}
}
