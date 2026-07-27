package ui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/dennisme/grex/internal/fleet"
	"github.com/dennisme/grex/internal/persistence"
)

// fakeUIStateStore is a minimal spy StateStore for exercising agentData's DB
// fallback. Only GetAgent is exercised by the handler; the rest panic if
// ever called, since nothing here should reach them.
type fakeUIStateStore struct {
	agent  fleet.Agent
	ok     bool
	err    error
	called bool

	// listAgents/listErr back ListAgents, exercised by fleetData's DB merge.
	listAgents []fleet.Agent
	listErr    error
	listCalled bool
}

var _ persistence.StateStore = (*fakeUIStateStore)(nil)

func (f *fakeUIStateStore) GetAgent(context.Context, string) (fleet.Agent, bool, error) {
	f.called = true
	return f.agent, f.ok, f.err
}

func (*fakeUIStateStore) SaveAgent(context.Context, fleet.Agent) error {
	panic("not used by agentData")
}

func (f *fakeUIStateStore) ListAgents(context.Context) ([]fleet.Agent, error) {
	f.listCalled = true
	return f.listAgents, f.listErr
}

func (*fakeUIStateStore) DeleteAgent(context.Context, string) error {
	panic("not used by agentData")
}

func (*fakeUIStateStore) SoftDeleteAgent(context.Context, string, time.Time) error {
	panic("not used by agentData")
}

func newMuxWithStore(t *testing.T, r *fleet.Registry, store persistence.StateStore) http.Handler {
	t.Helper()
	return newMuxWithStoreAndMetrics(t, r, store, nil)
}

func newMuxWithStoreAndMetrics(t *testing.T, r *fleet.Registry, store persistence.StateStore, m Metrics) http.Handler {
	t.Helper()
	h, err := New(r, Config{}, time.Now(), store, m)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)
	return mux
}

func TestAgentPageFallsBackToStoreOnRegistryMiss(t *testing.T) {
	r, _ := testUIRegistry(t)
	uid := uuid.New().String()
	store := &fakeUIStateStore{
		agent: fleet.Agent{
			InstanceUID: uid,
			Identifying: map[string]string{"service.name": "from-store"},
			Healthy:     true,
		},
		ok: true,
	}

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/agents/"+uid, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !store.called {
		t.Fatal("store.GetAgent was not called on a registry miss")
	}
	if !strings.Contains(rec.Body.String(), "from-store") {
		t.Errorf("body missing store-backed agent name: %s", rec.Body.String())
	}
}

func TestAgentPartialFallsBackToStoreOnRegistryMiss(t *testing.T) {
	r, _ := testUIRegistry(t)
	uid := uuid.New().String()
	store := &fakeUIStateStore{
		agent: fleet.Agent{
			InstanceUID: uid,
			Identifying: map[string]string{"service.name": "from-store"},
			Healthy:     true,
		},
		ok: true,
	}

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/partials/agents/"+uid, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !store.called {
		t.Fatal("store.GetAgent was not called on a registry miss")
	}
	if !strings.Contains(rec.Body.String(), "from-store") {
		t.Errorf("body missing store-backed agent name: %s", rec.Body.String())
	}
}

func TestAgentPageStoreSoftDeletedIsStill404(t *testing.T) {
	r, _ := testUIRegistry(t)
	evictedAt := time.Now()
	store := &fakeUIStateStore{
		agent: fleet.Agent{InstanceUID: uuid.New().String(), EvictedAt: &evictedAt},
		ok:    true,
	}

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/agents/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a soft-deleted store agent", rec.Code)
	}
}

func TestAgentPageStoreMissIsStill404(t *testing.T) {
	r, _ := testUIRegistry(t)
	store := &fakeUIStateStore{ok: false}

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/agents/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !store.called {
		t.Fatal("store.GetAgent was not called on a registry miss")
	}
}

func TestAgentPartialStoreMissIsStill404(t *testing.T) {
	r, _ := testUIRegistry(t)
	store := &fakeUIStateStore{ok: false}

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/partials/agents/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !store.called {
		t.Fatal("store.GetAgent was not called on a registry miss")
	}
}

func TestAgentPageStoreErrorStaysSafe404(t *testing.T) {
	r, _ := testUIRegistry(t)
	store := &fakeUIStateStore{err: errors.New("db unavailable")}

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/agents/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "db unavailable") {
		t.Error("store error leaked into HTML response")
	}
}

func TestAgentPartialStoreErrorStaysSafe404(t *testing.T) {
	r, _ := testUIRegistry(t)
	store := &fakeUIStateStore{err: errors.New("db unavailable")}

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/partials/agents/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "db unavailable") {
		t.Error("store error leaked into HTML response")
	}
}

