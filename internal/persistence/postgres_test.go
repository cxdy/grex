package persistence

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dennisme/grex/internal/fleet"
	"github.com/dennisme/grex/internal/persistence/testdb"
)

func testAgent(instanceUID string, lastSeen time.Time) fleet.Agent {
	return fleet.Agent{
		InstanceUID:      instanceUID,
		FirstSeen:        lastSeen,
		LastSeen:         lastSeen,
		Healthy:          true,
		HealthStatus:     "StatusOK",
		HealthStartTime:  lastSeen,
		HealthStatusTime: lastSeen,
		HealthReported:   true,
		Capabilities:     18437,
		Identifying: map[string]string{
			"service.name": "otelcol-contrib",
		},
		NonIdentifying: map[string]string{
			"deployment.environment": "dev",
		},
		MissingAttributes:          []string{"team"},
		ReservedAttributeConflicts: []string{"healthy"},
		EffectiveConfig: map[string]string{
			"":     "receivers: {}\n",
			"logs": "level: debug\n",
		},
		Packages: map[string]fleet.Package{
			"otelcol": {Name: "otelcol", AgentHasVersion: "1.0.0", Status: "Installed"},
		},
		Conn: fleet.ConnMeta{
			RemoteAddr: "10.0.0.1:1234",
			TLSSubject: "CN=agent-1",
			ViaGateway: true,
			Transport:  "ws",
		},
		Connected:           true,
		DescriptionReported: true,
		SequenceNum:         42,
	}
}

