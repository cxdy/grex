package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/dennisme/grex/internal/fleet"
	"github.com/dennisme/grex/internal/persistence"
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
	Agents  []map[string]any `json:"agents"`
	Total   int              `json:"total"`
	Limit   int              `json:"limit"`
	Offset  int              `json:"offset"`
	Partial bool             `json:"partial"`
}

func newMux(t *testing.T, r *fleet.Registry) http.Handler {
	t.Helper()
	return newMuxWithStore(t, r, nil)
}

func newMuxWithStore(t *testing.T, r *fleet.Registry, store persistence.StateStore) http.Handler {
	t.Helper()
	return newMuxWithStoreAndMetrics(t, r, store, nil)
}

func newMuxWithStoreAndMetrics(t *testing.T, r *fleet.Registry, store persistence.StateStore, m Metrics) http.Handler {
	t.Helper()
	h := New(r, time.Now(), store, m)
	mux := http.NewServeMux()
	h.Mount(mux, nil)
	return mux
}

func doGetRaw(t *testing.T, h http.Handler, path string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func doGet(t *testing.T, h http.Handler, path string) (int, testListResponse) {
	t.Helper()
	code, raw := doGetRaw(t, h, path)
	var body testListResponse
	if code == http.StatusOK && len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode response: %v (body=%s)", err, raw)
		}
	}
	return code, body
}

func TestListAgentsDefaults(t *testing.T) {
	h := newMux(t, testRegistry(t, 3))
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
	h := newMux(t, testRegistry(t, 5))

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
	h := newMux(t, testRegistry(t, 2))
	code, body := doGet(t, h, "/api/agents?offset=50")

	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Agents) != 0 || body.Total != 2 || body.Offset != 50 {
		t.Errorf("agents=%d total=%d offset=%d, want 0/2/50", len(body.Agents), body.Total, body.Offset)
	}
}

func TestListAgentsEmptyRegistry(t *testing.T) {
	h := newMux(t, testRegistry(t, 0))
	code, body := doGet(t, h, "/api/agents")

	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if len(body.Agents) != 0 || body.Total != 0 {
		t.Errorf("agents=%d total=%d, want 0/0", len(body.Agents), body.Total)
	}
}

func TestListAgentsLimitCapped(t *testing.T) {
	h := newMux(t, testRegistry(t, 3))
	_, body := doGet(t, h, fmt.Sprintf("/api/agents?limit=%d", maxLimit+500))

	if body.Limit != maxLimit {
		t.Errorf("limit = %d, want capped to %d", body.Limit, maxLimit)
	}
}

func TestListAgentsInvalidLimit(t *testing.T) {
	h := newMux(t, testRegistry(t, 1))
	for _, v := range []string{"abc", "0", "-1"} {
		code, _ := doGet(t, h, "/api/agents?limit="+v)
		if code != http.StatusBadRequest {
			t.Errorf("limit=%q: status = %d, want 400", v, code)
		}
	}
}

func TestListAgentsInvalidOffset(t *testing.T) {
	h := newMux(t, testRegistry(t, 1))
	for _, v := range []string{"abc", "-1"} {
		code, _ := doGet(t, h, "/api/agents?offset="+v)
		if code != http.StatusBadRequest {
			t.Errorf("offset=%q: status = %d, want 400", v, code)
		}
	}
}

