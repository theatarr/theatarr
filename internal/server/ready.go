package server

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/theatarr/theatarr/internal/jobs"
	"github.com/theatarr/theatarr/internal/store"
)

// StoreReadiness is ready when the database answers and its schema is at the version this binary knows.
func StoreReadiness(st *store.Store) Readiness {
	return func(ctx context.Context) (map[string]any, error) {
		if err := st.Ping(ctx); err != nil {
			return nil, fmt.Errorf("database: %w", err)
		}
		cur, target, err := st.SchemaVersion(ctx)
		detail := map[string]any{"schema": map[string]int64{"current": cur, "target": target}}
		if err != nil {
			return detail, err
		}
		if cur != target {
			return detail, fmt.Errorf("database schema is at version %d, this binary expects %d", cur, target)
		}
		return detail, nil
	}
}

// jobCollector reports the job queue: jobs by state, and per kind the ready jobs and how long the oldest has waited,
// in two queries per scrape.
type jobCollector struct {
	q                   *jobs.Queue
	desc, ready, oldest *prometheus.Desc
}

// RegisterJobGauges adds theatarr_jobs{state}, theatarr_jobs_ready{kind} and theatarr_jobs_oldest_ready_seconds{kind} to reg.
func RegisterJobGauges(reg prometheus.Registerer, q *jobs.Queue) {
	reg.MustRegister(&jobCollector{q: q,
		desc: prometheus.NewDesc("theatarr_jobs", "Jobs in the queue by state.", []string{"state"}, nil),
		ready: prometheus.NewDesc("theatarr_jobs_ready", "Queued jobs past their run time, waiting for a worker slot, by kind.",
			[]string{"kind"}, nil),
		oldest: prometheus.NewDesc("theatarr_jobs_oldest_ready_seconds",
			"How long the oldest ready job of a kind has waited past its run time.", []string{"kind"}, nil)})
}

func (c *jobCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
	ch <- c.ready
	ch <- c.oldest
}

func (c *jobCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	counts, err := c.q.Counts(ctx)
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.desc, err)
		return
	}
	for state, n := range counts {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(n), state)
	}
	// Failures are not read here (an empty window): theatarr_jobs_runs_total counts them.
	bl, err := c.q.Backlog(ctx, time.Now().Add(time.Hour))
	if err != nil {
		ch <- prometheus.NewInvalidMetric(c.ready, err)
		return
	}
	for _, k := range bl {
		ch <- prometheus.MustNewConstMetric(c.ready, prometheus.GaugeValue, float64(k.Ready), k.Kind)
		ch <- prometheus.MustNewConstMetric(c.oldest, prometheus.GaugeValue, k.OldestReady.Seconds(), k.Kind)
	}
}
