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
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
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

// Server owns the grex listeners and their lifecycles.
type Server struct {
	log      *slog.Logger
	cfg      *config.Config
	registry *prometheus.Registry

	listeners map[string]*listener
	fatal     chan error
}

type listener struct {
	addr string
	srv  *http.Server
	lis  net.Listener
}

// New builds a Server from the configuration. Call Start to bind and serve.
func New(cfg *config.Config, logger *slog.Logger, opamp OpAMP) *Server {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	telemetryMux := http.NewServeMux()
	telemetryMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})
	telemetryMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})
	telemetryMux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

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

	return &Server{
		log:      logger,
		cfg:      cfg,
		registry: registry,
		listeners: map[string]*listener{
			"opamp": {addr: cfg.Listeners.OpAMP, srv: &http.Server{
				Handler:           opampHandler,
				ConnContext:       opamp.ConnContext,
				ReadHeaderTimeout: 10 * time.Second,
			}},
			"ui":        {addr: cfg.Listeners.UI, srv: &http.Server{Handler: notImplemented, ReadHeaderTimeout: 10 * time.Second}},
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
	return nil
}

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

// Shutdown gracefully stops all listeners, waiting up to the context deadline.
func (s *Server) Shutdown(ctx context.Context) error {
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