func TestListAgentsMethodNotAllowed(t *testing.T) {
	h := newMux(t, testRegistry(t, 1))
	req := httptest.NewRequest(http.MethodPost, "/api/agents", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestListAgentsContentType(t *testing.T) {
	h := newMux(t, testRegistry(t, 1))
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
	h := newMux(t, r)
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
	h := newMux(t, r)
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
	h := newMux(t, r)
	_, body := doGet(t, h, "/api/agents?service.name=otelcol-contrib&deployment.environment=dev")

	if body.Total != 1 || len(body.Agents) != 1 {
		t.Fatalf("agents=%d total=%d, want 1/1", len(body.Agents), body.Total)
	}
}

func TestListAgentsFilterNoMatches(t *testing.T) {
	r := newAgentRegistry(t, [2]map[string]string{{"service.name": "otelcol-contrib"}, nil})
	h := newMux(t, r)
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
	h := newMux(t, r)
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
	h := newMux(t, r)
	_, body := doGet(t, h, "/api/agents?limit=2&offset=0")

	if body.Total != 3 {
		t.Errorf("total = %d, want 3 (limit/offset must not be treated as attribute filters)", body.Total)
	}
}

func TestListAgentsFilterByHealthy(t *testing.T) {
	r := newRegistry(t)
	reportAgent(r, true, fleet.ConnMeta{})
	unhealthy := reportAgent(r, false, fleet.ConnMeta{})

	h := newMux(t, r)
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

	h := newMux(t, r)
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

	h := newMux(t, r)
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

	h := newMux(t, r)
	_, body := doGet(t, h, "/api/agents?healthy=false&service.name=otelcol-contrib")

	if body.Total != 1 || len(body.Agents) != 1 {
		t.Fatalf("agents=%d total=%d, want 1/1", len(body.Agents), body.Total)
	}
}

func TestListAgentsFilterInvalidBoolValue(t *testing.T) {
	h := newMux(t, testRegistry(t, 1))
	for _, param := range []string{"healthy", "connected", "via_gateway"} {
		code, _ := doGet(t, h, "/api/agents?"+param+"=notabool")
		if code != http.StatusBadRequest {
			t.Errorf("%s=notabool: status = %d, want 400", param, code)
		}
	}
}

func TestListAgentsFilterEmptyBoolIsIgnored(t *testing.T) {
	// HTML "Any" selects submit as healthy=&connected=&via_gateway=.
	// Those must not 400; they mean no filter on that field.
	r := testRegistry(t, 2)
	h := newMux(t, r)
	code, body := doGet(t, h, "/api/agents?healthy=&connected=&via_gateway=")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body.Total != 2 {
		t.Errorf("total = %d, want 2 (empty bools are not filters)", body.Total)
	}
}

func TestListAgentsFilterEmptyBoolWithAttr(t *testing.T) {
	r := newAgentRegistry(t,
		[2]map[string]string{{"service.name": "keep"}, nil},
		[2]map[string]string{{"service.name": "drop"}, nil},
	)
	h := newMux(t, r)
	code, body := doGet(t, h, "/api/agents?healthy=&connected=&via_gateway=&attr_key=service.name&attr_value=keep")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body.Total != 1 {
		t.Fatalf("total = %d, want 1", body.Total)
	}
}

func TestListAgentsFilterMatchers(t *testing.T) {
	r := newAgentRegistry(t,
		[2]map[string]string{{"service.name": "otelcol-contrib"}, {"deployment.environment": "prod"}},
		[2]map[string]string{{"service.name": "otelcol-gateway"}, {"deployment.environment": "dev"}},
		[2]map[string]string{{"service.name": "other"}, {"deployment.environment": "prod"}},
	)
	h := newMux(t, r)

	t.Run("not equal", func(t *testing.T) {
		_, body := doGet(t, h, "/api/agents?match="+url.QueryEscape("service.name!=otelcol-contrib"))
		if body.Total != 2 {
			t.Fatalf("total = %d, want 2", body.Total)
		}
	})
	t.Run("regex", func(t *testing.T) {
		_, body := doGet(t, h, "/api/agents?match="+url.QueryEscape("service.name=~otelcol-.*"))
		if body.Total != 2 {
			t.Fatalf("total = %d, want 2", body.Total)
		}
	})
	t.Run("not regex", func(t *testing.T) {
		_, body := doGet(t, h, "/api/agents?match="+url.QueryEscape("service.name!~otelcol-.*"))
		if body.Total != 1 {
			t.Fatalf("total = %d, want 1", body.Total)
		}
	})
	t.Run("spaced matcher", func(t *testing.T) {
		_, body := doGet(t, h, "/api/agents?match="+url.QueryEscape("deployment.environment = prod"))
		if body.Total != 2 {
			t.Fatalf("total = %d, want 2", body.Total)
		}
	})
	t.Run("and multiple match", func(t *testing.T) {
		q := url.Values{}
		q.Add("match", "deployment.environment=prod")
		q.Add("match", "service.name=~otelcol-.*")
		_, body := doGet(t, h, "/api/agents?"+q.Encode())
		if body.Total != 1 {
			t.Fatalf("total = %d, want 1", body.Total)
		}
	})
	t.Run("invalid regex", func(t *testing.T) {
		code, _ := doGet(t, h, "/api/agents?match="+url.QueryEscape("service.name=~[invalid"))
		if code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", code)
		}
	})
	t.Run("quoted regex not match prefix", func(t *testing.T) {
		// service.instance.id !~ "9.+" should drop values starting with 9.
		r2 := newAgentRegistry(t,
			[2]map[string]string{{"service.instance.id": "9abc-starts-with-nine"}, nil},
			[2]map[string]string{{"service.instance.id": "a7af-starts-with-a"}, nil},
			[2]map[string]string{{"service.instance.id": "019f-starts-with-zero"}, nil},
		)
		h2 := newMux(t, r2)
		_, body := doGet(t, h2, "/api/agents?match="+url.QueryEscape(`service.instance.id !~ "9.+"`))
		if body.Total != 2 {
			t.Fatalf("total = %d, want 2 (exclude ids starting with 9)", body.Total)
		}
		for _, a := range body.Agents {
			id := a["identifying_attributes"].(map[string]any)["service.instance.id"].(string)
			if strings.HasPrefix(id, "9") {
				t.Errorf("should have filtered out %q", id)
			}
		}
	})
	t.Run("quoted regex match prefix", func(t *testing.T) {
		r2 := newAgentRegistry(t,
			[2]map[string]string{{"service.instance.id": "9abc"}, nil},
			[2]map[string]string{{"service.instance.id": "a7af"}, nil},
		)
		h2 := newMux(t, r2)
		_, body := doGet(t, h2, "/api/agents?match="+url.QueryEscape(`service.instance.id =~ '9.+'`))
		if body.Total != 1 {
			t.Fatalf("total = %d, want 1", body.Total)
		}
	})
}

func TestAttributeDiscovery(t *testing.T) {
	r := newAgentRegistry(t,
		[2]map[string]string{{"service.name": "a"}, {"deployment.environment": "dev"}},
		[2]map[string]string{{"service.name": "b"}, {"deployment.environment": "prod"}},
	)
	h := newMux(t, r)

	code, raw := doGetRaw(t, h, "/api/attributes")
	if code != http.StatusOK {
		t.Fatalf("keys status = %d", code)
	}
	var keysBody struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &keysBody); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(keysBody.Keys, "service.name") || !slices.Contains(keysBody.Keys, "deployment.environment") {
		t.Errorf("keys = %v", keysBody.Keys)
	}

	code, raw = doGetRaw(t, h, "/api/attributes/values?key=deployment.environment")
	if code != http.StatusOK {
		t.Fatalf("values status = %d", code)
	}
	var valsBody struct {
		Values []string `json:"values"`
	}
	if err := json.Unmarshal(raw, &valsBody); err != nil {
		t.Fatal(err)
	}
	if len(valsBody.Values) != 2 {
		t.Errorf("values = %v, want 2", valsBody.Values)
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
	h := newMux(t, testRegistry(t, 1))
	_, body := doGet(t, h, "/api/agents")

	if len(body.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(body.Agents))
	}
	agent := body.Agents[0]
	for _, field := range []string{
		"instance_uid", "sequence_num", "identifying_attributes", "capabilities",
		"capability_flags", "healthy", "health_reported", "description_reported",
		"connection", "connected", "first_seen", "last_seen",
		"role", "display_name",
	} {
		if _, ok := agent[field]; !ok {
			t.Errorf("agent JSON missing field %q: %v", field, agent)
		}
	}
	if _, ok := agent["effective_config"]; ok {
		t.Error("list projection should omit effective_config")
	}
}

