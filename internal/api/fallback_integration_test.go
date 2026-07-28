package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dennisme/grex/internal/fleet"
	"github.com/dennisme/grex/internal/persistence"
)

// TestGetAgentFallsBackToRealPostgres covers the actual scenario this goal
// exists for: an agent flushed by some other grex replica, never seen by
// this process's own fleet.Registry, still answerable via GET
// /api/agents/{id} through a real PostgresStore.
func TestGetAgentFallsBackToRealPostgres(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	agent := fleet.Agent{
		InstanceUID:  "agent-from-sibling-replica",
		FirstSeen:    time.Now().UTC().Truncate(time.Microsecond),
		LastSeen:     time.Now().UTC().Truncate(time.Microsecond),
		Healthy:      true,
		HealthStatus: "StatusOK",
		Identifying:  map[string]string{"service.name": "otelcol-contrib"},
		Connected:    true,
	}
	if err := store.SaveAgent(ctx, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	// This registry never saw the agent — simulates a request landing on a
	// different replica than the one holding the agent's live connection.
	registry := newRegistry(t)
	mux := newMuxWithStore(t, registry, store)

	code, raw := doGetRaw(t, mux, "/api/agents/"+agent.InstanceUID)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", code, raw)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["instance_uid"] != agent.InstanceUID {
		t.Errorf("instance_uid = %v, want %v", got["instance_uid"], agent.InstanceUID)
	}
	if got["healthy"] != true {
		t.Errorf("healthy = %v, want true", got["healthy"])
	}
}

// TestListAgentsFallsBackToRealPostgres covers the fleet-wide list version
// of the same scenario: an agent flushed by some other grex replica, never
// seen by this process's own fleet.Registry, still appears in GET
// /api/agents through a real PostgresStore merge.
func TestListAgentsFallsBackToRealPostgres(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	agent := fleet.Agent{
		InstanceUID:  "agent-from-sibling-replica",
		FirstSeen:    time.Now().UTC().Truncate(time.Microsecond),
		LastSeen:     time.Now().UTC().Truncate(time.Microsecond),
		Healthy:      true,
		HealthStatus: "StatusOK",
		Identifying:  map[string]string{"service.name": "otelcol-contrib"},
		Connected:    true,
	}
	if err := store.SaveAgent(ctx, agent); err != nil {
		t.Fatalf("SaveAgent: %v", err)
	}

	registry := newRegistry(t)
	localID := reportAgent(registry, true, fleet.ConnMeta{})
	mux := newMuxWithStore(t, registry, store)

	code, raw := doGetRaw(t, mux, "/api/agents")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", code, raw)
	}
	var resp listResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d, want 2", resp.Total)
	}
	var ids []string
	for _, a := range resp.Agents {
		ids = append(ids, a.InstanceUID)
	}
	if !slices.Contains(ids, localID) || !slices.Contains(ids, agent.InstanceUID) {
		t.Errorf("ids = %v, want both %s and %s", ids, localID, agent.InstanceUID)
	}
}

// newTestStore starts a throwaway Postgres container, applies grex's own
// migrations (internal/persistence/migrations), and returns a real
// PostgresStore against it. Skips the calling test if docker isn't
// available. Same pattern as internal/persistence's own test helpers,
// duplicated here rather than exported cross-package (matching how
// cmd/river-migrate keeps its own copy too).
func newTestStore(t *testing.T) persistence.StateStore {
	t.Helper()
	dsn := startTestPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	applyMigrations(t, pool)
	return persistence.NewPostgresStore(pool)
}

func applyMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	const dir = "../persistence/migrations"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		sql, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // test-only, name comes from os.ReadDir of our own migrations dir
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := pool.Exec(context.Background(), string(sql)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
}

func startTestPostgres(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("api-fallback-test-%d", time.Now().UnixNano())
	runArgs := []string{
		"run", "-d", "--name", name,
		"-e", "POSTGRES_USER=grex",
		"-e", "POSTGRES_PASSWORD=grex-test-password",
		"-e", "POSTGRES_DB=grex",
		"-p", "127.0.0.1::5432",
		"postgres:17.2-alpine",
	}
	if out, err := exec.Command("docker", runArgs...).CombinedOutput(); err != nil { //nolint:gosec // test-only, fixed docker subcommand + generated container name
		t.Skipf("docker run postgres: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", name).Run() //nolint:gosec // test-only, fixed docker subcommand + generated container name
	})

	portOut, err := exec.Command("docker", "port", name, "5432/tcp").CombinedOutput() //nolint:gosec // test-only, fixed docker subcommand + generated container name
	if err != nil {
		t.Fatalf("docker port: %v: %s", err, portOut)
	}
	_, port, err := net.SplitHostPort(strings.TrimSpace(string(portOut)))
	if err != nil {
		t.Fatalf("parse docker port output %q: %v", portOut, err)
	}

	waitForPostgresReady(t, name)
	return fmt.Sprintf("postgres://grex:grex-test-password@127.0.0.1:%s/grex?sslmode=disable", port)
}

// waitForPostgresReady waits for the container logs to show Postgres's
// startup-ready message twice: the official postgres image runs initdb, then
// restarts the server once. pg_isready alone can succeed during the brief
// window between those two starts and still refuse real connections.
func waitForPostgresReady(t *testing.T, name string) {
	t.Helper()
	const readyMsg = "database system is ready to accept connections"
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("docker", "logs", name).CombinedOutput() //nolint:gosec // test-only, fixed docker subcommand + generated container name
		if strings.Count(string(out), readyMsg) >= 2 {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatal("postgres did not become ready in time")
}
