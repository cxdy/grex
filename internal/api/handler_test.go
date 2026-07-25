package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/dennisme/grex/internal/fleet"
)

func testRegistry(t *testing.T, n int) *fleet.Registry {
	t.Helper()
	r := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	for i := range n {
		uid := uuid.New()
		r.Report(&protobufs.AgentToServer{
			InstanceUid: uid[:],
			AgentDescription: &protobufs.AgentDescription{
				IdentifyingAttributes: []*protobufs.KeyValue{{
					Key: "service.name",
					Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{
						StringValue: fmt.Sprintf("agent-%d", i),
					}},
				}},
			},
		}, fleet.ConnMeta{})
	}
	return r
}

// newAgentRegistry builds a registry from explicit identifying and
// non-identifying attribute maps, one agent per map pair, for filter tests.
func newAgentRegistry(t *testing.T, agents ...[2]map[string]string) *fleet.Registry {
	t.Helper()
	r := fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	toKV := func(m map[string]string) []*protobufs.KeyValue {
		var kvs []*protobufs.KeyValue
		for k, v := range m {
			kvs = append(kvs, &protobufs.KeyValue{
				Key:   k,
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: v}},
			})
		}
		return kvs
	}
	for _, a := range agents {
		uid := uuid.New()
		r.Report(&protobufs.AgentToServer{
			InstanceUid: uid[:],
			AgentDescription: &protobufs.AgentDescription{
				IdentifyingAttributes:    toKV(a[0]),
				NonIdentifyingAttributes: toKV(a[1]),
			},
		}, fleet.ConnMeta{})
	}
	return r
}

