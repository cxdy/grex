package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRunMissingDatabaseURL(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), "", &out)
	if err == nil {
		t.Fatal("want error for empty DATABASE_URL")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error = %q, want it to mention DATABASE_URL", err.Error())
	}
}

func TestRunBadDatabaseURL(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), "not-a-valid-dsn", &out)
	if err == nil {
		t.Fatal("want error for malformed DATABASE_URL")
	}
}

func TestRunMigrateUpConnectionError(t *testing.T) {
	var out bytes.Buffer
	// Nothing listens on 127.0.0.1:1; well-formed DSN, unreachable connect.
	// Exercises Migrate's own error path without needing docker.
	err := run(context.Background(), "postgres://x:x@127.0.0.1:1/x?sslmode=disable&connect_timeout=1", &out)
	if err == nil {
		t.Fatal("want error for unreachable database")
	}
	if !strings.Contains(err.Error(), "migrate up") {
		t.Errorf("error = %q, want it to mention migrate up", err.Error())
	}
}

func TestRunIntegration(t *testing.T) {
	dsn := startTestPostgres(t)
	ctx := context.Background()

	var out bytes.Buffer
	if err := run(ctx, dsn, &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "applied version 1:") {
		t.Errorf("output = %q, want applied versions listed", out.String())
	}

	out.Reset()
	if err := run(ctx, dsn, &out); err != nil {
		t.Fatalf("run (second call): %v", err)
	}
	if !strings.Contains(out.String(), "river schema already up to date") {
		t.Errorf("output = %q, want already-up-to-date message", out.String())
	}
}

func TestMainErrorPath(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	var code int
	prevExit := exitFunc
	exitFunc = func(c int) { code = c }
	t.Cleanup(func() { exitFunc = prevExit })
	main()
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestMainSuccess(t *testing.T) {
	dsn := startTestPostgres(t)
	t.Setenv("DATABASE_URL", dsn)
	called := false
	prevExit := exitFunc
	exitFunc = func(int) { called = true }
	t.Cleanup(func() { exitFunc = prevExit })
	main()
	if called {
		t.Error("exitFunc should not be called on success")
	}
}

// startTestPostgres starts a throwaway Postgres container via the docker
// CLI and returns its DSN. Skips the calling test if docker isn't available.
func startTestPostgres(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("river-migrate-test-%d", time.Now().UnixNano())
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
