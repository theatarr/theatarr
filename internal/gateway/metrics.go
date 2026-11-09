package gateway

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

type metrics struct {
	current        atomic.Pointer[Gateway]
	requests       *prometheus.CounterVec
	latency        *prometheus.HistogramVec
	refused        *prometheus.CounterVec
	budgetWait     *prometheus.HistogramVec
	budgetRefused  *prometheus.CounterVec
	budgetBackoffs *prometheus.CounterVec
	targetBackoffs *prometheus.CounterVec
	egressChecks   *prometheus.CounterVec

	vpnOK      *prometheus.Desc
	checkAge   *prometheus.Desc
	observedIP *prometheus.Desc
	backoff    *prometheus.Desc
	window     *prometheus.Desc
}

func newMetrics(g *Gateway) *metrics {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "theatarr_gateway_requests_total",
			Help: "Outbound requests sent, by host, target (class:key) and status class (2xx..5xx, error).",
		}, []string{"host", "target", "status"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "theatarr_gateway_request_seconds", Help: "Outbound request time to response headers, by host.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"host"}),
		refused: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "theatarr_gateway_refused_total",
			Help: "Outbound requests refused before or while sending, by target and reason " +
				"(unknown_target, host, vpn, backoff, budget, too_large).",
		}, []string{"target", "reason"}),
		budgetWait: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "theatarr_gateway_budget_wait_seconds", Help: "Time requests waited for a budget token, by budget.",
			Buckets: []float64{0, .1, .5, 1, 2, 5, 10, 30, 60},
		}, []string{"budget"}),
		budgetRefused: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "theatarr_gateway_budget_refused_total", Help: "Requests a budget refused, by budget and reason (budget, backoff, cap).",
		}, []string{"budget", "reason"}),
		budgetBackoffs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "theatarr_gateway_budget_backoffs_total", Help: "Back-offs set on a budget after a 429 or Retry-After.",
		}, []string{"budget"}),
		targetBackoffs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "theatarr_gateway_target_backoffs_total",
			Help: "Back-offs set on one target after a 429 or Retry-After, when its budget is shared and only the target was limited.",
		}, []string{"target"}),
		egressChecks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "theatarr_egress_checks_total", Help: "Egress checks by path and result (ok, not_vpn, error).",
		}, []string{"path", "result"}),
		vpnOK: prometheus.NewDesc("theatarr_egress_vpn_ok",
			"1 if the path's latest egress check is fresh and saw a VPN address; 0 otherwise.", []string{"path"}, nil),
		checkAge: prometheus.NewDesc("theatarr_egress_check_age_seconds",
			"Seconds since the path's latest egress check (absent before the first).", []string{"path"}, nil),
		observedIP: prometheus.NewDesc("theatarr_egress_observed_ip_info",
			"The exit address the latest egress check saw; always 1.", []string{"path", "ip"}, nil),
		backoff: prometheus.NewDesc("theatarr_gateway_backoff_seconds",
			"Seconds left on an active back-off, by subject (budget:<key>, indexer:<key>, ...).", []string{"subject"}, nil),
		window: prometheus.NewDesc("theatarr_gateway_budget_window_requests",
			"Requests counted in the current hourly or daily window of a capped budget.", []string{"budget", "period"}, nil),
	}
	m.current.Store(g)
	return m
}

func (m *metrics) register(r prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{m.requests, m.latency, m.refused, m.budgetWait, m.budgetRefused,
		m.budgetBackoffs, m.targetBackoffs, m.egressChecks, m} {
		if err := r.Register(c); err != nil {
			return err
		}
	}
	return nil
}

func (m *metrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- m.vpnOK
	ch <- m.checkAge
	ch <- m.observedIP
	ch <- m.backoff
	ch <- m.window
}

func (m *metrics) Collect(ch chan<- prometheus.Metric) {
	g := m.current.Load()
	if g == nil {
		return
	}
	now := g.now()
	for _, s := range g.EgressStates() {
		ok := 0.0
		if s.OK && !s.CheckedAt.IsZero() && g.egress != nil && now.Sub(s.CheckedAt) <= g.egress.maxAge {
			ok = 1
		}
		ch <- prometheus.MustNewConstMetric(m.vpnOK, prometheus.GaugeValue, ok, s.Path)
		if !s.CheckedAt.IsZero() {
			ch <- prometheus.MustNewConstMetric(m.checkAge, prometheus.GaugeValue, now.Sub(s.CheckedAt).Seconds(), s.Path)
		}
		if s.IP != "" {
			ch <- prometheus.MustNewConstMetric(m.observedIP, prometheus.GaugeValue, 1, s.Path, s.IP)
		}
	}
	backoffs, windows := g.state.snapshot()
	for subj, b := range backoffs {
		if left := b.until.Sub(now); left > 0 {
			ch <- prometheus.MustNewConstMetric(m.backoff, prometheus.GaugeValue, left.Seconds(), subj)
		}
	}
	for _, b := range g.buckets {
		for _, k := range b.windows(now) {
			ch <- prometheus.MustNewConstMetric(m.window, prometheus.GaugeValue, float64(windows[k]), b.key, k.period)
		}
	}
}
