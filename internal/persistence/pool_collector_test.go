package persistence

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dennisme/grex/internal/persistence/testdb"
)

// TestPoolCollectorReportsPoolStats covers real pool utilization visibility:
// max_conns should reflect the pool's own configured size, and
// acquired_conns should move as connections are actually held, not a
// static value.
func TestPoolCollectorReportsPoolStats(t *testing.T) {
	ctx := context.Background()
	pool := testdb.Pool(t)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer conn.Release()

	reg := prometheus.NewRegistry()
	reg.MustRegister(NewPoolCollector(pool))

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	value := func(name string) float64 {
		t.Helper()
		for _, fam := range families {
			if fam.GetName() == name {
				return fam.GetMetric()[0].GetGauge().GetValue()
			}
		}
		t.Fatalf("metric family %s not found in %v", name, families)
		return 0
	}

	if got, want := value("grex_persistence_pool_max_conns"), float64(pool.Config().MaxConns); got != want {
		t.Errorf("max conns = %v, want %v", got, want)
	}
	if got := value("grex_persistence_pool_acquired_conns"); got < 1 {
		t.Errorf("acquired conns = %v, want >= 1 (one held open by this test)", got)
	}
}