func TestGetAgentAndStatus(t *testing.T) {
	r := newAgentRegistry(t, [2]map[string]string{{"service.name": "detail-me"}, nil})
	// grab the only agent uid
	agents := r.List()
	if len(agents) != 1 {
		t.Fatalf("setup agents = %d", len(agents))
	}
	id := agents[0].InstanceUID
	h := newMux(t, r)

	req := httptest.NewRequest(http.MethodGet, "/api/agents/"+id, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get agent status = %d", rec.Code)
	}
	var detail map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["display_name"] != "detail-me" {
		t.Errorf("display_name = %v", detail["display_name"])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestNewZeroStartedAt(t *testing.T) {
	h := New(newRegistry(t), time.Time{}, nil, nil)
	if h.startedAt.IsZero() {
		t.Fatal("startedAt should default when zero")
	}
}

func TestGetAgentNotFoundAndStatusMix(t *testing.T) {
	r := newRegistry(t)
	// Connected healthy
	reportAgent(r, true, fleet.ConnMeta{})
	// Unhealthy connected
	reportAgent(r, false, fleet.ConnMeta{})
	// Awaiting description (no health, no description) — Report always sets something;
	// use agent without health for health_unknown.
	uid := uuid.New()
	r.Report(&protobufs.AgentToServer{
		InstanceUid: uid[:],
		AgentDescription: &protobufs.AgentDescription{
			IdentifyingAttributes: []*protobufs.KeyValue{{
				Key:   "service.name",
				Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: "awaiting-health"}},
			}},
		},
		// no Health → HealthReported false
	}, fleet.ConnMeta{})
	// Disconnected
	id := reportAgent(r, true, fleet.ConnMeta{})
	r.SetConnected(id, false)

	mux := newMux(t, r)
	code, raw := doGetRaw(t, mux, "/api/agents/"+uuid.New().String())
	if code != http.StatusNotFound {
		t.Fatalf("missing agent = %d body=%s", code, raw)
	}

	code, raw = doGetRaw(t, mux, "/api/status")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	fleetStats, ok := st["fleet"].(map[string]any)
	if !ok {
		t.Fatalf("fleet stats missing: %v", st)
	}
	if fleetStats["total"].(float64) < 3 {
		t.Errorf("total = %v", fleetStats["total"])
	}
}

