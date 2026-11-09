package indexer

import "github.com/prometheus/client_golang/prometheus"

var seaDexLockWait = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name: "theatarr_seadex_lock_wait_seconds", Help: "Time spent acquiring the SeaDex observation lock, including failed waits.",
	Buckets: []float64{.001, .01, .1, 1, 5, 15, 60, 120, 300},
})

func RegisterSeaDexMetrics(reg prometheus.Registerer) {
	reg.MustRegister(seaDexLockWait)
}