// TestSaveAgentWithNoDescriptionYet covers a freshly connected agent whose
// first report has no AgentDescription yet — a normal, common state, not an
// edge case. MissingAttributes/ReservedAttributeConflicts are nil until
// fleet.Registry computes them, and a nil []string must not be sent to the
// NOT NULL text[] columns as SQL NULL.
func TestSaveAgentWithNoDescriptionYet(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	agent := fleet.Agent{
		InstanceUID: "agent-1",
		FirstSeen:   now,
		LastSeen:    now,
		Connected:   true,
		// MissingAttributes, ReservedAttributeConflicts, Identifying,
		// NonIdentifying, Packages, EffectiveConfig all left nil.
	}
	if err := store.SaveAgent(ctx, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	if _, ok, err := store.GetAgent(ctx, "agent-1"); err != nil || !ok {
		t.Fatalf("GetAgent: ok=%v err=%v, want found", ok, err)
	}
}

func TestSaveAndGetAgent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	agent := testAgent("agent-1", time.Now().UTC().Truncate(time.Microsecond))
	if err := store.SaveAgent(ctx, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	got, ok, err := store.GetAgent(ctx, "agent-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if !ok {
		t.Fatal("GetAgent: not found")
	}
	if got.HealthStatus != agent.HealthStatus || got.Healthy != agent.Healthy {
		t.Errorf("health = %+v, want matching %+v", got, agent)
	}
	if got.Identifying["service.name"] != "otelcol-contrib" {
		t.Errorf("Identifying = %v", got.Identifying)
	}
	if got.NonIdentifying["deployment.environment"] != "dev" {
		t.Errorf("NonIdentifying = %v", got.NonIdentifying)
	}
	if len(got.MissingAttributes) != 1 || got.MissingAttributes[0] != "team" {
		t.Errorf("MissingAttributes = %v", got.MissingAttributes)
	}
	if len(got.ReservedAttributeConflicts) != 1 || got.ReservedAttributeConflicts[0] != "healthy" {
		t.Errorf("ReservedAttributeConflicts = %v", got.ReservedAttributeConflicts)
	}
	if got.EffectiveConfig[""] != agent.EffectiveConfig[""] || got.EffectiveConfig["logs"] != agent.EffectiveConfig["logs"] {
		t.Errorf("EffectiveConfig = %v", got.EffectiveConfig)
	}
	if got.Packages["otelcol"].AgentHasVersion != "1.0.0" {
		t.Errorf("Packages = %v", got.Packages)
	}
	if got.Conn.RemoteAddr != agent.Conn.RemoteAddr || !got.Conn.ViaGateway {
		t.Errorf("Conn = %+v", got.Conn)
	}
	if !got.Connected || !got.DescriptionReported || got.SequenceNum != 42 {
		t.Errorf("session fields wrong: connected=%v description_reported=%v sequence_num=%d",
			got.Connected, got.DescriptionReported, got.SequenceNum)
	}
	if !got.HealthStartTime.Equal(agent.HealthStartTime) {
		t.Errorf("HealthStartTime = %v, want %v", got.HealthStartTime, agent.HealthStartTime)
	}
	if !got.HealthStatusTime.Equal(agent.HealthStatusTime) {
		t.Errorf("HealthStatusTime = %v, want %v", got.HealthStatusTime, agent.HealthStatusTime)
	}
}

// TestSaveAgentPreservesOriginalFirstSeen covers a grex1->grex2 reconnect:
// grex2 has never seen this agent before, so its own local registry entry
// gets created with FirstSeen set to grex2's own connect time, not the
// agent's true first-ever-seen time. That wrong value must never overwrite
// the original, even on a legitimate (guard-passing, newer LastSeen) write.
func TestSaveAgentPreservesOriginalFirstSeen(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	trueFirstSeen := time.Now().UTC().Truncate(time.Microsecond)
	first := testAgent("agent-1", trueFirstSeen)
	if err := store.SaveAgent(ctx, first); err != nil {
		t.Fatalf("SaveAgent (first): %v", err)
	}

	fromDifferentReplica := testAgent("agent-1", trueFirstSeen.Add(time.Hour))
	fromDifferentReplica.FirstSeen = trueFirstSeen.Add(time.Hour) // wrong, this replica's own connect time
	if err := store.SaveAgent(ctx, fromDifferentReplica); err != nil {
		t.Fatalf("SaveAgent (different replica): %v", err)
	}

	got, ok, err := store.GetAgent(ctx, "agent-1")
	if err != nil || !ok {
		t.Fatalf("GetAgent: %v, %v", ok, err)
	}
	if !got.FirstSeen.Equal(trueFirstSeen) {
		t.Errorf("FirstSeen = %v, want original %v preserved", got.FirstSeen, trueFirstSeen)
	}
}

func TestSaveAgentRejectsStaleWrite(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	older := time.Now().UTC().Truncate(time.Microsecond)
	newer := older.Add(time.Hour)

	newAgent := testAgent("agent-1", newer)
	newAgent.HealthStatus = "StatusOK"
	if err := store.SaveAgent(ctx, newAgent); err != nil {
		t.Fatalf("SaveAgent (newer): %v", err)
	}

	staleAgent := testAgent("agent-1", older)
	staleAgent.HealthStatus = "StaleStatus"
	if err := store.SaveAgent(ctx, staleAgent); err != nil {
		t.Fatalf("SaveAgent (stale): %v", err)
	}

	got, ok, err := store.GetAgent(ctx, "agent-1")
	if err != nil || !ok {
		t.Fatalf("GetAgent: %v, %v", ok, err)
	}
	if got.HealthStatus != "StatusOK" {
		t.Errorf("HealthStatus = %q, want the newer write to survive the stale one", got.HealthStatus)
	}
}

// TestSaveAgentPersistsDisconnectEvenWhenLastSeenUnchanged covers a real,
// always-reachable gap: Registry.Sweep marks a missed-heartbeat agent
// disconnected without ever touching LastSeen (see Sweep's own doc
// comment), so the dirty-triggered flush that follows carries the exact
// same LastSeen as what's already stored. The agents-table guard correctly
// rejects that as "not newer" — but agent_session must still record
// Connected=false; it must not be silently skipped just because the
// agents-table write was a no-op.
func TestSaveAgentPersistsDisconnectEvenWhenLastSeenUnchanged(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	lastSeen := time.Now().UTC().Truncate(time.Microsecond)
	connected := testAgent("agent-1", lastSeen)
	connected.Connected = true
	if err := store.SaveAgent(ctx, connected); err != nil {
		t.Fatalf("SaveAgent (connected): %v", err)
	}

	disconnected := testAgent("agent-1", lastSeen) // same LastSeen, Sweep never advances it
	disconnected.Connected = false
	if err := store.SaveAgent(ctx, disconnected); err != nil {
		t.Fatalf("SaveAgent (disconnected): %v", err)
	}

	got, ok, err := store.GetAgent(ctx, "agent-1")
	if err != nil || !ok {
		t.Fatalf("GetAgent: %v, %v", ok, err)
	}
	if got.Connected {
		t.Error("Connected = true, want false: the disconnect write must not be skipped just because LastSeen didn't advance")
	}
}

// TestSaveSessionRefreshesIndependentOfAgentsTable covers the wholesale
// periodic session snapshot's own write path (persistence.SessionSnapshotter
// calls SaveSession directly, not SaveAgent): it must be able to refresh
// agent_session on its own, without touching (or needing) the agents table
// at all.
func TestSaveSessionRefreshesIndependentOfAgentsTable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	t1 := time.Now().UTC().Truncate(time.Microsecond)
	agent := testAgent("agent-1", t1)
	if err := store.SaveAgent(ctx, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	t2 := t1.Add(time.Minute)
	agent.LastSeen = t2 // SessionSnapshotter's wholesale pass: fresher heartbeat, no identity/health change
	if err := store.SaveSession(ctx, agent); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	got, ok, err := store.GetAgent(ctx, "agent-1")
	if err != nil || !ok {
		t.Fatalf("GetAgent: %v, %v", ok, err)
	}
	if !got.SessionUpdatedAt.Equal(t2) {
		t.Errorf("SessionUpdatedAt = %v, want %v", got.SessionUpdatedAt, t2)
	}
	if !got.LastSeen.Equal(t1) {
		t.Errorf("LastSeen (agents table) = %v, want unchanged %v: SaveSession must not touch the agents table", got.LastSeen, t1)
	}
}

// TestSaveSessionRejectsStaleWrite mirrors TestSaveAgentRejectsStaleWrite for
// the standalone session write path.
func TestSaveSessionRejectsStaleWrite(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	older := time.Now().UTC().Truncate(time.Microsecond)
	newer := older.Add(time.Hour)

	agent := testAgent("agent-1", newer)
	agent.Connected = true
	if err := store.SaveAgent(ctx, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	stale := agent
	stale.LastSeen = older
	stale.Connected = false
	if err := store.SaveSession(ctx, stale); err != nil {
		t.Fatalf("SaveSession (stale): %v", err)
	}

	got, ok, err := store.GetAgent(ctx, "agent-1")
	if err != nil || !ok {
		t.Fatalf("GetAgent: %v, %v", ok, err)
	}
	if !got.Connected {
		t.Error("Connected = false, want true: the stale SaveSession write must be rejected")
	}
}

// TestQueueSaveSessionBatchedWritesLandCorrectly covers the chunked-batch
// path SessionSnapshotter uses at scale (docs/spec/design.md's Scaling gaps
// items 3-4): several agents' SaveSession-equivalent writes queued onto one
// pgx.Batch, sent in a single round trip, each result read in order — must
// land identically to calling SaveSession once per agent.
func TestQueueSaveSessionBatchedWritesLandCorrectly(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	agents := []fleet.Agent{
		testAgent("agent-1", now),
		testAgent("agent-2", now),
		testAgent("agent-3", now),
	}
	for _, a := range agents {
		if err := store.SaveAgent(ctx, a); err != nil {
			t.Fatalf("SaveAgent %s: %v", a.InstanceUID, err)
		}
	}

	batch := &pgx.Batch{}
	for i := range agents {
		agents[i].Connected = true
		agents[i].LastSeen = now.Add(time.Minute)
		store.QueueSaveSession(batch, agents[i])
	}
	results := store.SendBatch(ctx, batch)
	for i, a := range agents {
		if _, err := results.Exec(); err != nil {
			t.Errorf("batch result %d (%s): %v", i, a.InstanceUID, err)
		}
	}
	if err := results.Close(); err != nil {
		t.Fatalf("results.Close: %v", err)
	}

	for _, a := range agents {
		got, ok, err := store.GetAgent(ctx, a.InstanceUID)
		if err != nil || !ok {
			t.Fatalf("GetAgent %s: %v, %v", a.InstanceUID, err, ok)
		}
		if !got.Connected {
			t.Errorf("%s: Connected = false, want true after batched SaveSession", a.InstanceUID)
		}
		if !got.SessionUpdatedAt.Equal(now.Add(time.Minute)) {
			t.Errorf("%s: SessionUpdatedAt = %v, want %v", a.InstanceUID, got.SessionUpdatedAt, now.Add(time.Minute))
		}
	}
}

// TestQueueSoftDeleteAgentBatchedWritesLandCorrectly is
// TestQueueSaveSessionBatchedWritesLandCorrectly's counterpart for
// Flusher's other batched write.
func TestQueueSoftDeleteAgentBatchedWritesLandCorrectly(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	ids := []string{"agent-1", "agent-2", "agent-3"}
	for _, id := range ids {
		if err := store.SaveAgent(ctx, testAgent(id, now)); err != nil {
			t.Fatalf("SaveAgent %s: %v", id, err)
		}
	}

	batch := &pgx.Batch{}
	for _, id := range ids {
		store.QueueSoftDeleteAgent(batch, id, now)
	}
	results := store.SendBatch(ctx, batch)
	for i, id := range ids {
		if _, err := results.Exec(); err != nil {
			t.Errorf("batch result %d (%s): %v", i, id, err)
		}
	}
	if err := results.Close(); err != nil {
		t.Fatalf("results.Close: %v", err)
	}

	for _, id := range ids {
		got, ok, err := store.GetAgent(ctx, id)
		if err != nil || !ok {
			t.Fatalf("GetAgent %s: %v, %v", id, err, ok)
		}
		if got.EvictedAt == nil {
			t.Errorf("%s: EvictedAt = nil, want set after batched SoftDeleteAgent", id)
		}
	}
}

// TestSaveAgentStaleWriteDoesNotClobberEffectiveConfig covers the gap found
// while reasoning through a grex1->grex2 reconnect: agent_effective_config
// has no per-row guard of its own (it's a wholesale delete+insert), so a
// stale SaveAgent call must be rejected before it ever touches that table,
// not just have its agents/agent_session writes rejected.
func TestSaveAgentStaleWriteDoesNotClobberEffectiveConfig(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	older := time.Now().UTC().Truncate(time.Microsecond)
	newer := older.Add(time.Hour)

	newAgent := testAgent("agent-1", newer)
	newAgent.EffectiveConfig = map[string]string{"": "current config\n"}
	if err := store.SaveAgent(ctx, newAgent); err != nil {
		t.Fatalf("SaveAgent (newer): %v", err)
	}

	staleAgent := testAgent("agent-1", older)
	staleAgent.EffectiveConfig = map[string]string{"": "stale config that should never appear\n"}
	if err := store.SaveAgent(ctx, staleAgent); err != nil {
		t.Fatalf("SaveAgent (stale): %v", err)
	}

	got, ok, err := store.GetAgent(ctx, "agent-1")
	if err != nil || !ok {
		t.Fatalf("GetAgent: %v, %v", ok, err)
	}
	if got.EffectiveConfig[""] != "current config\n" {
		t.Errorf("EffectiveConfig = %v, want the stale write to have been rejected before touching this table", got.EffectiveConfig)
	}
}

func TestSaveAgentConcurrentOutOfOrderWrites(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Microsecond)
	older := testAgent("agent-1", base)
	older.HealthStatus = "older"
	newer := testAgent("agent-1", base.Add(time.Hour))
	newer.HealthStatus = "newer"

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		errs <- store.SaveAgent(ctx, newer)
	}()
	go func() {
		defer wg.Done()
		errs <- store.SaveAgent(ctx, older)
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("SaveAgent: %v", err)
		}
	}

	got, ok, err := store.GetAgent(ctx, "agent-1")
	if err != nil || !ok {
		t.Fatalf("GetAgent: %v, %v", ok, err)
	}
	if got.HealthStatus != "newer" {
		t.Errorf("HealthStatus = %q, want %q (latest event time wins regardless of write order)", got.HealthStatus, "newer")
	}
}

