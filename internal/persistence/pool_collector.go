package persistence

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	descPoolAcquiredConns = prometheus.NewDesc(
		"grex_persistence_pool_acquired_conns",
		"Connections currently acquired (in use) from the persistence connection pool.",
		nil, nil)
	descPoolMaxConns = prometheus.NewDesc(
		"grex_persistence_pool_max_conns",
		"Configured maximum size of the persistence connection pool.",
		nil, nil)
)

// PoolCollector derives connection-pool utilization gauges from pgxpool at
// scrape time. Direct visibility into whether the pool is saturated —
// e.g. grex sized with far more CPUs than Postgres can actually sustain
// concurrent writes for (pgxpool's own default MaxConns, when
// unconfigured, is max(4, runtime.NumCPU()) — the grex process's own CPU
// count, unrelated to the database server's capacity) — shows up here as
// acquired_conns pinned at max_conns, a sharper signal than inferring it
// from write latency alone.
type PoolCollector struct {
	pool *pgxpool.Pool
}

// NewPoolCollector builds a collector over pool. Does not take ownership;
// the caller still owns pool's lifecycle (including Close).
func NewPoolCollector(pool *pgxpool.Pool) *PoolCollector {
	return &PoolCollector{pool: pool}
}

// Describe implements prometheus.Collector.
func (c *PoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- descPoolAcquiredConns
	ch <- descPoolMaxConns
}

// Collect implements prometheus.Collector.
func (c *PoolCollector) Collect(ch chan<- prometheus.Metric) {
	stat := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(descPoolAcquiredConns, prometheus.GaugeValue, float64(stat.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(descPoolMaxConns, prometheus.GaugeValue, float64(stat.MaxConns()))
}
