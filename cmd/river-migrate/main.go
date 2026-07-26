// river-migrate runs River's own schema migrator against the database named
// by DATABASE_URL, creating or updating River's tables (river_job,
// river_leader, etc). It is a one-shot dev-infra step, not part of grex's
// runtime; grex's own tables are migrated separately via golang-migrate.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

func main() {
	if err := run(context.Background(), os.Getenv("DATABASE_URL"), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "river-migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURL string, out io.Writer) error {
	if databaseURL == "" {
		return errors.New("DATABASE_URL must be set")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("build migrator: %w", err)
	}
	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	if len(res.Versions) == 0 {
		_, _ = fmt.Fprintln(out, "river schema already up to date")
		return nil
	}
	for _, v := range res.Versions {
		_, _ = fmt.Fprintf(out, "applied version %d: %s\n", v.Version, v.Name)
	}
	return nil
}
