package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testCerts holds paths to a generated CA, server, and client certificate.
type testCerts struct {
	caFile         string
	caPool         *x509.CertPool
	serverCert     string
	serverKey      string
	clientTLSCert  tls.Certificate
	clientCertFile string
}

func genCerts(t *testing.T) testCerts {
	t.Helper()
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "grex test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	issue := func(name string, extUsage x509.ExtKeyUsage, ips []net.IP) (certPEM, keyPEM []byte) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: name},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{extUsage},
			IPAddresses:  ips,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		return certPEM, keyPEM
	}

	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	serverCertPEM, serverKeyPEM := issue("grex", x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")})
	clientCertPEM, clientKeyPEM := issue("otelcol", x509.ExtKeyUsageClientAuth, nil)

	clientTLSCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return testCerts{
		caFile:         write("ca.pem", caPEM),
		caPool:         pool,
		serverCert:     write("server.pem", serverCertPEM),
		serverKey:      write("server-key.pem", serverKeyPEM),
		clientTLSCert:  clientTLSCert,
		clientCertFile: write("client.pem", clientCertPEM),
	}
}

func startTLSServer(t *testing.T, certs testCerts, mtls bool) *Server {
	t.Helper()
	cfg := testConfig()
	cfg.TLS.CertFile = certs.serverCert
	cfg.TLS.KeyFile = certs.serverKey
	if mtls {
		cfg.TLS.ClientCAFile = certs.caFile
	}
	s := New(cfg, testLogger())
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return s
}

func tlsGet(t *testing.T, addr string, tlsCfg *tls.Config) (int, error) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}

func TestOpAMPListenerServesTLS(t *testing.T) {
	certs := genCerts(t)
	s := startTLSServer(t, certs, false)

	code, err := tlsGet(t, s.OpAMPAddr(), &tls.Config{RootCAs: certs.caPool, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("TLS GET: %v", err)
	}
	if code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", code, http.StatusNotImplemented)
	}
}

func TestOpAMPListenerRequiresClientCert(t *testing.T) {
	certs := genCerts(t)
	s := startTLSServer(t, certs, true)

	code, err := tlsGet(t, s.OpAMPAddr(), &tls.Config{
		RootCAs:      certs.caPool,
		Certificates: []tls.Certificate{certs.clientTLSCert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("mTLS GET with client cert: %v", err)
	}
	if code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", code, http.StatusNotImplemented)
	}

	if _, err := tlsGet(t, s.OpAMPAddr(), &tls.Config{RootCAs: certs.caPool, MinVersion: tls.VersionTLS12}); err == nil {
		t.Error("GET without client cert succeeded, want handshake rejection")
	}
}

func TestUIAndTelemetryStayPlainHTTP(t *testing.T) {
	certs := genCerts(t)
	s := startTLSServer(t, certs, true)

	if code, _ := get(t, "http://"+s.TelemetryAddr()+"/healthz"); code != http.StatusOK {
		t.Errorf("telemetry /healthz over plain HTTP = %d, want 200", code)
	}
	if code, _ := get(t, "http://"+s.UIAddr()+"/"); code != http.StatusNotImplemented {
		t.Errorf("ui over plain HTTP = %d, want 501", code)
	}
}
