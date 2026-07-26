package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dennisme/grex/internal/fleet"
)

// PostgresStore is a StateStore backed by Postgres. Every write is a guarded
// UPSERT keyed on event time (Agent.LastSeen), never flush wall-clock time,
// so concurrent flushes from multiple grex replicas for the same
// instance_uid converge correctly regardless of which one reaches Postgres
// first: a stale write is a no-op against a row that's already newer.
type PostgresStore struct {
	pool *pgxpool.Pool
}

var _ StateStore = (*PostgresStore)(nil)

// NewPostgresStore wraps an existing pgx pool. The caller owns the pool's
// lifecycle (including Close).
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// SaveAgent writes agent across the agents, agent_session, and
// agent_effective_config tables in one transaction. agent_effective_config
// is replaced wholesale (delete, then insert the current set) rather than
// guarded per row: unlike agents/agent_session it isn't part of the
// multi-writer ordering guarantee, a simplification accepted for now.
func (s *PostgresStore) SaveAgent(ctx context.Context, agent fleet.Agent) error {
	identifying, err := json.Marshal(nonNilStringMap(agent.Identifying))
	if err != nil {
		return fmt.Errorf("marshal identifying attributes: %w", err)
	}
	nonIdentifying, err := json.Marshal(nonNilStringMap(agent.NonIdentifying))
	if err != nil {
		return fmt.Errorf("marshal non_identifying attributes: %w", err)
	}
	packages, err := json.Marshal(nonNilPackageMap(agent.Packages))
	if err != nil {
		return fmt.Errorf("marshal packages: %w", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	_, err = tx.Exec(ctx, `
		INSERT INTO agents (
			instance_uid, first_seen, last_seen, healthy, health_error,
			health_status, health_start_time, health_status_time, health_reported,
			capabilities, identifying, non_identifying, missing_attributes,
			reserved_attribute_conflicts, packages, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (instance_uid) DO UPDATE SET
			last_seen = EXCLUDED.last_seen,
			healthy = EXCLUDED.healthy,
			health_error = EXCLUDED.health_error,
			health_status = EXCLUDED.health_status,
			health_start_time = EXCLUDED.health_start_time,
			health_status_time = EXCLUDED.health_status_time,
			health_reported = EXCLUDED.health_reported,
			capabilities = EXCLUDED.capabilities,
			identifying = EXCLUDED.identifying,
			non_identifying = EXCLUDED.non_identifying,
			missing_attributes = EXCLUDED.missing_attributes,
			reserved_attribute_conflicts = EXCLUDED.reserved_attribute_conflicts,
			packages = EXCLUDED.packages,
			updated_at = EXCLUDED.updated_at
		WHERE agents.updated_at < EXCLUDED.updated_at`,
		agent.InstanceUID, agent.FirstSeen, agent.LastSeen, agent.Healthy, agent.HealthError,
		agent.HealthStatus, nullTime(agent.HealthStartTime), nullTime(agent.HealthStatusTime), agent.HealthReported,
		int64(agent.Capabilities), //nolint:gosec // bit-for-bit storage, no arithmetic performed on this value
		identifying, nonIdentifying, agent.MissingAttributes,
		agent.ReservedAttributeConflicts, packages, agent.LastSeen)
	if err != nil {
		return fmt.Errorf("upsert agents: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO agent_session (
			instance_uid, connected, remote_addr, tls_subject, via_gateway,
			transport, description_reported, sequence_num, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (instance_uid) DO UPDATE SET
			connected = EXCLUDED.connected,
			remote_addr = EXCLUDED.remote_addr,
			tls_subject = EXCLUDED.tls_subject,
			via_gateway = EXCLUDED.via_gateway,
			transport = EXCLUDED.transport,
			description_reported = EXCLUDED.description_reported,
			sequence_num = EXCLUDED.sequence_num,
			updated_at = EXCLUDED.updated_at
		WHERE agent_session.updated_at < EXCLUDED.updated_at`,
		agent.InstanceUID, agent.Connected, agent.Conn.RemoteAddr, agent.Conn.TLSSubject, agent.Conn.ViaGateway,
		agent.Conn.Transport, agent.DescriptionReported, int64(agent.SequenceNum), //nolint:gosec // bit-for-bit storage
		agent.LastSeen)
	if err != nil {
		return fmt.Errorf("upsert agent_session: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM agent_effective_config WHERE instance_uid = $1`, agent.InstanceUID); err != nil {
		return fmt.Errorf("clear agent_effective_config: %w", err)
	}
	for filename, body := range agent.EffectiveConfig {
		_, err = tx.Exec(ctx, `
			INSERT INTO agent_effective_config (instance_uid, filename, body, updated_at)
			VALUES ($1, $2, $3, $4)`,
			agent.InstanceUID, filename, body, agent.LastSeen)
		if err != nil {
			return fmt.Errorf("insert agent_effective_config: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// GetAgent reads one agent back, joining across all three tables. Not
// wired into grex's runtime anywhere yet (no reset-on-load hydration, no
// API/UI reads from the database) — this exists so SaveAgent is testable.
func (s *PostgresStore) GetAgent(ctx context.Context, instanceUID string) (fleet.Agent, bool, error) {
	agents, err := s.queryAgents(ctx, `WHERE a.instance_uid = $1`, instanceUID)
	if err != nil {
		return fleet.Agent{}, false, err
	}
	if len(agents) == 0 {
		return fleet.Agent{}, false, nil
	}
	return agents[0], true, nil
}

// ListAgents reads every agent back. Same "not wired in yet" caveat as
// GetAgent.
func (s *PostgresStore) ListAgents(ctx context.Context) ([]fleet.Agent, error) {
	return s.queryAgents(ctx, "")
}

func (s *PostgresStore) queryAgents(ctx context.Context, where string, args ...any) ([]fleet.Agent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			a.instance_uid, a.first_seen, a.last_seen, a.healthy, a.health_error,
			a.health_status, a.health_start_time, a.health_status_time, a.health_reported,
			a.capabilities, a.identifying, a.non_identifying, a.missing_attributes,
			a.reserved_attribute_conflicts, a.packages,
			s.connected, s.remote_addr, s.tls_subject, s.via_gateway, s.transport,
			s.description_reported, s.sequence_num
		FROM agents a
		LEFT JOIN agent_session s ON s.instance_uid = a.instance_uid
		`+where+`
		ORDER BY a.instance_uid`, args...)
	if err != nil {
		return nil, fmt.Errorf("query agents: %w", err)
	}
	defer rows.Close()

	var out []fleet.Agent
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		out = append(out, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agents: %w", err)
	}

	for i, agent := range out {
		cfg, err := s.effectiveConfig(ctx, agent.InstanceUID)
		if err != nil {
			return nil, err
		}
		out[i].EffectiveConfig = cfg
	}
	return out, nil
}

func scanAgent(rows pgx.Rows) (fleet.Agent, error) {
	var (
		a                                     fleet.Agent
		healthStartTime, healthStatusTime     *time.Time
		capabilities, sequenceNum             int64
		identifying, nonIdentifying, packages []byte
		connected, viaGateway                 *bool
		remoteAddr, tlsSubject, transport     *string
	)
	err := rows.Scan(
		&a.InstanceUID, &a.FirstSeen, &a.LastSeen, &a.Healthy, &a.HealthError,
		&a.HealthStatus, &healthStartTime, &healthStatusTime, &a.HealthReported,
		&capabilities, &identifying, &nonIdentifying, &a.MissingAttributes,
		&a.ReservedAttributeConflicts, &packages,
		&connected, &remoteAddr, &tlsSubject, &viaGateway, &transport,
		&a.DescriptionReported, &sequenceNum)
	if err != nil {
		return fleet.Agent{}, err
	}

	a.Capabilities = uint64(capabilities) //nolint:gosec // bit-for-bit storage, no arithmetic performed on this value
	a.SequenceNum = uint64(sequenceNum)   //nolint:gosec // bit-for-bit storage, no arithmetic performed on this value
	if healthStartTime != nil {
		a.HealthStartTime = *healthStartTime
	}
	if healthStatusTime != nil {
		a.HealthStatusTime = *healthStatusTime
	}
	if connected != nil {
		a.Connected = *connected
	}
	if viaGateway != nil {
		a.Conn.ViaGateway = *viaGateway
	}
	if remoteAddr != nil {
		a.Conn.RemoteAddr = *remoteAddr
	}
	if tlsSubject != nil {
		a.Conn.TLSSubject = *tlsSubject
	}
	if transport != nil {
		a.Conn.Transport = *transport
	}

	if err := json.Unmarshal(identifying, &a.Identifying); err != nil {
		return fleet.Agent{}, fmt.Errorf("unmarshal identifying attributes: %w", err)
	}
	if err := json.Unmarshal(nonIdentifying, &a.NonIdentifying); err != nil {
		return fleet.Agent{}, fmt.Errorf("unmarshal non_identifying attributes: %w", err)
	}
	if err := json.Unmarshal(packages, &a.Packages); err != nil {
		return fleet.Agent{}, fmt.Errorf("unmarshal packages: %w", err)
	}
	if len(a.Identifying) == 0 {
		a.Identifying = nil
	}
	if len(a.NonIdentifying) == 0 {
		a.NonIdentifying = nil
	}
	if len(a.Packages) == 0 {
		a.Packages = nil
	}
	return a, nil
}

// nullTime converts a possibly-zero time.Time into a value pgx will bind as
// SQL NULL when zero. Agent.HealthStartTime/HealthStatusTime are zero when
// the agent hasn't set the corresponding field yet.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func (s *PostgresStore) effectiveConfig(ctx context.Context, instanceUID string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT filename, body FROM agent_effective_config WHERE instance_uid = $1`, instanceUID)
	if err != nil {
		return nil, fmt.Errorf("query agent_effective_config: %w", err)
	}
	defer rows.Close()

	cfg := make(map[string]string)
	for rows.Next() {
		var filename, body string
		if err := rows.Scan(&filename, &body); err != nil {
			return nil, fmt.Errorf("scan agent_effective_config: %w", err)
		}
		cfg[filename] = body
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent_effective_config: %w", err)
	}
	if len(cfg) == 0 {
		return nil, nil
	}
	return cfg, nil
}

// DeleteAgent removes an agent outright. agent_session and
// agent_effective_config cascade via their foreign keys.
func (s *PostgresStore) DeleteAgent(ctx context.Context, instanceUID string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM agents WHERE instance_uid = $1`, instanceUID); err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	return nil
}

func nonNilStringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func nonNilPackageMap(m map[string]fleet.Package) map[string]fleet.Package {
	if m == nil {
		return map[string]fleet.Package{}
	}
	return m
}
