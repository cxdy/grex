package testcert_test

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/testcert"
)

func TestGenProducesUsableTLSMaterial(t *testing.T) {
	certs := testcert.Gen(t)

	for _, path := range []string{certs.CAFile, certs.ServerCertFile, certs.ServerKeyFile, certs.ClientCertFile} {
		if path == "" {
			t.Fatal("expected non-empty cert path")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
	if certs.CAPool == nil {
		t.Fatal("expected CA pool")
	}
	if len(certs.ClientTLSCert.Certificate) == 0 {
		t.Fatal("expected client TLS certificate")
	}

	// Server cert + key load as a keypair.
	serverTLS, err := tls.LoadX509KeyPair(certs.ServerCertFile, certs.ServerKeyFile)
	if err != nil {
		t.Fatalf("server keypair: %v", err)
	}

	// Serve HTTPS with the generated server cert and verify with the CA pool
	// plus optional client cert (mTLS-ready material).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck

	tlsLn := tls.NewListener(ln, &tls.Config{
		Certificates: []tls.Certificate{serverTLS},
		ClientCAs:    certs.CAPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	})
	defer tlsLn.Close() //nolint:errcheck

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = srv.Serve(tlsLn) }()
	t.Cleanup(func() { _ = srv.Close() })

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      certs.CAPool,
				Certificates: []tls.Certificate{certs.ClientTLSCert},
				MinVersion:   tls.VersionTLS12,
			},
		},
		Timeout: 3 * time.Second,
	}
	resp, err := client.Get("https://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatalf("mTLS GET: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// CA pool trusts the server certificate subject.
	caPEM, err := os.ReadFile(certs.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("could not parse CA PEM")
	}
	_ = httptest.NewRequest(http.MethodGet, "https://127.0.0.1/", nil)
}
