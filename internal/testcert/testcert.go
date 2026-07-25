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
	"io"
	"math/big"
	"net"
	"net/url"
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

	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
}

// genDeps are overridable hooks so tests can exercise failure paths.
// Zero value uses production crypto and filesystem behavior.
type genDeps struct {
	// generateKey mints a P-256 private key. Defaults to ecdsa.GenerateKey.
	// (crypto/ecdsa ignores a custom rand.Reader since Go 1.26, so keygen is
	// injected as a whole rather than via io.Reader.)
	generateKey func() (*ecdsa.PrivateKey, error)
	// writeFile writes PEM material. Defaults to os.WriteFile.
	writeFile func(name string, data []byte, perm os.FileMode) error
	// createCertificate defaults to x509.CreateCertificate.
	createCertificate func(rand io.Reader, template, parent *x509.Certificate, pub, priv any) ([]byte, error)
	// parseCertificate defaults to x509.ParseCertificate.
	parseCertificate func(der []byte) (*x509.Certificate, error)
	// marshalECPrivateKey defaults to x509.MarshalECPrivateKey.
	marshalECPrivateKey func(key *ecdsa.PrivateKey) ([]byte, error)
	// x509KeyPair defaults to tls.X509KeyPair.
	x509KeyPair func(certPEMBlock, keyPEMBlock []byte) (tls.Certificate, error)
}

func (d genDeps) withDefaults() genDeps {
	if d.generateKey == nil {
		d.generateKey = func() (*ecdsa.PrivateKey, error) {
			return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		}
	}
	if d.writeFile == nil {
		d.writeFile = os.WriteFile
	}
	if d.createCertificate == nil {
		d.createCertificate = x509.CreateCertificate
	}
	if d.parseCertificate == nil {
		d.parseCertificate = x509.ParseCertificate
	}
	if d.marshalECPrivateKey == nil {
		d.marshalECPrivateKey = x509.MarshalECPrivateKey
	}
	if d.x509KeyPair == nil {
		d.x509KeyPair = tls.X509KeyPair
	}
	return d
}

// Gen writes a fresh CA, server, and client certificate into a temp dir owned
// by the test.
func Gen(tb testing.TB) Certs {
	tb.Helper()
	return gen(tb, genDeps{})
}

func gen(tb testing.TB, deps genDeps) Certs {
	tb.Helper()
	deps = deps.withDefaults()
	dir := tb.TempDir()

	caKey, err := deps.generateKey()
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
	caDER, err := deps.createCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		tb.Fatal(err)
	}
	caCert, err := deps.parseCertificate(caDER)
	if err != nil {
		tb.Fatal(err)
	}

	issue := func(name string, extUsage x509.ExtKeyUsage, ips []net.IP, uris []*url.URL) (certPEM, keyPEM []byte) {
		return issueCert(tb, deps, caCert, caKey, name, extUsage, ips, uris)
	}

	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := deps.writeFile(path, data, 0o600); err != nil {
			tb.Fatal(err)
		}
		return path
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	serverCertPEM, serverKeyPEM := issue("grex", x509.ExtKeyUsageServerAuth, []net.IP{net.ParseIP("127.0.0.1")}, nil)
	clientCertPEM, clientKeyPEM := issue("otelcol", x509.ExtKeyUsageClientAuth, nil, nil)

	clientTLSCert, err := deps.x509KeyPair(clientCertPEM, clientKeyPEM)
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
		caCert:         caCert,
		caKey:          caKey,
	}
}

// issueCert mints a certificate signed by caCert/caKey for name, with the
// given extended key usage, IP SANs, and URI SANs.
func issueCert(tb testing.TB, deps genDeps, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, name string, extUsage x509.ExtKeyUsage, ips []net.IP, uris []*url.URL) (certPEM, keyPEM []byte) {
	tb.Helper()
	key, err := deps.generateKey()
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
		URIs:         uris,
	}
	der, err := deps.createCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		tb.Fatal(err)
	}
	keyDER, err := deps.marshalECPrivateKey(key)
	if err != nil {
		tb.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// IssueClient mints an additional client certificate signed by the same CA
// as Gen, carrying the given URI SANs (e.g. a spiffe:// identity).
func (c Certs) IssueClient(tb testing.TB, name string, uris []*url.URL) tls.Certificate {
	tb.Helper()
	deps := genDeps{}.withDefaults()
	certPEM, keyPEM := issueCert(tb, deps, c.caCert, c.caKey, name, x509.ExtKeyUsageClientAuth, nil, uris)
	cert, err := deps.x509KeyPair(certPEM, keyPEM)
	if err != nil {
		tb.Fatal(err)
	}
	return cert
}