func newRegistry(t *testing.T) *fleet.Registry {
	t.Helper()
	return fleet.New(fleet.Config{
		HeartbeatInterval:     30 * time.Second,
		StaleMissedHeartbeats: 3,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

// reportAgent registers one agent with the given health and connection
// state, for top-level-field filter tests.
func reportAgent(r *fleet.Registry, healthy bool, meta fleet.ConnMeta) string {
	uid := uuid.New()
	r.Report(&protobufs.AgentToServer{
		InstanceUid:      uid[:],
		Health:           &protobufs.ComponentHealth{Healthy: healthy},
		AgentDescription: &protobufs.AgentDescription{},
	}, meta)
	return uid.String()
}

type testListResponse struct {
	Agents []map[string]any `json:"agents"`
	Total  int              `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

func doGet(t *testing.T, h http.Handler, path string) (int, testListResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body testListResponse
	if rec.Code == http.StatusOK && rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
		}
	}
	return rec.Code, body
}

func TestListAgentsDefaults(t *testing.T) {
	h := New(testRegistry(t, 3))
	code, body := doGet(t, h, "/api/agents")

	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Agents) != 3 || body.Total != 3 {
		t.Errorf("agents=%d total=%d, want 3/3", len(body.Agents), body.Total)
	}
	if body.Limit != defaultLimit || body.Offset != 0 {
		t.Errorf("limit=%d offset=%d, want %d/0", body.Limit, body.Offset, defaultLimit)
	}
}

func TestListAgentsPaginationIsStableAndOrdered(t *testing.T) {
	h := New(testRegistry(t, 5))

	_, page1 := doGet(t, h, "/api/agents?limit=2&offset=0")
	_, page2 := doGet(t, h, "/api/agents?limit=2&offset=2")
	_, page3 := doGet(t, h, "/api/agents?limit=2&offset=4")

	if len(page1.Agents) != 2 || len(page2.Agents) != 2 || len(page3.Agents) != 1 {
		t.Fatalf("page sizes = %d/%d/%d, want 2/2/1", len(page1.Agents), len(page2.Agents), len(page3.Agents))
	}
	for _, p := range []testListResponse{page1, page2, page3} {
		if p.Total != 5 {
			t.Errorf("total = %d, want 5", p.Total)
		}
	}

	seen := map[string]bool{}
	var ordered []string
	for _, p := range []testListResponse{page1, page2, page3} {
		for _, a := range p.Agents {
			id := a["instance_uid"].(string)
			if seen[id] {
				t.Errorf("agent %s appeared on more than one page", id)
			}
			seen[id] = true
			ordered = append(ordered, id)
		}
	}
	if len(seen) != 5 {
		t.Errorf("saw %d distinct agents across pages, want 5", len(seen))
	}
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1] >= ordered[i] {
			t.Errorf("pages not consistently ordered by instance_uid: %v", ordered)
			break
		}
	}
}

func TestListAgentsOffsetBeyondTotal(t *testing.T) {
	h := New(testRegistry(t, 2))
	code, body := doGet(t, h, "/api/agents?offset=50")

	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Agents) != 0 || body.Total != 2 || body.Offset != 50 {
		t.Errorf("agents=%d total=%d offset=%d, want 0/2/50", len(body.Agents), body.Total, body.Offset)
	}
}

func TestListAgentsEmptyRegistry(t *testing.T) {
	h := New(testRegistry(t, 0))
	code, body := doGet(t, h, "/api/agents")

	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Agents) != 0 || body.Total != 0 {
		t.Errorf("agents=%d total=%d, want 0/0", len(body.Agents), body.Total)
	}
}

func TestListAgentsLimitCapped(t *testing.T) {
	h := New(testRegistry(t, 3))
	_, body := doGet(t, h, fmt.Sprintf("/api/agents?limit=%d", maxLimit+500))

	if body.Limit != maxLimit {
		t.Errorf("limit = %d, want capped to %d", body.Limit, maxLimit)
	}
}

func TestListAgentsInvalidLimit(t *testing.T) {
	h := New(testRegistry(t, 1))
	for _, v := range []string{"abc", "0", "-1"} {
		code, _ := doGet(t, h, "/api/agents?limit="+v)
		if code != http.StatusBadRequest {
			t.Errorf("limit=%q: status = %d, want 400", v, code)
		}
	}
}

func TestListAgentsInvalidOffset(t *testing.T) {
	h := New(testRegistry(t, 1))
	for _, v := range []string{"abc", "-1"} {
		code, _ := doGet(t, h, "/api/agents?offset="+v)
		if code != http.StatusBadRequest {
			t.Errorf("offset=%q: status = %d, want 400", v, code)
		}
	}
}

func TestListAgentsMethodNotAllowed(t *testing.T) {
	h := New(testRegistry(t, 1))
	req := httptest.NewRequest(http.MethodPost, "/api/agents", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestListAgentsContentType(t *testing.T) {
	h := New(testRegistry(t, 1))
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestListAgentsFilterByIdentifyingAttribute(t *testing.T) {
	r := newAgentRegistry(t,
		[2]map[string]string{{"service.name": "otelcol-contrib"}, nil},
		[2]map[string]string{{"service.name": "otelcol-gateway"}, nil},
	)
	h := New(r)
	_, body := doGet(t, h, "/api/agents?service.name=otelcol-contrib")

	if body.Total != 1 || len(body.Agents) != 1 {
		t.Fatalf("agents=%d total=%d, want 1/1", len(body.Agents), body.Total)
	}
	if body.Agents[0]["identifying_attributes"].(map[string]any)["service.name"] != "otelcol-contrib" {
		t.Errorf("wrong agent matched: %v", body.Agents[0])
	}
}

func TestListAgentsFilterByNonIdentifyingAttribute(t *testing.T) {
	r := newAgentRegistry(t,
		[2]map[string]string{nil, {"deployment.environment": "dev"}},
		[2]map[string]string{nil, {"deployment.environment": "prod"}},
	)
	h := New(r)
	_, body := doGet(t, h, "/api/agents?deployment.environment=prod")

	if body.Total != 1 || len(body.Agents) != 1 {
		t.Fatalf("agents=%d total=%d, want 1/1", len(body.Agents), body.Total)
	}
}

func TestListAgentsFilterMultipleKeysAreANDed(t *testing.T) {
	r := newAgentRegistry(t,
		[2]map[string]string{{"service.name": "otelcol-contrib"}, {"deployment.environment": "dev"}},
		[2]map[string]string{{"service.name": "otelcol-contrib"}, {"deployment.environment": "prod"}},
		[2]map[string]string{{"service.name": "otelcol-gateway"}, {"deployment.environment": "dev"}},
	)
	h := New(r)
	_, body := doGet(t, h, "/api/agents?service.name=otelcol-contrib&deployment.environment=dev")

	if body.Total != 1 || len(body.Agents) != 1 {
		t.Fatalf("agents=%d total=%d, want 1/1", len(body.Agents), body.Total)
	}
}

func TestListAgentsFilterNoMatches(t *testing.T) {
	r := newAgentRegistry(t, [2]map[string]string{{"service.name": "otelcol-contrib"}, nil})
	h := New(r)
	_, body := doGet(t, h, "/api/agents?service.name=nonexistent")

	if body.Total != 0 || len(body.Agents) != 0 {
		t.Errorf("agents=%d total=%d, want 0/0", len(body.Agents), body.Total)
	}
}

func TestListAgentsFilterTotalReflectsFilteredSetForPagination(t *testing.T) {
	r := newAgentRegistry(t,
		[2]map[string]string{{"service.name": "otelcol-contrib"}, nil},
		[2]map[string]string{{"service.name": "otelcol-contrib"}, nil},
		[2]map[string]string{{"service.name": "otelcol-gateway"}, nil},
	)
	h := New(r)
	_, body := doGet(t, h, "/api/agents?service.name=otelcol-contrib&limit=1")

	if body.Total != 2 {
		t.Errorf("total = %d, want 2 (filtered set size, not full fleet)", body.Total)
	}
	if len(body.Agents) != 1 {
		t.Errorf("agents = %d, want 1 (limit applied after filter)", len(body.Agents))
	}
}

func TestListAgentsFilterIgnoresReservedParams(t *testing.T) {
	r := testRegistry(t, 3)
	h := New(r)
	_, body := doGet(t, h, "/api/agents?limit=2&offset=0")

	if body.Total != 3 {
		t.Errorf("total = %d, want 3 (limit/offset must not be treated as attribute filters)", body.Total)
	}
}

func TestListAgentsFilterByHealthy(t *testing.T) {
	r := newRegistry(t)
	reportAgent(r, true, fleet.ConnMeta{})
	unhealthy := reportAgent(r, false, fleet.ConnMeta{})

	h := New(r)
	_, body := doGet(t, h, "/api/agents?healthy=false")

	if body.Total != 1 || len(body.Agents) != 1 {
		t.Fatalf("agents=%d total=%d, want 1/1", len(body.Agents), body.Total)
	}
	if body.Agents[0]["instance_uid"] != unhealthy {
		t.Errorf("wrong agent matched: %v", body.Agents[0])
	}
}

func TestListAgentsFilterByConnected(t *testing.T) {
	r := newRegistry(t)
	reportAgent(r, true, fleet.ConnMeta{})
	disconnected := reportAgent(r, true, fleet.ConnMeta{})
	r.SetConnected(disconnected, false)

	h := New(r)
	_, body := doGet(t, h, "/api/agents?connected=false")

	if body.Total != 1 || len(body.Agents) != 1 {
		t.Fatalf("agents=%d total=%d, want 1/1", len(body.Agents), body.Total)
	}
	if body.Agents[0]["instance_uid"] != disconnected {
		t.Errorf("wrong agent matched: %v", body.Agents[0])
	}
}

func TestListAgentsFilterByViaGateway(t *testing.T) {
	r := newRegistry(t)
	reportAgent(r, true, fleet.ConnMeta{ViaGateway: false})
	gatewayed := reportAgent(r, true, fleet.ConnMeta{ViaGateway: true})

	h := New(r)
	_, body := doGet(t, h, "/api/agents?via_gateway=true")

	if body.Total != 1 || len(body.Agents) != 1 {
		t.Fatalf("agents=%d total=%d, want 1/1", len(body.Agents), body.Total)
	}
	if body.Agents[0]["instance_uid"] != gatewayed {
		t.Errorf("wrong agent matched: %v", body.Agents[0])
	}
}

func TestListAgentsFilterCombinesBoolAndAttribute(t *testing.T) {
	r := newRegistry(t)
	uid := uuid.New()
	r.Report(&protobufs.AgentToServer{
		InstanceUid: uid[:],
		Health:      &protobufs.ComponentHealth{Healthy: false},
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key:   "service.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "otelcol-contrib"}},
			}},
		},
	}, fleet.ConnMeta{})
	reportAgent(r, false, fleet.ConnMeta{}) // unhealthy but different service.name

	h := New(r)
	_, body := doGet(t, h, "/api/agents?healthy=false&service.name=otelcol-contrib")

	if body.Total != 1 || len(body.Agents) != 1 {
		t.Fatalf("agents=%d total=%d, want 1/1", len(body.Agents), body.Total)
	}
}

func TestListAgentsFilterInvalidBoolValue(t *testing.T) {
	h := New(testRegistry(t, 1))
	for _, param := range []string{"healthy", "connected", "via_gateway"} {
		code, _ := doGet(t, h, "/api/agents?"+param+"=notabool")
		if code != http.StatusBadRequest {
			t.Errorf("%s=notabool: status = %d, want 400", param, code)
		}
	}
}

// TestBoolFieldsMatchFleetReservedAttributeKeys keeps this package's filter
// key set and fleet's ReservedAttributeKeys (which drives the
// grex_agent_reserved_attribute_conflicts_total metric) from drifting apart:
// every key the API special-cases must be one fleet warns about, and vice
// versa.
func TestBoolFieldsMatchFleetReservedAttributeKeys(t *testing.T) {
	want := make(map[string]bool, len(fleet.ReservedAttributeKeys))
	for _, key := range fleet.ReservedAttributeKeys {
		want[key] = true
	}
	for key := range boolFields {
		if !want[key] {
			t.Errorf("boolFields has %q, not in fleet.ReservedAttributeKeys", key)
		}
	}
	for key := range want {
		if _, ok := boolFields[key]; !ok {
			t.Errorf("fleet.ReservedAttributeKeys has %q, not filterable via boolFields", key)
		}
	}
}

func TestListAgentsFullAttributeSet(t *testing.T) {
	h := New(testRegistry(t, 1))
	_, body := doGet(t, h, "/api/agents")

	if len(body.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(body.Agents))
	}
	agent := body.Agents[0]
	for _, field := range []string{
		"instance_uid", "sequence_num", "identifying_attributes", "capabilities",
		"capability_flags", "healthy", "health_reported", "description_reported",
		"connection", "connected", "first_seen", "last_seen",
	} {
		if _, ok := agent[field]; !ok {
			t.Errorf("agent JSON missing field %q: %v", field, agent)
		}
	}
}