// fakeAPIStateStore is a minimal spy StateStore for exercising getAgent's DB
// fallback. Only GetAgent is exercised by the handler; the rest panic if
// ever called, since nothing here should reach them.
type fakeAPIStateStore struct {
	agent  fleet.Agent
	ok     bool
	err    error
	called bool

	// listAgents/listErr back ListAgents, exercised by listAgents' DB merge.
	listAgents []fleet.Agent
	listErr    error
	listCalled bool
}

var _ persistence.StateStore = (*fakeAPIStateStore)(nil)

func (f *fakeAPIStateStore) GetAgent(context.Context, string) (fleet.Agent, bool, error) {
	f.called = true
	return f.agent, f.ok, f.err
}

func (*fakeAPIStateStore) SaveAgent(context.Context, fleet.Agent) error {
	panic("not used by getAgent")
}

func (*fakeAPIStateStore) SaveSession(context.Context, fleet.Agent) error {
	panic("not used by getAgent")
}

func (f *fakeAPIStateStore) ListAgents(context.Context) ([]fleet.Agent, error) {
	f.listCalled = true
	return f.listAgents, f.listErr
}

func (*fakeAPIStateStore) DeleteAgent(context.Context, string) error {
	panic("not used by getAgent")
}

func (*fakeAPIStateStore) SoftDeleteAgent(context.Context, string, time.Time) error {
	panic("not used by getAgent")
}

func TestGetAgentFallsBackToStoreOnRegistryMiss(t *testing.T) {
	r := newRegistry(t)
	uid := uuid.New().String()
	store := &fakeAPIStateStore{
		agent: fleet.Agent{InstanceUID: uid, Healthy: true, HealthReported: true},
		ok:    true,
	}

	mux := newMuxWithStore(t, r, store)
	code, raw := doGetRaw(t, mux, "/api/agents/"+uid)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", code, raw)
	}
	if !store.called {
		t.Fatal("store.GetAgent was not called on a registry miss")
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["instance_uid"] != uid {
		t.Errorf("instance_uid = %v, want %v", got["instance_uid"], uid)
	}
}