func TestAgentPageRegistryHitNeverConsultsStore(t *testing.T) {
	r, id := testUIRegistry(t)
	store := &fakeUIStateStore{} // would panic if GetAgent were ever called incorrectly

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/agents/"+id, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if store.called {
		t.Error("store.GetAgent was called even though the registry already had the agent")
	}
}

func TestAgentPartialRegistryHitNeverConsultsStore(t *testing.T) {
	r, id := testUIRegistry(t)
	store := &fakeUIStateStore{}

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/partials/agents/"+id, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if store.called {
		t.Error("store.GetAgent was called even though the registry already had the agent")
	}
}

func TestAgentPageNoStoreConfiguredStaysNotFound(t *testing.T) {
	r, _ := testUIRegistry(t)
	h, err := New(r, Config{}, time.Now(), nil, nil) // nil store, same as database.host unset
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)

	req := httptest.NewRequest(http.MethodGet, "/agents/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (byte-identical to no-database behavior)", rec.Code)
	}
}

func TestAgentPartialNoStoreConfiguredStaysNotFound(t *testing.T) {
	r, _ := testUIRegistry(t)
	h, err := New(r, Config{}, time.Now(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Mount(mux)

	req := httptest.NewRequest(http.MethodGet, "/partials/agents/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (byte-identical to no-database behavior)", rec.Code)
	}
}

func TestFleetPartialMergesStoreAgentsNotInRegistry(t *testing.T) {
	r, localID := testUIRegistry(t)
	dbOnlyID := uuid.New().String()
	store := &fakeUIStateStore{
		listAgents: []fleet.Agent{{
			InstanceUID: dbOnlyID,
			Identifying: map[string]string{"service.name": "from-store"},
		}},
	}

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/partials/agents", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !store.listCalled {
		t.Fatal("store.ListAgents was not called")
	}
	body := rec.Body.String()
	if !strings.Contains(body, localID) {
		t.Errorf("body missing local registry agent %s", localID)
	}
	if !strings.Contains(body, "from-store") {
		t.Errorf("body missing store-backed agent: %s", body)
	}
}

func TestFleetPartialExcludesSoftDeletedStoreAgents(t *testing.T) {
	r, _ := testUIRegistry(t)
	evictedAt := time.Now()
	store := &fakeUIStateStore{
		listAgents: []fleet.Agent{{
			InstanceUID: uuid.New().String(),
			Identifying: map[string]string{"service.name": "evicted-agent"},
			EvictedAt:   &evictedAt,
		}},
	}

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/partials/agents", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "evicted-agent") {
		t.Error("soft-deleted store agent leaked into fleet list")
	}
}

func TestFleetPartialDegradesToRegistryOnStoreError(t *testing.T) {
	r, localID := testUIRegistry(t)
	store := &fakeUIStateStore{err: errors.New("db unavailable"), listErr: errors.New("db unavailable")}
	metrics := &fakeUIMetrics{}

	mux := newMuxWithStoreAndMetrics(t, r, store, metrics)
	req := httptest.NewRequest(http.MethodGet, "/partials/agents", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (store error should degrade, not fail the request): body=%s", rec.Code, rec.Body.String())
	}
	if !store.listCalled {
		t.Fatal("store.ListAgents was not called")
	}
	if !strings.Contains(rec.Body.String(), localID) {
		t.Errorf("body missing local registry agent %s", localID)
	}
	if strings.Contains(rec.Body.String(), "db unavailable") {
		t.Error("store error leaked into HTML response")
	}
	if !strings.Contains(rec.Body.String(), "partial-banner") {
		t.Error("body missing partial-data banner")
	}
	if metrics.listStoreFallbackFailedSurface != "ui" {
		t.Errorf("ListStoreFallbackFailed surface = %q, want %q", metrics.listStoreFallbackFailedSurface, "ui")
	}
}

func TestFleetPartialMergeSuccessShowsNoBanner(t *testing.T) {
	r, _ := testUIRegistry(t)
	store := &fakeUIStateStore{}

	mux := newMuxWithStore(t, r, store)
	req := httptest.NewRequest(http.MethodGet, "/partials/agents", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "partial-banner") {
		t.Error("banner shown even though the store merge succeeded")
	}
}

// fakeUIMetrics is a spy Metrics for exercising fleetData's fallback-error
// counter.
type fakeUIMetrics struct {
	listStoreFallbackFailedSurface string
}

func (f *fakeUIMetrics) ListStoreFallbackFailed(surface string) {
	f.listStoreFallbackFailedSurface = surface
}