func TestListAgents(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.SaveAgent(ctx, testAgent("agent-1", now)); err != nil {
		t.Fatalf("SaveAgent agent-1: %v", err)
	}
	if err := store.SaveAgent(ctx, testAgent("agent-2", now)); err != nil {
		t.Fatalf("SaveAgent agent-2: %v", err)
	}

	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("ListAgents = %d agents, want 2", len(agents))
	}
}

func TestGetAgentReportsEvictedAt(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.SaveAgent(ctx, testAgent("agent-1", now)); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	got, ok, err := store.GetAgent(ctx, "agent-1")
	if err != nil || !ok {
		t.Fatalf("GetAgent before soft delete: ok=%v err=%v", ok, err)
	}
	if got.EvictedAt != nil {
		t.Fatalf("EvictedAt = %v, want nil before soft delete", got.EvictedAt)
	}

	if err := store.SoftDeleteAgent(ctx, "agent-1", now); err != nil {
		t.Fatalf("SoftDeleteAgent: %v", err)
	}

	got, ok, err = store.GetAgent(ctx, "agent-1")
	if err != nil || !ok {
		t.Fatalf("GetAgent after soft delete: ok=%v err=%v, want still present (soft delete, not removed)", ok, err)
	}
	if got.EvictedAt == nil {
		t.Fatal("EvictedAt = nil, want set after soft delete")
	}
}

