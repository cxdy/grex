// Package testcert mints throwaway TLS material for tests: a CA, a server
// certificate for 127.0.0.1, and a client certificate signed by the same CA.
package testcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Certs holds the generated certificate files and in-memory forms.
type Certs struct {
	CAFile         string
	CAPool         *x509.CertPool
	ServerCertFile string
	ServerKeyFile  string
	ClientTLSCert  tls.Certificate
	ClientCertFile string
}

// Gen writes a fresh CA, server, and client certificate into a temp dir owned
// by the test.
func Gen(tb testing.TB) Certs {
	tb.Helper()
	dir := tb.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
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
		tb.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		tb.Fatal(err)
	}

	issue := func(name string, extUsage x509.ExtKeyUsage, ips []net.IP) (certPEM, keyPEM []byte) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			tb.Fatal(err)
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
			tb.Fatal(err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			tb.Fatal(err)
		}
		certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		return certPEM, keyPEM
	}

	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			tb.Fatal(err)
		}
		return path
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	serverCertPEM, serverKeyPEM := issue("grex", x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")})
	clientCertPEM, clientKeyPEM := issue("otelcol", x509.ExtKeyUsageClientAuth, nil)

	clientTLSCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		tb.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	return Certs{
		CAFile:         write("ca.pem", caPEM),
		CAPool:         pool,
		ServerCertFile: write("server.pem", serverCertPEM),
		ServerKeyFile:  write("server-key.pem", serverKeyPEM),
		ClientTLSCert:  clientTLSCert,
		ClientCertFile: write("client.pem", clientCertPEM),
	}
}
