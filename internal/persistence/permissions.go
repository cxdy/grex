package persistence

import (
	"context"
	"fmt"
)

var _ PermissionStore = (*PostgresStore)(nil)

// CreateRoleMapping inserts a role_mapping row. TenantID is stored as-is
// (empty string means "no tenant," per the design doc's nullable column —
// matching-by-identity logic is a separate, not-yet-built concern).
func (s *PostgresStore) CreateRoleMapping(ctx context.Context, m RoleMapping) (RoleMapping, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO role_mapping (identity_kind, identity_value, match, role, tenant_id)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		RETURNING id, identity_kind, identity_value, match, role, COALESCE(tenant_id, ''), created_at, updated_at`,
		m.IdentityKind, m.IdentityValue, m.Match, m.Role, m.TenantID)
	created, err := scanRoleMapping(row)
	if err != nil {
		return RoleMapping{}, fmt.Errorf("insert role_mapping: %w", err)
	}
	return created, nil
}

// GetRoleMapping reads one role_mapping row by id.
func (s *PostgresStore) GetRoleMapping(ctx context.Context, id int64) (RoleMapping, bool, error) {
	mappings, err := s.queryRoleMappings(ctx, `WHERE id = $1`, id)
	if err != nil {
		return RoleMapping{}, false, err
	}
	if len(mappings) == 0 {
		return RoleMapping{}, false, nil
	}
	return mappings[0], true, nil
}

// ListRoleMappings reads every role_mapping row.
func (s *PostgresStore) ListRoleMappings(ctx context.Context) ([]RoleMapping, error) {
	return s.queryRoleMappings(ctx, "")
}

func (s *PostgresStore) queryRoleMappings(ctx context.Context, where string, args ...any) ([]RoleMapping, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, identity_kind, identity_value, match, role, COALESCE(tenant_id, ''), created_at, updated_at
		FROM role_mapping `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query role_mapping: %w", err)
	}
	defer rows.Close()

	var mappings []RoleMapping
	for rows.Next() {
		m, err := scanRoleMapping(rows)
		if err != nil {
			return nil, fmt.Errorf("scan role_mapping: %w", err)
		}
		mappings = append(mappings, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate role_mapping: %w", err)
	}
	return mappings, nil
}

// rowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows (Query),
// so scanRoleMapping works for both a single-row lookup and a list scan.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRoleMapping(row rowScanner) (RoleMapping, error) {
	var m RoleMapping
	if err := row.Scan(&m.ID, &m.IdentityKind, &m.IdentityValue, &m.Match, &m.Role, &m.TenantID, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return RoleMapping{}, err
	}
	return m, nil
}
