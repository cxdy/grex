#!/bin/sh
# Mints the dev certificates for the compose stack: a local CA, a grex server
# certificate, and one client certificate per collector. Idempotent: exits
# early when the CA already exists. Keys are made world-readable because the
# containers run as different non-root users; these certificates are for
# local development only.
set -eu

CERT_DIR="${CERT_DIR:-/certs}"
DAYS=365

if [ -f "$CERT_DIR/ca.pem" ]; then
    echo "certificates already present in $CERT_DIR, nothing to do"
    exit 0
fi

mkdir -p "$CERT_DIR"
cd "$CERT_DIR"

openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
    -keyout ca-key.pem -out ca.pem -days "$DAYS" \
    -subj "/CN=grex dev CA"

issue() {
    name="$1"
    ext="$2"
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
issue agent-1 "extendedKeyUsage=clientAuth"
issue agent-2 "extendedKeyUsage=clientAuth"
issue gateway "extendedKeyUsage=clientAuth"

chmod 644 ./*.pem
echo "certificates written to $CERT_DIR"
