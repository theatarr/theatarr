package shadow

import (
	"context"
	"math"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/theatarr/theatarr/internal/store"
)

type metrics struct {
	agreement        *prometheus.GaugeVec
	versionAgreement *prometheus.GaugeVec
	reasons          *prometheus.GaugeVec
	diffs            *prometheus.GaugeVec
	grabs            *prometheus.CounterVec
	searches         *prometheus.CounterVec
	requests         prometheus.Counter
	rssItems         prometheus.Counter
	deferred         prometheus.Counter
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		versionAgreement: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "theatarr_shadow_version_agreement_ratio", Help: "Agreement per version, including live theatarr-only grabs; NaN without comparisons."}, []string{"version", "window"}),
		reasons:          prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "theatarr_shadow_disagreement_reasons", Help: "Disagreement rows per version, bucket and reason in the reporting window."}, []string{"version", "bucket", "reason", "window"}),
		agreement: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "theatarr_shadow_agreement_ratio",
			Help: "Share of comparisons that agree, including live theatarr-only grabs; dry-run hand grabs left out; NaN without comparisons."},
			[]string{"window"}),
		diffs: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "theatarr_shadow_diffs",
			Help: "Diff rows in the window, per bucket."}, []string{"bucket", "window"}),
		grabs: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "theatarr_shadow_grabs_total",
			Help: "Arr grabs re-decided, per bucket."}, []string{"bucket"}),
		searches: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "theatarr_shadow_searches_total",
			Help: "Shadow searches per indexer and outcome (ok, error)."}, []string{"indexer", "outcome"}),
		requests: prometheus.NewCounter(prometheus.CounterOpts{Name: "theatarr_shadow_indexer_requests_total",
			Help: "Upstream indexer requests the shadow runner made (searches and RSS; cached results not counted)."}),
		rssItems: prometheus.NewCounter(prometheus.CounterOpts{Name: "theatarr_shadow_rss_items_total",
			Help: "New RSS items decided."}),
		deferred: prometheus.NewCounter(prometheus.CounterOpts{Name: "theatarr_shadow_grab_deferrals_total",
			Help: "Grabs whose search was put off (hourly cap, or no indexer answered)."}),
	}
	if reg != nil {
		reg.MustRegister(m.versionAgreement, m.reasons, m.agreement, m.diffs, m.grabs, m.searches, m.requests, m.rssItems, m.deferred)
	}
	return m
}

// RefreshMetrics sets the agreement and bucket gauges from the database.
func (r *Runner) RefreshMetrics(ctx context.Context) error {
	s, err := LoadSummary(ctx, r.st.Reader(), r.now())
	if err != nil {
		return err
	}
	ratio := math.NaN()
	if s.Agreement != nil {
		ratio = *s.Agreement
	}
	r.m.agreement.WithLabelValues(agreementWinName).Set(ratio)
	for _, b := range Buckets {
		r.m.diffs.WithLabelValues(b, agreementWinName).Set(float64(s.Buckets[b]))
	}
	r.m.versionAgreement.Reset()
	for _, v := range s.Versions {
		ratio := math.NaN()
		if v.Agreement != nil {
			ratio = *v.Agreement
		}
		r.m.versionAgreement.WithLabelValues(v.Version, agreementWinName).Set(ratio)
	}
	rows, err := r.st.Reader().QueryContext(ctx, "SELECT version_key, bucket, reason, COUNT(*) FROM shadow_diff WHERE at >= ? AND bucket != ? GROUP BY version_key, bucket, reason", store.Millis(s.Since), BucketAgree)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.m.reasons.Reset()
	for rows.Next() {
		var v, b, reason string
		var n int
		if err := rows.Scan(&v, &b, &reason, &n); err != nil {
			return err
		}
		if !IsMetadataPending(reason) {
			r.m.reasons.WithLabelValues(v, b, reason, agreementWinName).Set(float64(n))
		}
	}
	return rows.Err()
}
