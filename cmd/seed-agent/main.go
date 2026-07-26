// seed-agent writes one or more fake agents directly into Postgres via
// persistence.PostgresStore.SaveAgent, bypassing fleet.Registry entirely.
// It exists to manually reproduce the DB read fallback in
// internal/api.Handler.getAgent: run grex with no agents connected to it,
// seed an instance_uid with this tool, then GET /api/agents/{id} against
// that grex and see it answered from the database instead of a 404. See
// docs/developer/testing.md's "Manual: DB read fallback" section.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dennisme/grex/internal/fleet"
	"github.com/dennisme/grex/internal/persistence"
)

func main() {
	if err := run(context.Background(), os.Getenv("DATABASE_URL"), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "seed-agent:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURL string, ids []string) error {
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL must be set")
	}
	if len(ids) == 0 {
		ids = []string{"agent-from-replica-1", "agent-from-replica-2"}
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	store := persistence.NewPostgresStore(pool)
	now := time.Now().UTC()

	for i, id := range ids {
		agent := fleet.Agent{
			InstanceUID:  id,
			FirstSeen:    now,
			LastSeen:     now,
			Healthy:      i%2 == 0, // alternate so a multi-id seed shows both states
			HealthStatus: "StatusOK",
			Identifying:  map[string]string{"service.name": "otelcol-contrib"},
			Connected:    true,
		}
		if !agent.Healthy {
			agent.HealthStatus = "StatusFail"
			agent.Identifying["service.name"] = "otelcol-gateway"
		}
		if err := store.SaveAgent(ctx, agent); err != nil {
			return fmt.Errorf("save %s: %w", id, err)
		}
		log.Printf("seeded %s (healthy=%v)", id, agent.Healthy) //nolint:gosec // id is an operator-supplied CLI arg to a local dev tool, not external input
	}
	return nil
}
