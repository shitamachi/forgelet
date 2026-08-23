// Package metrics exposes the control-plane Prometheus instrumentation
// (spec 0010 FR-O3): queue depth, dispatch latency and job duration/success
// rate. The registry is private to forgelet — nothing registers into the
// global prometheus default registry, so tests stay isolated.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/shitamachi/forgelet/internal/run/model"
)

// Metrics carries the control-plane collectors.
type Metrics struct {
	registry *prometheus.Registry

	queueDepth      prometheus.Gauge
	dispatchLatency prometheus.Histogram
	jobDuration     *prometheus.HistogramVec
	completedTotal  *prometheus.CounterVec
}

// New wires the metric set with its own registry.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{registry: reg}
	m.queueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "forgelet",
		Name:      "jobrun_queue_depth",
		Help:      "JobRuns waiting for dispatch.",
	})
	m.dispatchLatency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "forgelet",
		Name:      "dispatch_duration_seconds",
		Help:      "Time from JobRun creation to dispatch acknowledgement.",
		Buckets:   []float64{0.1, 0.5, 1, 5, 15, 60, 300},
	})
	m.jobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "forgelet",
		Name:      "job_duration_seconds",
		Help:      "Wall-clock duration of terminal JobRuns by status.",
		Buckets:   []float64{1, 10, 30, 60, 300, 900, 3600},
	}, []string{"status", "runner_class"})
	m.completedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "forgelet",
		Name:      "jobruns_completed_total",
		Help:      "Terminal JobRuns by final status; success rate derives from this.",
	}, []string{"status"})
	reg.MustRegister(m.queueDepth, m.dispatchLatency, m.jobDuration, m.completedTotal)
	return m
}

// Handler serves the registry in Prometheus text format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// SetQueueDepth publishes the current number of queued JobRuns.
func (m *Metrics) SetQueueDepth(n int) { m.queueDepth.Set(float64(n)) }

// ObserveDispatch records how long a JobRun waited before dispatch.
func (m *Metrics) ObserveDispatch(created time.Time, dispatchedAt time.Time) {
	if !created.IsZero() && !dispatchedAt.IsZero() && dispatchedAt.After(created) {
		m.dispatchLatency.Observe(dispatchedAt.Sub(created).Seconds())
	}
}

// ObserveCompletion records duration and completion counters for a terminal
// JobRun. Non-terminal records are ignored (projection replays).
func (m *Metrics) ObserveCompletion(rec model.JobRunRecord) {
	if !rec.Status.IsTerminal() {
		return
	}
	m.completedTotal.WithLabelValues(string(rec.Status)).Inc()
	if rec.StartedAt != nil && rec.FinishedAt != nil && rec.FinishedAt.After(*rec.StartedAt) {
		m.jobDuration.WithLabelValues(string(rec.Status), rec.RunnerClass).
			Observe(rec.FinishedAt.Sub(*rec.StartedAt).Seconds())
	}
}
