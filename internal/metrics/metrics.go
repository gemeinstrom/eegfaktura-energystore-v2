// Package metrics holds the Prometheus collectors used by energystore-v2.
// Collectors are registered against a private Registry so /metrics is
// emitted cleanly (no Go runtime + process metrics that would clash if
// promauto's default registry is used elsewhere). The registry is
// exposed via Handler().
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics groups the collectors so callers can pass them around.
type Metrics struct {
	registry *prometheus.Registry

	MQTTMessagesTotal   *prometheus.CounterVec
	MQTTDecodeErrors    prometheus.Counter
	MQTTUpsertErrors    prometheus.Counter
	MQTTDLQWrites       prometheus.Counter
	MQTTConnected       prometheus.Gauge
	MQTTUpsertBatchSize prometheus.Histogram
	MQTTUpsertDuration  prometheus.Histogram

	HTTPRequestDuration *prometheus.HistogramVec
	HTTPRequestsTotal   *prometheus.CounterVec
}

// New constructs a freshly-registered Metrics bundle.
func New() *Metrics {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector())
	r.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	m := &Metrics{
		registry: r,

		MQTTMessagesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "esv2", Subsystem: "mqtt",
				Name: "messages_total", Help: "MQTT messages handled, by source + result.",
			},
			[]string{"source", "result"}, // source: energy|inverter, result: ok|decode_error|upsert_error
		),
		MQTTDecodeErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "esv2", Subsystem: "mqtt",
			Name: "decode_errors_total", Help: "Payload decode failures.",
		}),
		MQTTUpsertErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "esv2", Subsystem: "mqtt",
			Name: "upsert_errors_total", Help: "Upsert failures after successful decode.",
		}),
		MQTTDLQWrites: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "esv2", Subsystem: "mqtt",
			Name: "dlq_writes_total", Help: "Messages written to the dead-letter queue.",
		}),
		MQTTConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "esv2", Subsystem: "mqtt",
			Name: "connected", Help: "1 if the MQTT client is currently connected.",
		}),
		MQTTUpsertBatchSize: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "esv2", Subsystem: "mqtt",
			Name: "upsert_batch_size", Help: "Slot count per UpsertSlots batch.",
			Buckets: []float64{1, 4, 16, 64, 256, 1024, 4096, 16384},
		}),
		MQTTUpsertDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "esv2", Subsystem: "mqtt",
			Name: "upsert_duration_seconds", Help: "End-to-end ingest latency: decode + upsert.",
			Buckets: prometheus.DefBuckets,
		}),

		HTTPRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "esv2", Subsystem: "http",
				Name: "request_duration_seconds", Help: "HTTP handler latency.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"route", "method", "status"},
		),
		HTTPRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "esv2", Subsystem: "http",
				Name: "requests_total", Help: "HTTP requests handled.",
			},
			[]string{"route", "method", "status"},
		),
	}
	r.MustRegister(
		m.MQTTMessagesTotal,
		m.MQTTDecodeErrors,
		m.MQTTUpsertErrors,
		m.MQTTDLQWrites,
		m.MQTTConnected,
		m.MQTTUpsertBatchSize,
		m.MQTTUpsertDuration,
		m.HTTPRequestDuration,
		m.HTTPRequestsTotal,
	)
	return m
}

// Handler returns the /metrics HTTP handler.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

// Instrument wraps an http.Handler and records request_duration_seconds +
// requests_total. The `route` label must be passed in: net/http's mux
// doesn't expose the matched pattern at handler time, so we annotate
// per-route at registration time.
func (m *Metrics) Instrument(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)
		dur := time.Since(start).Seconds()
		status := httpStatusBucket(ww.status)
		m.HTTPRequestDuration.WithLabelValues(route, r.Method, status).Observe(dur)
		m.HTTPRequestsTotal.WithLabelValues(route, r.Method, status).Inc()
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func httpStatusBucket(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	default:
		return "2xx"
	}
}
