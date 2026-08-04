package synth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dennisme/grex/internal/testcert"
)

func TestTLSConfigPlaintext(t *testing.T) {
	conf, err := Config{}.tlsConfig()
	if err != nil {
		t.Fatalf("tlsConfig: %v", err)
	}
	if conf != nil {
		t.Errorf("tlsConfig() = %v, want nil for a plaintext run", conf)
	}
}

func TestTLSConfigCAOnly(t *testing.T) {
	certs := testcert.Gen(t)
	conf, err := Config{CAFile: certs.CAFile}.tlsConfig()
	if err != nil {
		t.Fatalf("tlsConfig: %v", err)
	}
	if conf.RootCAs == nil {
		t.Error("RootCAs not set from CAFile")
	}
	if len(conf.Certificates) != 0 {
		t.Error("client certificate set without cert/key")
	}
}

func TestTLSConfigMutualTLS(t *testing.T) {
	certs := testcert.Gen(t)
	// The server keypair is used only as a file-backed cert/key to exercise
	// LoadX509KeyPair; tlsConfig does not care what role the cert plays.
	conf, err := Config{
		CertFile: certs.ServerCertFile,
		KeyFile:  certs.ServerKeyFile,
		CAFile:   certs.CAFile,
	}.tlsConfig()
	if err != nil {
		t.Fatalf("tlsConfig: %v", err)
	}
	if conf.RootCAs == nil {
		t.Error("RootCAs not set")
	}
	if len(conf.Certificates) != 1 {
		t.Errorf("Certificates = %d, want 1", len(conf.Certificates))
	}
}

func TestTLSConfigErrors(t *testing.T) {
	certs := testcert.Gen(t)
	emptyCA := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(emptyCA, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"missing ca file", Config{CAFile: "/no/such/ca.pem"}, "read ca"},
		{"ca without certs", Config{CAFile: emptyCA}, "no certificates"},
		{"bad cert pair", Config{CertFile: "/no/cert.pem", KeyFile: "/no/key.pem", CAFile: certs.CAFile}, "load client cert"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.cfg.tlsConfig()
			if err == nil {
				t.Fatalf("tlsConfig() = nil, want error mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("tlsConfig() = %q, want it to mention %q", err.Error(), tt.want)
			}
		})
	}
}

func TestRunRejectsBadTLS(t *testing.T) {
	cfg := validConfig()
	cfg.CAFile = "/no/such/ca.pem"
	if _, err := Run(context.Background(), cfg, discardSlog()); err == nil {
		t.Fatal("Run with unreadable CA = nil error, want failure")
	}
}

func TestInstanceUIDRejectsBadBytes(t *testing.T) {
	if _, err := instanceUID([]byte{0x01, 0x02}); err == nil {
		t.Error("instanceUID(2 bytes) = nil error, want failure")
	}
}

func TestOpAMPLoggerIsQuiet(t *testing.T) {
	// The logger drops everything; call both methods so a future change that
	// makes them do work is visible in coverage.
	l := opampLogger{}
	l.Debugf(context.Background(), "debug %d", 1)
	l.Errorf(context.Background(), "error %s", "x")
}
