package persistence

import (
	"context"
	"fmt"
)

var _ ConnectionStore = (*PostgresStore)(nil)

// UpsertAgentConnection registers (or moves) which replica currently holds
// an agent's socket. A second call for the same InstanceUID overwrites the
// owner rather than erroring or adding a row — that's the exact mechanism
// the HA handoff case needs (see docs/spec/design.md's Dispatch routing
// section): an agent reconnecting to a different replica is a normal event,
// not a conflict.
//
// connected_at only moves when replica_id actually changes (a real
// handoff). A caller (Flusher) re-upserting the same replica_id on every
// refresh tick to keep last_seen fresh must not reset connected_at to "now"
// each time — that would make it indistinguishable from last_seen and lose
// when the connection actually started.
func (s *PostgresStore) UpsertAgentConnection(ctx context.Context, conn AgentConnection) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO agent_connections (instance_uid, replica_id, replica_label, connected_at, last_seen)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (instance_uid) DO UPDATE SET
			replica_id = EXCLUDED.replica_id,
			replica_label = EXCLUDED.replica_label,
			connected_at = CASE WHEN agent_connections.replica_id = EXCLUDED.replica_id
				THEN agent_connections.connected_at ELSE EXCLUDED.connected_at END,
			last_seen = EXCLUDED.last_seen`,
		conn.InstanceUID, conn.ReplicaID, conn.ReplicaLabel, conn.ConnectedAt, conn.LastSeen)
	if err != nil {
		return fmt.Errorf("upsert agent_connections: %w", err)
	}
	return nil
}

// GetAgentConnection reads which replica currently owns instanceUID's
// socket, per this table's last write.
func (s *PostgresStore) GetAgentConnection(ctx context.Context, instanceUID string) (AgentConnection, bool, error) {
	conns, err := s.queryAgentConnections(ctx, `WHERE instance_uid = $1`, instanceUID)
	if err != nil {
		return AgentConnection{}, false, err
	}
	if len(conns) == 0 {
		return AgentConnection{}, false, nil
	}
	return conns[0], true, nil
}

// ListAgentConnections reads every agent_connections row.
func (s *PostgresStore) ListAgentConnections(ctx context.Context) ([]AgentConnection, error) {
	return s.queryAgentConnections(ctx, "")
}

func (s *PostgresStore) queryAgentConnections(ctx context.Context, where string, args ...any) ([]AgentConnection, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT instance_uid, replica_id, replica_label, connected_at, last_seen
		FROM agent_connections `+where+` ORDER BY instance_uid`, args...)
	if err != nil {
		return nil, fmt.Errorf("query agent_connections: %w", err)
	}
	defer rows.Close()

	var conns []AgentConnection
	for rows.Next() {
		var c AgentConnection
		if err := rows.Scan(&c.InstanceUID, &c.ReplicaID, &c.ReplicaLabel, &c.ConnectedAt, &c.LastSeen); err != nil {
			return nil, fmt.Errorf("scan agent_connections: %w", err)
		}
		conns = append(conns, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent_connections: %w", err)
	}
	return conns, nil
}

// DeleteAgentConnection deregisters a clean disconnect. Deleting a row that
// doesn't exist is not an error: the caller is declaring "this instance_uid
// no longer has a live socket," which is already true if there's no row.
func (s *PostgresStore) DeleteAgentConnection(ctx context.Context, instanceUID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM agent_connections WHERE instance_uid = $1`, instanceUID)
	if err != nil {
		return fmt.Errorf("delete agent_connections: %w", err)
	}
	return nil
}
