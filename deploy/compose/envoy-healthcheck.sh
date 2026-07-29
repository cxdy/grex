#!/usr/bin/env bash
# Envoy's admin port answering only means the process is up, not that its
# cluster has actually discovered and health-checked both grex backends.
# Gating opamp-gateway's startup on the bare TCP port alone races its first
# upstream connections against Envoy still catching up to grex-2, and both
# connections land on whichever single backend Envoy already knew about —
# not a load-balancing bug, a startup-ordering one. Wait for both.
set -euo pipefail
exec 3<>/dev/tcp/127.0.0.1/9901
printf 'GET /clusters HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n' >&3
healthy=$(timeout 2 cat <&3 | grep -c 'grex_opamp.*health_flags::healthy')
[ "$healthy" = 2 ]
