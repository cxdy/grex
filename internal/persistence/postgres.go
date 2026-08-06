package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dennisme/grex/internal/fleet"
)

// execer is satisfied by both *pgxpool.Pool and pgx.Tx, so
// saveSessionUpsert can run either inside SaveAgent's transaction or as
// SaveSession's own standalone statement.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// saveSessionUpsert is agent_session's write, factored out so it can run
// independently of the agents table's own guard (see SaveAgent) as well as
// standalone (see SaveSession). Guarded on agent_session's own updated_at,
// not the agents table's — the two tables are allowed to disagree about
// which write is "newest" since they're written by different paths on
// different cadences (SaveAgent's dirty-triggered writes vs
// SessionSnapshotter's keepalive ones).
//
// The guard allows ties (<=), not just strict advances (<): Registry.Sweep
// marks a missed-heartbeat agent disconnected without ever advancing
// LastSeen (see Sweep's own doc comment and SaveAgent's), so a disconnect
// write's own event time is, by construction, exactly equal to whatever
// LastSeen already produced the currently-stored row — not older, but not
// strictly newer either. Rejecting ties would silently drop every
// disconnect. This stays safe against the actual risk (a genuinely older
// write, from a stale flush, clobbering newer data written elsewhere): <=
// still rejects anything strictly older, same as the agents table's guard;
// it only additionally accepts the disconnect-at-an-unmoved-LastSeen case.
func saveSessionUpsert(ctx context.Context, q execer, agent fleet.Agent) error {
	sql, args := saveSessionSQL(agent)
	_, err := q.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("upsert agent_session: %w", err)
	}
	return nil
}

// saveSessionSQL builds saveSessionUpsert's statement and args, shared with
// QueueSaveSession so the two never drift apart: one runs it immediately,
// the other queues the identical statement onto a pgx.Batch for a chunked
// round trip (see docs/spec/design.md's Scaling gaps items 3-4).
func saveSessionSQL(agent fleet.Agent) (string, []any) {
	return `
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
		WHERE agent_session.updated_at <= EXCLUDED.updated_at`,
		[]any{
			agent.InstanceUID, agent.Connected, agent.Conn.RemoteAddr, agent.Conn.TLSSubject, agent.Conn.ViaGateway,
			agent.Conn.Transport, agent.DescriptionReported, int64(agent.SequenceNum), //nolint:gosec // bit-for-bit storage
			agent.LastSeen,
		}
}

// QueueSaveSession appends a SaveSession-equivalent statement to batch
// instead of executing it immediately. The caller sends batch (SendBatch)
// and must read exactly one result per queued statement, in order.
func (s *PostgresStore) QueueSaveSession(batch *pgx.Batch, agent fleet.Agent) {
	sql, args := saveSessionSQL(agent)
	batch.Queue(sql, args...)
}

// SendBatch sends batch in one round trip. Implements BatchStateStore.
func (s *PostgresStore) SendBatch(ctx context.Context, batch *pgx.Batch) pgx.BatchResults {
	return s.pool.SendBatch(ctx, batch)
}

// PostgresStore is a StateStore backed by Postgres. Every write is a guarded
// UPSERT keyed on event time (Agent.LastSeen), never flush wall-clock time,
// so concurrent flushes from multiple grex replicas for the same
// instance_uid converge correctly regardless of which one reaches Postgres
// first: a stale write is a no-op against a row that's already newer.
type PostgresStore struct {
	pool *pgxpool.Pool
}

var _ StateStore = (*PostgresStore)(nil)
var _ BatchStateStore = (*PostgresStore)(nil)