// A DB-only agent (registry miss) whose stored Connected=true is stale
// (session_updated_at past the registry's HeartbeatInterval, matching
// Sweep's own threshold) must be reported as disconnected — the owning
// replica may have crashed without ever clearing this, and Sweep only
// evaluates locally registered agents, never a DB-only fallback like this
// one.
func TestGetAgentStoreStaleConnectedIsReportedDisconnected(t *testing.T) {
	r := newRegistry(t) // HeartbeatInterval: 30s
	uid := uuid.New().String()
	store := &fakeAPIStateStore{
		agent: fleet.Agent{
			InstanceUID:      uid,
			Connected:        true,
			SessionUpdatedAt: time.Now().Add(-time.Minute), // past the 30s threshold
		},
		ok: true,
	}

	mux := newMuxWithStore(t, r, store)
	code, raw := doGetRaw(t, mux, "/api/agents/"+uid)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", code, raw)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["connected"] != false {
		t.Errorf("connected = %v, want false: session_updated_at is past HeartbeatInterval", got["connected"])
	}
}

// The mirror case: a DB-only agent whose session was refreshed recently
// keeps Connected true — the staleness check must not overcorrect to
// "always false for a DB fallback."
func TestGetAgentStoreFreshConnectedIsPreserved(t *testing.T) {
	r := newRegistry(t) // HeartbeatInterval: 30s
	uid := uuid.New().String()
	store := &fakeAPIStateStore{
		agent: fleet.Agent{
			InstanceUID:      uid,
			Connected:        true,
			SessionUpdatedAt: time.Now().Add(-time.Second), // well within the threshold
		},
		ok: true,
	}

	mux := newMuxWithStore(t, r, store)
	code, raw := doGetRaw(t, mux, "/api/agents/"+uid)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", code, raw)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["connected"] != true {
		t.Errorf("connected = %v, want true: last_seen is within HeartbeatInterval", got["connected"])
	}
}

func TestGetAgentStoreMissIsStill404(t *testing.T) {
	r := newRegistry(t)
	store := &fakeAPIStateStore{ok: false}

	mux := newMuxWithStore(t, r, store)
	code, raw := doGetRaw(t, mux, "/api/agents/"+uuid.New().String())
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s", code, raw)
	}
	if !store.called {
		t.Fatal("store.GetAgent was not called on a registry miss")
	}
}

func TestGetAgentStoreSoftDeletedIsStill404(t *testing.T) {
	r := newRegistry(t)
	uid := uuid.New().String()
	evictedAt := time.Now()
	store := &fakeAPIStateStore{
		agent: fleet.Agent{InstanceUID: uid, EvictedAt: &evictedAt},
		ok:    true,
	}

	mux := newMuxWithStore(t, r, store)
	code, _ := doGetRaw(t, mux, "/api/agents/"+uid)
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a soft-deleted store agent", code)
	}
}

func TestGetAgentStoreErrorIs500(t *testing.T) {
	r := newRegistry(t)
	store := &fakeAPIStateStore{err: errors.New("db unavailable")}

	mux := newMuxWithStore(t, r, store)
	code, _ := doGetRaw(t, mux, "/api/agents/"+uuid.New().String())
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}
}

func TestGetAgentRegistryHitNeverConsultsStore(t *testing.T) {
	r := newRegistry(t)
	id := reportAgent(r, true, fleet.ConnMeta{})
	store := &fakeAPIStateStore{} // would panic if GetAgent were ever called with ok left false incorrectly

	mux := newMuxWithStore(t, r, store)
	code, _ := doGetRaw(t, mux, "/api/agents/"+id)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if store.called {
		t.Error("store.GetAgent was called even though the registry already had the agent")
	}
}

func TestGetAgentNoStoreConfiguredStaysNotFound(t *testing.T) {
	r := newRegistry(t)
	mux := newMux(t, r) // nil store, same as database.host unset
	code, _ := doGetRaw(t, mux, "/api/agents/"+uuid.New().String())
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (byte-identical to no-database behavior)", code)
	}
}

