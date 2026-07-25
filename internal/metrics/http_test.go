package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestHTTPMetricsRecordsRequests(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := NewHTTPMetrics(reg)

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	notFound := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	wrapped := h.Instrument("/api/agents", ok)
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/agents", nil))

	wrappedOther := h.Instrument("/api/other", notFound)
	wrappedOther.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/other", nil))

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, fam := range families {
		names[fam.GetName()] = true
	}
	if !names["grex_api_requests_total"] {
		t.Error("grex_api_requests_total not registered")
	}
	if !names["grex_api_request_duration_seconds"] {
		t.Error("grex_api_request_duration_seconds not registered")
	}

	got := testutil.ToFloat64(h.requests.WithLabelValues("/api/agents", "200", "get"))
	if got != 2 {
		t.Errorf("agents route count = %v, want 2", got)
	}
	got = testutil.ToFloat64(h.requests.WithLabelValues("/api/other", "404", "post"))
	if got != 1 {
		t.Errorf("other route count = %v, want 1", got)
	}
}

func TestHTTPMetricsDistinctRoutesShareRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := NewHTTPMetrics(reg)

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// Instrumenting two different routes must not panic on duplicate
	// registration; both curry the same underlying vectors.
	a := h.Instrument("/api/agents", ok)
	b := h.Instrument("/api/status", ok)

	a.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	b.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/status", nil))
}
