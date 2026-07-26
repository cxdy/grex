# Authentication

## Current status

| Surface | Auth today |
|---------|------------|
| OpAMP | Optional **mTLS** (`opamp_tls.client_ca_file`) for collectors and gateways |
| UI + JSON API | Optional **mTLS with SPIFFE IDs** (`ui_tls.client_ca_file`) |
| Telemetry | Optional **mTLS with SPIFFE IDs** (`telemetry_tls.client_ca_file`); `/healthz` and `/readyz` always exempt |

!!! warning "OIDC planned, not shipped"
    OIDC login via [Dex](https://dexidp.io/) (milestone 7 in the
    [design SPEC](../spec/design.md)) is tracked in
    **[issue #11](https://github.com/dennisme/grex/issues/11)**. Compose
    already runs Dex with static users so that work can proceed offline;
    grex does not consume Dex yet.

## mTLS with SPIFFE IDs

When `ui_tls.client_ca_file` or `telemetry_tls.client_ca_file` is set, grex
takes the caller's identity from their client certificate's **SPIFFE ID**: a
single URI Subject Alternative Name of the form
`spiffe://<trust-domain>/<path>`. Certs carrying zero URI SANs, more than
one, or a URI SAN that isn't a well-formed `spiffe://` URI are rejected.

### Path format

| Namespace | Format | Used for |
|-----------|--------|----------|
| Humans | `spiffe://<trust-domain>/user/<name>` | Operators, on-call, scripts run by a person |
| Services | `spiffe://<trust-domain>/service/<name>` | Automation: Prometheus, CI |

Not `/agent/...` for either: "agent" already means an OpAMP-managed
collector everywhere else in grex, and reusing it here would be confusing.
The trust domain used for UI/telemetry identities (e.g.
`spiffe://grex-api.internal/...`) is kept separate from any trust domain
used for OpAMP-gateway agent identity, so the two authorization surfaces
can be rotated and scoped independently.

### Role mapping

`auth.role_mapping` maps a SPIFFE ID, or a prefix of one, to a role:

```yaml
auth:
  role_mapping:
    - match: exact
      spiffe_id: spiffe://grex-api.internal/user/alice
      role: viewer
    - match: prefix
      spiffe_id: spiffe://grex-api.internal/service/
      role: viewer
  default_role: none
```

Exact matches win over prefix matches regardless of position in the list;
among rules of the same specificity, the first one wins. `default_role`
applies to an authenticated caller matching no rule; `none` denies access
(`403`).

Full request-handling order is in [TLS and mTLS](tls-mtls.md#ui-and-telemetry-listeners).

### Roles

| Role | 1.0 behavior |
|------|--------------|
| `viewer` | Passes the auth gate; the read-only UI/API has no further role-based restriction in 1.0 |
| `admin` | Same as viewer in 1.0; reserved for later gates once mutating endpoints exist |
| `none` | Denied (`403`) |

## Collectors (OpAMP)

TLS termination and optional client cert verification on the OpAMP
listener, independent of the SPIFFE/role mechanism above: grex records the
peer certificate subject, but does not (yet) resolve a SPIFFE ID or role for
OpAMP connections. See [TLS and mTLS](tls-mtls.md).

## Operator guidance

- Until OIDC ships, mTLS is the only way to require identity on the UI and
  telemetry listeners; an unauthenticated caller can still reach them if
  `ui_tls.client_ca_file` / `telemetry_tls.client_ca_file` are left unset
- Bind UI and telemetry to a private network interface if you are not
  ready to configure mTLS
- Prefer OpAMP mTLS for any shared or multi-tenant network path to
  collectors
- Do not rely on "security through obscurity" of the URL