func TestListAgentsMergesStoreAgentsNotInRegistry(t *testing.T) {
	r := newRegistry(t)
	localID := reportAgent(r, true, fleet.ConnMeta{})
	dbOnlyID := uuid.New().String()
	store := &fakeAPIStateStore{listAgents: []fleet.Agent{{InstanceUID: dbOnlyID}}}

	mux := newMuxWithStore(t, r, store)
	code, raw := doGetRaw(t, mux, "/api/agents")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", code, raw)
	}
	if !store.listCalled {
		t.Fatal("store.ListAgents was not called")
	}
	var resp testListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 {
		t.Fatalf("total = %d, want 2", resp.Total)
	}
	var ids []string
	for _, a := range resp.Agents {
		ids = append(ids, a["instance_uid"].(string))
	}
	if !slices.Contains(ids, localID) || !slices.Contains(ids, dbOnlyID) {
		t.Errorf("ids = %v, want both %s and %s", ids, localID, dbOnlyID)
	}
}

func TestListAgentsExcludesSoftDeletedStoreAgents(t *testing.T) {
	r := newRegistry(t)
	evictedAt := time.Now()
	store := &fakeAPIStateStore{listAgents: []fleet.Agent{{InstanceUID: uuid.New().String(), EvictedAt: &evictedAt}}}

	mux := newMuxWithStore(t, r, store)
	code, raw := doGetRaw(t, mux, "/api/agents")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", code, raw)
	}
	var resp testListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 0 {
		t.Fatalf("total = %d, want 0 (soft-deleted store agent excluded)", resp.Total)
	}
}

func TestListAgentsDegradesToRegistryOnStoreError(t *testing.T) {
	r := newRegistry(t)
	localID := reportAgent(r, true, fleet.ConnMeta{})
	store := &fakeAPIStateStore{listErr: errors.New("db unavailable")}
	metrics := &fakeAPIMetrics{}

	mux := newMuxWithStoreAndMetrics(t, r, store, metrics)
	code, raw := doGetRaw(t, mux, "/api/agents")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (store error should degrade, not fail the request): body=%s", code, raw)
	}
	if !store.listCalled {
		t.Fatal("store.ListAgents was not called")
	}
	var resp testListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 || resp.Agents[0]["instance_uid"] != localID {
		t.Fatalf("resp = %+v, want registry-only agent %s", resp, localID)
	}
	if !resp.Partial {
		t.Error("partial = false, want true when the store merge failed")
	}
	if metrics.listStoreFallbackFailedSurface != "api" {
		t.Errorf("ListStoreFallbackFailed surface = %q, want %q", metrics.listStoreFallbackFailedSurface, "api")
	}
}

func TestListAgentsMergeSuccessIsNotPartial(t *testing.T) {
	r := newRegistry(t)
	reportAgent(r, true, fleet.ConnMeta{})
	store := &fakeAPIStateStore{}
	metrics := &fakeAPIMetrics{}

	mux := newMuxWithStoreAndMetrics(t, r, store, metrics)
	_, raw := doGetRaw(t, mux, "/api/agents")
	var resp testListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Partial {
		t.Error("partial = true, want false when the store merge succeeded")
	}
	if metrics.listStoreFallbackFailedSurface != "" {
		t.Error("ListStoreFallbackFailed should not fire when the merge succeeded")
	}
}

func TestListAgentsNoStoreIsNotPartial(t *testing.T) {
	r := newRegistry(t)
	reportAgent(r, true, fleet.ConnMeta{})

	mux := newMux(t, r) // nil store, same as database.host unset
	_, raw := doGetRaw(t, mux, "/api/agents")
	var resp testListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Partial {
		t.Error("partial = true, want false when no store is configured")
	}
}

// fakeAPIMetrics is a spy Metrics for exercising listAgents' fallback-error
// counter.
type fakeAPIMetrics struct {
	listStoreFallbackFailedSurface string
}

func (f *fakeAPIMetrics) ListStoreFallbackFailed(surface string) {
	f.listStoreFallbackFailedSurface = surface
}

func TestListAgentsNoStoreConfiguredSkipsMerge(t *testing.T) {
	r := newRegistry(t)
	reportAgent(r, true, fleet.ConnMeta{})
	mux := newMux(t, r) // nil store, same as database.host unset

	code, raw := doGetRaw(t, mux, "/api/agents")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", code, raw)
	}
	var resp testListResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d, want 1", resp.Total)
	}
}
