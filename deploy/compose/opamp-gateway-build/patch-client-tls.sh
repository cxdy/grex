#!/bin/sh
# Applies the grex fix for opampgateway v1.10.0: Settings.TLS is declared but
# never wired into the upstream websocket dialer, so wss:// upstream
# connections ignore ca_file/cert_file/key_file entirely. This patches
# newClient to build the dialer from the configured TLS client settings.
set -eu

client="$1/internal/gateway/client.go"

sed -i 's/\*websocket\.DefaultDialer,/newUpstreamDialer(settings, logger),/' "$client"
grep -q 'newUpstreamDialer(settings, logger)' "$client"

cat >> "$client" <<'EOF'

// newUpstreamDialer builds the upstream websocket dialer honoring the
// configured TLS client settings.
func newUpstreamDialer(settings Settings, logger *zap.Logger) websocket.Dialer {
	dialer := *websocket.DefaultDialer
	tlsCfg, err := settings.TLS.LoadTLSConfig(context.Background())
	if err != nil {
		logger.Error("load upstream TLS config", zap.Error(err))
		return dialer
	}
	if tlsCfg != nil {
		dialer.TLSClientConfig = tlsCfg
	}
	return dialer
}
EOF

echo "patched $client"