// NewPostgresStore wraps an existing pgx pool. The caller owns the pool's
// lifecycle (including Close).
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// SaveAgent writes agent across the agents, agent_session, and
// agent_effective_config tables in one transaction. agent_effective_config
// is gated on the agents-table write actually landing (see below); it has
// no per-row timestamp of its own, unlike the other two, so it needs the
// whole call gated up front rather than guarded row by row.
//
// agent_session is NOT gated behind the agents-table write: it has its own
// guard (saveSessionUpsert, keyed on agent_session's own updated_at) and is
// always attempted regardless of whether the agents-table write landed.
// This matters for a real, always-reachable case: Registry.Sweep marks a
// missed-heartbeat agent disconnected without ever advancing LastSeen (see
// Sweep's own doc comment), so the dirty-triggered flush that follows
// carries the exact same LastSeen already stored in agents — the
// agents-table guard correctly rejects that as "not newer," but
// agent_session must still record Connected=false. Gating it behind the
// agents-table's result would silently drop every disconnect that isn't
// paired with a genuine identity/health change, exactly the stale
// Connected=true bug this schema exists to avoid.
//
// A stale flush landing after a newer one is expected for the agents/
// agent_effective_config pair: an agent can reconnect to a different grex
// replica on every connection, and nothing guarantees the replica holding
// the older data flushes first. Without agent_effective_config's gate, that
// replica's delete+insert could still commit after the newer replica's,
// overwriting current files with stale ones — not just temporary staleness
// (which self-heals on the next flush) but an actual inversion that would
// persist until the stale replica's next flush.
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

	// first_seen is intentionally absent from the UPDATE SET list below: it
	// must never move forward once a row exists. A replica that has never
	// seen this agent before creates its own local registry entry with
	// FirstSeen set to its own connect time, which is wrong for any agent
	// that connected somewhere else first — omitting it here means that
	// wrong value is simply discarded on conflict, and the original row's
	// first_seen (set once, on the actual first INSERT) survives untouched.
	tag, err := tx.Exec(ctx, `
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
		identifying, nonIdentifying, nonNilStringSlice(agent.MissingAttributes),
		nonNilStringSlice(agent.ReservedAttributeConflicts), packages, agent.LastSeen)
	if err != nil {
		return fmt.Errorf("upsert agents: %w", err)
	}
	agentsUpdated := tag.RowsAffected() > 0

	if err := saveSessionUpsert(ctx, tx, agent); err != nil {
		return err
	}

	if agentsUpdated {
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
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// SaveSession writes only agent_session, independent of the agents table
// (see saveSessionUpsert). Used by SessionSnapshotter's keepalive pass —
// see StateStore's doc comment for why this isn't routed through SaveAgent.
func (s *PostgresStore) SaveSession(ctx context.Context, agent fleet.Agent) error {
	return saveSessionUpsert(ctx, s.pool, agent)
}

// GetAgent reads one agent back, joining across all three tables. Includes
// soft-deleted rows (Agent.EvictedAt set) — filtering those out for a
// "live" view is the API/UI layer's job, not this one's; see Agent.EvictedAt.
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

// ListAgents reads every agent back, including soft-deleted rows. Same
// caveat as GetAgent.
func (s *PostgresStore) ListAgents(ctx context.Context) ([]fleet.Agent, error) {
	return s.queryAgents(ctx, "")
}

func (s *PostgresStore) queryAgents(ctx context.Context, where string, args ...any) ([]fleet.Agent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			a.instance_uid, a.first_seen, a.last_seen, a.healthy, a.health_error,
			a.health_status, a.health_start_time, a.health_status_time, a.health_reported,
			a.capabilities, a.identifying, a.non_identifying, a.missing_attributes,
			a.reserved_attribute_conflicts, a.packages, a.evicted_at,
			s.connected, s.remote_addr, s.tls_subject, s.via_gateway, s.transport,
			s.description_reported, s.sequence_num, s.updated_at
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
		evictedAt                             *time.Time
		capabilities, sequenceNum             int64
		identifying, nonIdentifying, packages []byte
		connected, viaGateway                 *bool
		remoteAddr, tlsSubject, transport     *string
		sessionUpdatedAt                      *time.Time
	)
	err := rows.Scan(
		&a.InstanceUID, &a.FirstSeen, &a.LastSeen, &a.Healthy, &a.HealthError,
		&a.HealthStatus, &healthStartTime, &healthStatusTime, &a.HealthReported,
		&capabilities, &identifying, &nonIdentifying, &a.MissingAttributes,
		&a.ReservedAttributeConflicts, &packages, &evictedAt,
		&connected, &remoteAddr, &tlsSubject, &viaGateway, &transport,
		&a.DescriptionReported, &sequenceNum, &sessionUpdatedAt)
	if err != nil {
		return fleet.Agent{}, err
	}

	a.Capabilities = uint64(capabilities) //nolint:gosec // bit-for-bit storage, no arithmetic performed on this value
	a.SequenceNum = uint64(sequenceNum)   //nolint:gosec // bit-for-bit storage, no arithmetic performed on this value
	a.EvictedAt = evictedAt
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
	if sessionUpdatedAt != nil {
		a.SessionUpdatedAt = *sessionUpdatedAt
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

// SoftDeleteAgent implements StateStore. The WHERE clause makes this
// idempotent: once evicted_at is set, a later call (e.g. a retried flush)
// leaves it untouched rather than moving it forward.
func (s *PostgresStore) SoftDeleteAgent(ctx context.Context, instanceUID string, evictedAt time.Time) error {
	sql, args := softDeleteAgentSQL(instanceUID, evictedAt)
	_, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("soft delete agent: %w", err)
	}
	return nil
}

func softDeleteAgentSQL(instanceUID string, evictedAt time.Time) (string, []any) {
	return `UPDATE agents SET evicted_at = $2 WHERE instance_uid = $1 AND evicted_at IS NULL`,
		[]any{instanceUID, evictedAt}
}

// QueueSoftDeleteAgent appends a SoftDeleteAgent-equivalent statement to
// batch instead of executing it immediately. See QueueSaveSession.
func (s *PostgresStore) QueueSoftDeleteAgent(batch *pgx.Batch, instanceUID string, evictedAt time.Time) {
	sql, args := softDeleteAgentSQL(instanceUID, evictedAt)
	batch.Queue(sql, args...)
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

// nonNilStringSlice guards against binding a nil []string to a NOT NULL
// text[] column: pgx encodes a nil slice as SQL NULL, not an empty array.
// missing_attributes/reserved_attribute_conflicts are nil on any agent that
// hasn't reported an AgentDescription yet (e.g. a bare first status message
// with no description), which is a normal, common state, not an edge case.
func nonNilStringSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
