package persistence

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dennisme/grex/internal/fleet"
)

// pendingBatchOp is one statement fakeBatchStore has queued but not yet
// sent, recorded so fakeBatchResults can hand back per-item outcomes in the
// exact order they were queued — pgx.Batch results must be read in send
// order, and the fake needs to honor that same contract to be a faithful
// stand-in.
type pendingBatchOp struct {
	kind        string // "save_session" | "soft_delete" | "upsert_conn"
	instanceUID string
	agent       fleet.Agent
	evictedAt   time.Time
	conn        AgentConnection
}

// fakeBatchStore is a minimal StateStore + BatchStateStore + ConnectionStore
// + BatchConnectionStore stand-in, proving Flusher/SessionSnapshotter take
// the chunked-batch path when their store supports it, and that one item's
// error doesn't stop the rest of its chunk's results from being read — all
// without a real Postgres connection, since pgx.BatchResults is an
// interface. Methods outside what Flusher/SessionSnapshotter actually call
// panic, same convention as erroringStateStore/blockingFlusherStore.
type fakeBatchStore struct {
	mu      sync.Mutex
	saved   map[string]fleet.Agent
	evicted map[string]time.Time
	conns   map[string]AgentConnection

	// errFor makes the queued statement for this instance_uid return an
	// error when its batch result is read, without preventing the rest of
	// that chunk's results from being read afterward.
	errFor map[string]error

	sendBatchCount int
	pending        []pendingBatchOp
}

var (
	_ StateStore           = (*fakeBatchStore)(nil)
	_ BatchStateStore      = (*fakeBatchStore)(nil)
	_ ConnectionStore      = (*fakeBatchStore)(nil)
	_ BatchConnectionStore = (*fakeBatchStore)(nil)
)

func (f *fakeBatchStore) QueueSaveSession(batch *pgx.Batch, agent fleet.Agent) {
	batch.Queue("-- fake save_session, never sent to a real connection")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, pendingBatchOp{kind: "save_session", instanceUID: agent.InstanceUID, agent: agent})
}

func (f *fakeBatchStore) QueueSoftDeleteAgent(batch *pgx.Batch, instanceUID string, evictedAt time.Time) {
	batch.Queue("-- fake soft_delete, never sent to a real connection")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, pendingBatchOp{kind: "soft_delete", instanceUID: instanceUID, evictedAt: evictedAt})
}

func (f *fakeBatchStore) QueueUpsertAgentConnection(batch *pgx.Batch, conn AgentConnection) {
	batch.Queue("-- fake upsert_agent_connection, never sent to a real connection")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, pendingBatchOp{kind: "upsert_conn", instanceUID: conn.InstanceUID, conn: conn})
}

func (f *fakeBatchStore) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults {
	f.mu.Lock()
	ops := f.pending
	f.pending = nil
	f.sendBatchCount++
	f.mu.Unlock()
	return &fakeBatchResults{store: f, ops: ops}
}

// sendBatchCalls/largestBatch are safe for concurrent use, mirroring
// fakeStateStore's own hasAgent-style accessor convention.
func (f *fakeBatchStore) sendBatchCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendBatchCount
}

func (f *fakeBatchStore) savedSession(instanceUID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.saved[instanceUID]
	return ok
}

func (f *fakeBatchStore) wasEvicted(instanceUID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.evicted[instanceUID]
	return ok
}

func (f *fakeBatchStore) wasConnectionUpserted(instanceUID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.conns[instanceUID]
	return ok
}

func (f *fakeBatchStore) SaveAgent(context.Context, fleet.Agent) error {
	panic("not used by SessionSnapshotter/Flusher's batched path")
}
func (f *fakeBatchStore) SaveSession(context.Context, fleet.Agent) error {
	panic("not used once the batched path is taken")
}
func (f *fakeBatchStore) GetAgent(context.Context, string) (fleet.Agent, bool, error) {
	panic("not used by Flusher/SessionSnapshotter")
}
func (f *fakeBatchStore) ListAgents(context.Context) ([]fleet.Agent, error) {
	panic("not used by Flusher/SessionSnapshotter")
}
func (f *fakeBatchStore) DeleteAgent(context.Context, string) error {
	panic("not used by Flusher/SessionSnapshotter")
}
func (f *fakeBatchStore) SoftDeleteAgent(context.Context, string, time.Time) error {
	panic("not used once the batched path is taken")
}
func (f *fakeBatchStore) UpsertAgentConnection(context.Context, AgentConnection) error {
	panic("not used once the batched path is taken")
}
func (f *fakeBatchStore) GetAgentConnection(context.Context, string) (AgentConnection, bool, error) {
	panic("not used by Flusher")
}
func (f *fakeBatchStore) ListAgentConnections(context.Context) ([]AgentConnection, error) {
	panic("not used by Flusher")
}
func (f *fakeBatchStore) DeleteAgentConnection(context.Context, string) error {
	panic("not used by Flusher")
}

// fakeBatchResults hands back pendingBatchOp outcomes in send order,
// applying each one to the owning fakeBatchStore only once its result is
// actually read via Exec — the same "nothing happens until you read the
// result" contract pgx.BatchResults documents.
type fakeBatchResults struct {
	store *fakeBatchStore
	ops   []pendingBatchOp
	i     int
}

func (r *fakeBatchResults) Exec() (pgconn.CommandTag, error) {
	if r.i >= len(r.ops) {
		return pgconn.CommandTag{}, errors.New("fakeBatchResults: Exec called more times than statements were queued")
	}
	op := r.ops[r.i]
	r.i++

	r.store.mu.Lock()
	defer r.store.mu.Unlock()
	if err := r.store.errFor[op.instanceUID]; err != nil {
		return pgconn.CommandTag{}, err
	}
	switch op.kind {
	case "save_session":
		if r.store.saved == nil {
			r.store.saved = make(map[string]fleet.Agent)
		}
		r.store.saved[op.instanceUID] = op.agent
	case "soft_delete":
		if r.store.evicted == nil {
			r.store.evicted = make(map[string]time.Time)
		}
		r.store.evicted[op.instanceUID] = op.evictedAt
	case "upsert_conn":
		if r.store.conns == nil {
			r.store.conns = make(map[string]AgentConnection)
		}
		r.store.conns[op.instanceUID] = op.conn
	}
	return pgconn.CommandTag{}, nil
}

func (r *fakeBatchResults) Query() (pgx.Rows, error) {
	panic("not used by Flusher/SessionSnapshotter")
}

func (r *fakeBatchResults) QueryRow() pgx.Row {
	panic("not used by Flusher/SessionSnapshotter")
}

// Close drains any unread results, same as the real pgx.BatchResults
// contract — a caller that errors out of its read loop early must not
// leave the fake (or the real connection, in production) out of sync for
// the next batch.
func (r *fakeBatchResults) Close() error {
	for r.i < len(r.ops) {
		_, _ = r.Exec() // draining continues regardless of any individual error
	}
	return nil
}
