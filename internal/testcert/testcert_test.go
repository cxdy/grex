package testcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// fatalTB records Fatal without failing the parent test, then exits the
// goroutine like testing.T so gen() does not continue past the failure.
type fatalTB struct {
	testing.TB
	msg string
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatal(args ...any) {
	f.msg = fmt.Sprint(args...)
	runtime.Goexit()
}

func (f *fatalTB) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// runGen runs gen in a child goroutine so Fatal/Goexit does not abort the test.
// A deferred send still runs after Goexit, delivering the fatal message.
func runGen(t *testing.T, deps genDeps) (msg string, succeeded bool) {
	t.Helper()
	done := make(chan string, 1)
	go func() {
		ft := &fatalTB{TB: t}
		defer func() { done <- ft.msg }()
		_ = gen(ft, deps)
	}()
	select {
	case msg = <-done:
		return msg, msg == ""
	case <-time.After(5 * time.Second):
		t.Fatal("gen timed out")
		return "", false
	}
}

func expectFatal(t *testing.T, deps genDeps) {
	t.Helper()
	msg, ok := runGen(t, deps)
	if ok {
		t.Fatal("expected gen to Fatal, but it succeeded")
	}
	if msg == "" {
		t.Fatal("expected non-empty fatal message")
	}
}

func realGenerateKey() (*ecdsa.PrivateKey, error) {
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

func TestGenProducesUsableTLSMaterial(t *testing.T) {
	certs := Gen(t)

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

	serverTLS, err := tls.LoadX509KeyPair(certs.ServerCertFile, certs.ServerKeyFile)
	if err != nil {
		t.Fatalf("server keypair: %v", err)
	}

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

	caPEM, err := os.ReadFile(certs.CAFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("could not parse CA PEM")
	}
}

func TestGenFailurePaths(t *testing.T) {
	boom := errors.New("injected failure")

	t.Run("ca key generation", func(t *testing.T) {
		expectFatal(t, genDeps{
			generateKey: func() (*ecdsa.PrivateKey, error) {
				return nil, boom
			},
		})
	})

	t.Run("create CA certificate", func(t *testing.T) {
		expectFatal(t, genDeps{
			createCertificate: func(io.Reader, *x509.Certificate, *x509.Certificate, any, any) ([]byte, error) {
				return nil, boom
			},
		})
	})

	t.Run("parse CA certificate", func(t *testing.T) {
		expectFatal(t, genDeps{
			parseCertificate: func([]byte) (*x509.Certificate, error) {
				return nil, boom
			},
		})
	})

	t.Run("issue leaf key generation", func(t *testing.T) {
		var n atomic.Int32
		expectFatal(t, genDeps{
			generateKey: func() (*ecdsa.PrivateKey, error) {
				if n.Add(1) == 1 {
					return realGenerateKey()
				}
				return nil, boom
			},
		})
	})

	t.Run("create leaf certificate", func(t *testing.T) {
		var calls atomic.Int32
		expectFatal(t, genDeps{
			createCertificate: func(r io.Reader, template, parent *x509.Certificate, pub, priv any) ([]byte, error) {
				if calls.Add(1) == 1 {
					return x509.CreateCertificate(r, template, parent, pub, priv)
				}
				return nil, boom
			},
		})
	})

	t.Run("marshal leaf private key", func(t *testing.T) {
		expectFatal(t, genDeps{
			marshalECPrivateKey: func(*ecdsa.PrivateKey) ([]byte, error) {
				return nil, boom
			},
		})
	})

	t.Run("write file", func(t *testing.T) {
		expectFatal(t, genDeps{
			writeFile: func(string, []byte, os.FileMode) error {
				return boom
			},
		})
	})

	t.Run("client key pair", func(t *testing.T) {
		expectFatal(t, genDeps{
			x509KeyPair: func([]byte, []byte) (tls.Certificate, error) {
				return tls.Certificate{}, boom
			},
		})
	})
}
