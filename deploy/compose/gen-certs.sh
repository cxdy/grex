#!/bin/sh
# Mints the dev certificates for the compose stack: a local CA, server
# certificates for grex and the OpAMP gateway, and client certificates for
# every OpAMP client. Idempotent per file: existing certificates are kept,
# missing ones are issued from the existing CA. Keys are made world-readable
# because the containers run as different non-root users; these certificates
# are for local development only.
set -eu

CERT_DIR="${CERT_DIR:-/certs}"
DAYS=365

mkdir -p "$CERT_DIR"
cd "$CERT_DIR"

if [ ! -f ca.pem ]; then
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
        -keyout ca-key.pem -out ca.pem -days "$DAYS" \
        -subj "/CN=grex dev CA"
fi

issue() {
    name="$1"
    ext="$2"
    [ -f "$name.pem" ] && return 0
    openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
        -keyout "$name-key.pem" -out "$name.csr" -subj "/CN=$name"
    openssl x509 -req -in "$name.csr" -CA ca.pem -CAkey ca-key.pem \
        -CAcreateserial -days "$DAYS" -out "$name.pem" -extfile /dev/stdin <<EOF
$ext
EOF
    rm "$name.csr"
}

issue server "subjectAltName=DNS:grex,DNS:localhost
extendedKeyUsage=serverAuth"
issue opamp-gateway "subjectAltName=DNS:opamp-gateway,DNS:localhost
extendedKeyUsage=serverAuth"
issue opamp-gateway-client "extendedKeyUsage=clientAuth"
issue agent-1 "extendedKeyUsage=clientAuth"
issue agent-2 "extendedKeyUsage=clientAuth"
issue gateway "extendedKeyUsage=clientAuth"

# UI/telemetry listener clients, identified by SPIFFE URI SAN rather than
# CommonName. Trust domain grex-api.internal is kept separate from any future
# OpAMP-gateway identity trust domain.
issue user-alice "subjectAltName=URI:spiffe://grex-api.internal/user/alice
extendedKeyUsage=clientAuth"
issue user-admin "subjectAltName=URI:spiffe://grex-api.internal/user/admin
extendedKeyUsage=clientAuth"
issue user-mallory "subjectAltName=URI:spiffe://grex-api.internal/user/mallory
extendedKeyUsage=clientAuth"
issue service-prometheus "subjectAltName=URI:spiffe://grex-api.internal/service/prometheus
extendedKeyUsage=clientAuth"

chmod 644 ./*.pem
echo "certificates present in $CERT_DIR"
