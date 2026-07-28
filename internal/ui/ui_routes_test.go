package ui

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/dennisme/grex/internal/fleet"
)

func testUIRegistry(t *testing.T) (*fleet.Registry, string) {
	t.Helper()
	r := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	uid := uuid.New()
	r.Report(&protobufs.AgentToServer{
		InstanceUid: uid[:],
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{
				{
					Key:   "service.name",
					Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "route-agent"}},
				},
				{
					Key:   "service.version",
					Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "1.2.3"}},
				},
				{
					Key:   "service.component",
					Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "agent"}},
				},
			},
			NonIdentifyingAttributes: []*protobufs.KeyValue{{
				Key:   "host.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "box-1"}},
			}},
		},
		Health: &protobufs.ComponentHealth{Healthy: true},
	}, fleet.ConnMeta{ViaGateway: false, Transport: "http"})
	return r, uid.String()
}

func TestNewDefaultsZeroStartedAt(t *testing.T) {
	r, _ := testUIRegistry(t)
	h, err := New(r, Config{PollInterval: 0}, time.Time{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.cfg.PollInterval != 5*time.Second {
		t.Fatalf("default poll = %s", h.cfg.PollInterval)
	}
	if h.started.IsZero() {
		t.Fatal("started should be set when zero passed")
	}
}

func TestFleetBadPaginationAndFilters(t *testing.T) {
	r, _ := testUIRegistry(t)
	h, err := New(r, Config{}, time.Now(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)

	for _, path := range []string{
		"/?limit=nope",
		"/?offset=-1",
		"/partials/agents?limit=0",
		"/partials/agents?healthy=notabool",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", path, rec.Code)
		}
	}
}

func TestAgentAndStatusPartials(t *testing.T) {
	r, id := testUIRegistry(t)
	// Second agent: disconnected / unhealthy / awaiting for status counters.
	uid2 := uuid.New()
	r.Report(&protobufs.AgentToServer{
		InstanceUid: uid2[:],
		// No description → awaiting full state
	}, fleet.ConnMeta{})
	r.SetConnected(uid2.String(), false)

	uid3 := uuid.New()
	r.Report(&protobufs.AgentToServer{
		InstanceUid: uid3[:],
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key:   "service.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "sick"}},
			}},
		},
		Health: &protobufs.ComponentHealth{Healthy: false},
	}, fleet.ConnMeta{ViaGateway: true, Transport: "ws"})

	h, err := New(r, Config{PollInterval: time.Second}, time.Now().Add(-2*time.Hour), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)

	// Agent partial OK.
	req := httptest.NewRequest(http.MethodGet, "/partials/agents/"+id, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent partial = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "route-agent") {
		t.Error("agent partial missing name")
	}

	// Agent partial 404.
	req = httptest.NewRequest(http.MethodGet, "/partials/agents/"+uuid.New().String(), nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing agent partial = %d", rec.Code)
	}

	// Status partial.
	req = httptest.NewRequest(http.MethodGet, "/partials/status", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status partial = %d", rec.Code)
	}
	body := rec.Body.String()
	// Should render status counters for mixed fleet.
	if body == "" {
		t.Error("empty status partial")
	}

	// Full status page with multi-agent stats (healthy, unhealthy, disconnected, awaiting).
	req = httptest.NewRequest(http.MethodGet, "/status", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	// Fleet with sort columns exercises template helpers (sortHref, pageList, etc.).
	for _, q := range []string{
		"/?sort=name&order=asc",
		"/?sort=role&order=desc",
		"/?sort=version&order=asc",
		"/?sort=via&order=asc",
		"/?sort=transport&order=desc",
		"/?sort=last_seen",
		"/?sort=status&order=asc",
		"/?limit=1&offset=0",
		"/?limit=1&offset=1",
		"/?healthy=true",
		"/?connected=true&via_gateway=false",
		"/partials/agents?sort=name&order=desc&limit=1",
	} {
		req = httptest.NewRequest(http.MethodGet, q, nil)
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d", q, rec.Code)
		}
	}
}

func TestFleetPageSortAndFiltersInHTML(t *testing.T) {
	r, _ := testUIRegistry(t)
	h, err := New(r, Config{}, time.Now(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)

	req := httptest.NewRequest(http.MethodGet, "/?sort=name&order=asc&healthy=true&match=service.name%3Droute-agent", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"route-agent", "th-sort", "Healthy"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}
