// Package testdb starts a throwaway, migrated Postgres container for tests
// that need a real database — internal/persistence's own tests, and
// anything elsewhere in the module exercising the database.host-enabled
// path (e.g. cmd/grex). Kept as its own package (not testdb_test.go inside
// internal/persistence) specifically so other packages' tests can import
// it; Go doesn't allow importing another package's _test.go-only symbols.
package testdb

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Instance is a throwaway Postgres container's connection info, in the same
// shape internal/config.Database expects — so a caller building a test
// config.yaml (cmd/grex) can copy these fields directly.
type Instance struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string
}

// DSN formats Instance as a libpq connection string, for callers
// constructing a *pgxpool.Pool directly (internal/persistence's own tests).
func (i Instance) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		i.User, i.Password, i.Host, i.Port, i.DBName, i.SSLMode)
}

// migrationsDir is internal/persistence/migrations, resolved relative to
// this source file rather than the calling test's working directory — so
// New works the same whether it's called from internal/persistence's own
// tests or a different package's (e.g. cmd/grex).
func migrationsDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "migrations")
}

// New starts a throwaway Postgres container via docker, waits for
// readiness, and applies every grex migration. Skips the calling test if
// docker isn't available. Registers its own teardown; the caller does not
// need to remove the container itself.
func New(t *testing.T) Instance {
	t.Helper()
	instance := startContainer(t)
	pool, err := pgxpool.New(context.Background(), instance.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	applyMigrations(t, pool)
	return instance
}

// Pool is a convenience for callers that just want a ready *pgxpool.Pool
// (internal/persistence's own tests, which build a PostgresStore directly
// over it) rather than the discrete config fields New returns.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	instance := startContainer(t)
	pool, err := pgxpool.New(context.Background(), instance.DSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	applyMigrations(t, pool)
	return pool
}

func startContainer(t *testing.T) Instance {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("grex-testdb-%d", time.Now().UnixNano())
	const user, password, dbname = "grex", "grex-test-password", "grex"
	runArgs := []string{
		"run", "-d", "--name", name,
		"-e", "POSTGRES_USER=" + user,
		"-e", "POSTGRES_PASSWORD=" + password,
		"-e", "POSTGRES_DB=" + dbname,
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
	_, portStr, err := net.SplitHostPort(strings.TrimSpace(string(portOut)))
	if err != nil {
		t.Fatalf("parse docker port output %q: %v", portOut, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse docker port %q: %v", portStr, err)
	}

	waitForReady(t, name)

	return Instance{Host: "127.0.0.1", Port: port, User: user, Password: password, DBName: dbname, SSLMode: "disable"}
}

func applyMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	dir := migrationsDir()
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

// waitForReady waits for the container logs to show Postgres's
// startup-ready message twice: the official postgres image runs initdb,
// then restarts the server once. pg_isready alone can succeed during the
// brief window between those two starts and still refuse real connections.
func waitForReady(t *testing.T, name string) {
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
