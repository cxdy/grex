# Authentication

## Current status

| Surface | Auth today |
|---------|------------|
| OpAMP | Optional **mTLS** (`tls.client_ca_file`) for collectors |
| UI + JSON API | **None** — open to anyone who can open the port |
| Telemetry | **None** — metrics and health probes are unauthenticated |

!!! warning "Planned, not shipped"
    UI/API authentication (mTLS with SPIFFE IDs, then OIDC via Dex) is
    tracked in **[issue #11](https://github.com/dennisme/grex/issues/11)**.
    Until it lands, treat the UI listener as an internal-only service.

Compose already runs **Dex** with static users so the future OIDC path can
be developed offline. grex does not consume Dex yet.

## Planned model (from design)

This summarizes the living [design SPEC](../spec/design.md). Details may
change before implementation.

### Collectors (OpAMP)

Already partially available: TLS termination and optional client cert
verification on the OpAMP listener.

### Users (UI / API) — milestone plan

1. **mTLS first** — required client certificates on the UI listener; identity
   from a single SPIFFE URI SAN (`spiffe://…`); role map by SPIFFE ID/prefix.
2. **OIDC second** — Authorization Code flow against [Dex](https://dexidp.io/);
   first connector GitHub org/teams as `groups` claims; same role table shape.

### Roles (planned)

| Role | 1.0 intent |
|------|------------|
| `viewer` | See everything the UI shows |
| `admin` | Same as viewer until mutations exist; reserved for later gates |

Default and explicit maps will be static configuration (no role admin UI).

## Operator guidance until auth ships

- Bind UI and telemetry to localhost or a private network interface
- Put a trusted reverse proxy or mesh policy in front if broader access is
  required temporarily
- Prefer OpAMP mTLS for any shared or multi-tenant network path to collectors
- Do not rely on “security through obscurity” of the URL

When issue #11 closes, this page should be updated to match the shipped
config fields and login flow.