func TestListAgentsIncludesSoftDeletedWithEvictedAtSet(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := store.SaveAgent(ctx, testAgent("agent-1", now)); err != nil {
		t.Fatalf("SaveAgent agent-1: %v", err)
	}
	if err := store.SaveAgent(ctx, testAgent("agent-2", now)); err != nil {
		t.Fatalf("SaveAgent agent-2: %v", err)
	}
	if err := store.SoftDeleteAgent(ctx, "agent-1", now); err != nil {
		t.Fatalf("SoftDeleteAgent: %v", err)
	}

	agents, err := store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("ListAgents = %d agents, want 2 (soft-deleted row still included)", len(agents))
	}
	byID := make(map[string]fleet.Agent, len(agents))
	for _, a := range agents {
		byID[a.InstanceUID] = a
	}
	if byID["agent-1"].EvictedAt == nil {
		t.Error("agent-1.EvictedAt = nil, want set")
	}
	if byID["agent-2"].EvictedAt != nil {
		t.Error("agent-2.EvictedAt = non-nil, want nil")
	}
}

func TestDeleteAgentCascades(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := store.SaveAgent(ctx, testAgent("agent-1", time.Now().UTC())); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}
	if err := store.DeleteAgent(ctx, "agent-1"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	if _, ok, err := store.GetAgent(ctx, "agent-1"); err != nil || ok {
		t.Fatalf("GetAgent after delete: ok=%v err=%v, want ok=false", ok, err)
	}
	var sessionCount, configCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM agent_session WHERE instance_uid = 'agent-1'`).Scan(&sessionCount); err != nil {
		t.Fatalf("count agent_session: %v", err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM agent_effective_config WHERE instance_uid = 'agent-1'`).Scan(&configCount); err != nil {
		t.Fatalf("count agent_effective_config: %v", err)
	}
	if sessionCount != 0 || configCount != 0 {
		t.Errorf("cascade left rows behind: session=%d config=%d", sessionCount, configCount)
	}
}

