package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HTTPMetrics instruments HTTP handlers with request counts and durations,
// partitioned by route, HTTP method, and status code.
type HTTPMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewHTTPMetrics builds and registers the underlying vectors. Call
// Instrument once per route to wrap its handler.
func NewHTTPMetrics(reg prometheus.Registerer) *HTTPMetrics {
	h := &HTTPMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "grex_api_requests_total",
			Help: "API requests, by route, method, and status code.",
		}, []string{"route", "code", "method"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "grex_api_request_duration_seconds",
			Help:    "API request duration in seconds, by route, method, and status code.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "code", "method"}),
	}
	reg.MustRegister(h.requests, h.duration)
	return h
}

// Instrument wraps next so every request against it is counted and timed
// under the given route label.
func (h *HTTPMetrics) Instrument(route string, next http.Handler) http.Handler {
	counter := h.requests.MustCurryWith(prometheus.Labels{"route": route})
	duration := h.duration.MustCurryWith(prometheus.Labels{"route": route})
	return promhttp.InstrumentHandlerDuration(duration, promhttp.InstrumentHandlerCounter(counter, next))
}
