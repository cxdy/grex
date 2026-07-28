package ui

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dennisme/grex/internal/fleet"
	"github.com/dennisme/grex/internal/persistence"
)

// TestAgentPageFallsBackToRealPostgres covers the actual scenario this goal
// exists for: an agent flushed by some other grex replica, never seen by
// this process's own fleet.Registry, still answerable via GET
// /agents/{id} through a real PostgresStore.
func TestAgentPageFallsBackToRealPostgres(t *testing.T) {
	store := newUITestStore(t)
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
	registry, _ := testUIRegistry(t)
	mux := newMuxWithStore(t, registry, store)

	req := httptest.NewRequest(http.MethodGet, "/agents/"+agent.InstanceUID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, agent.InstanceUID) {
		t.Errorf("body missing instance uid %q", agent.InstanceUID)
	}
	if !strings.Contains(body, "otelcol-contrib") {
		t.Errorf("body missing agent name: %s", body)
	}
}

// TestFleetPartialFallsBackToRealPostgres covers the fleet-wide list
// version of the same scenario: an agent flushed by some other grex
// replica, never seen by this process's own fleet.Registry, still appears
// in the fleet list through a real PostgresStore merge.
func TestFleetPartialFallsBackToRealPostgres(t *testing.T) {
	store := newUITestStore(t)
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

	registry, localID := testUIRegistry(t)
	mux := newMuxWithStore(t, registry, store)

	req := httptest.NewRequest(http.MethodGet, "/partials/agents", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, localID) {
		t.Errorf("body missing local registry agent %s", localID)
	}
	if !strings.Contains(body, "otelcol-contrib") {
		t.Errorf("body missing agent from sibling replica: %s", body)
	}
}

// newUITestStore starts a throwaway Postgres container, applies grex's own
// migrations (internal/persistence/migrations), and returns a real
// PostgresStore against it. Skips the calling test if docker isn't
// available. Same pattern as internal/api's own fallback integration test,
// duplicated here rather than exported cross-package (matching how
// cmd/river-migrate keeps its own copy too).
func newUITestStore(t *testing.T) persistence.StateStore {
	t.Helper()
	dsn := startUITestPostgres(t)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	applyUIMigrations(t, pool)
	return persistence.NewPostgresStore(pool)
}

func applyUIMigrations(t *testing.T, pool *pgxpool.Pool) {
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

func startUITestPostgres(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("ui-fallback-test-%d", time.Now().UnixNano())
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

	waitForUITestPostgresReady(t, name)
	return fmt.Sprintf("postgres://grex:grex-test-password@127.0.0.1:%s/grex?sslmode=disable", port)
}

// waitForUITestPostgresReady waits for the container logs to show Postgres's
// startup-ready message twice: the official postgres image runs initdb, then
// restarts the server once. pg_isready alone can succeed during the brief
// window between those two starts and still refuse real connections.
func waitForUITestPostgresReady(t *testing.T, name string) {
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
