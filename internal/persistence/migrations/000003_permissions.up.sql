-- Flat identity-to-role table, per docs/spec/design.md's Permission table
-- schema. Role set stays exactly viewer/admin; both still read-only until
-- Jobs (below) is the first place they actually differ in behavior.
CREATE TABLE role_mapping (
    id BIGSERIAL PRIMARY KEY,
    -- 'spiffe' | 'oidc_group'. Generalized name (not spiffe_id) so the OIDC
    -- identity source slots in later with no rename.
    identity_kind TEXT NOT NULL,
    -- The SPIFFE ID string, or the OIDC groups claim value.
    identity_value TEXT NOT NULL,
    -- 'exact' | 'prefix'. Mirrors today's spiffe.RoleRule.Match.
    match TEXT NOT NULL,
    -- 'viewer' | 'admin'.
    role TEXT NOT NULL,
    -- Nullable, unused until multi-tenancy exists.
    tenant_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- "What role does this caller have" is the hot lookup path; index the
-- columns it filters on.
CREATE INDEX idx_role_mapping_identity ON role_mapping (identity_kind, identity_value);