// The following exercise each method's real database-error path with a
// pre-cancelled context, rather than a mock: a cancelled context causes
// pgx to genuinely fail the Begin/Query/Exec call, so these are real
// errors from the real driver, not scripted behavior.

func TestSaveAgentContextCanceled(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.SaveAgent(ctx, testAgent("agent-1", time.Now().UTC()))
	if err == nil {
		t.Fatal("want error for a cancelled context")
	}
	if !strings.Contains(err.Error(), "begin") {
		t.Errorf("error = %q, want it to mention begin", err.Error())
	}
}

func TestGetAgentContextCanceled(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := store.GetAgent(ctx, "agent-1"); err == nil {
		t.Fatal("want error for a cancelled context")
	}
}

func TestListAgentsContextCanceled(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.ListAgents(ctx); err == nil {
		t.Fatal("want error for a cancelled context")
	}
}

func TestEffectiveConfigContextCanceled(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.effectiveConfig(ctx, "agent-1"); err == nil {
		t.Fatal("want error for a cancelled context")
	}
}

func TestDeleteAgentContextCanceled(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.DeleteAgent(ctx, "agent-1"); err == nil {
		t.Fatal("want error for a cancelled context")
	}
}

func TestSoftDeleteAgentContextCanceled(t *testing.T) {
	store := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.SoftDeleteAgent(ctx, "agent-1", time.Now()); err == nil {
		t.Fatal("want error for a cancelled context")
	}
}

// newTestStore starts a throwaway Postgres container, applies the
// migrations, and returns a PostgresStore against it. Skips the calling
// test if docker isn't available. See internal/persistence/testdb: the
// container/migration bootstrap itself lives there so cmd/grex's tests (the
// database.host-enabled path) can reuse it instead of a second copy.
func newTestStore(t *testing.T) *PostgresStore {
	t.Helper()
	return NewPostgresStore(testdb.Pool(t))
}
