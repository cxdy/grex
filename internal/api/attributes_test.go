package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/dennisme/grex/internal/fleet"
)

func TestAttributeValuesEmptyKey(t *testing.T) {
	t.Parallel()
	agents := []fleet.Agent{
		{Identifying: map[string]string{"service.name": "a"}},
	}
	if got := AttributeValues(agents, ""); got != nil {
		t.Fatalf("AttributeValues empty key = %v, want nil", got)
	}
}

func TestAttributeValuesIdentifyingAndNonIdentifying(t *testing.T) {
	t.Parallel()
	agents := []fleet.Agent{
		{
			Identifying:    map[string]string{"service.name": "a", "service.version": "1"},
			NonIdentifying: map[string]string{"deployment.environment": "prod"},
		},
		{
			Identifying:    map[string]string{"service.name": "b"},
			NonIdentifying: map[string]string{"deployment.environment": "dev", "service.version": "2"},
		},
	}
	vals := AttributeValues(agents, "deployment.environment")
	if !slices.Equal(vals, []string{"dev", "prod"}) {
		t.Fatalf("values = %v", vals)
	}
	// Key only on identifying for one agent, non-identifying for another.
	vers := AttributeValues(agents, "service.version")
	if !slices.Equal(vers, []string{"1", "2"}) {
		t.Fatalf("versions = %v", vers)
	}
}

func TestListAttributeKeysPrefixFilter(t *testing.T) {
	r := newAgentRegistry(t,
		[2]map[string]string{{"service.name": "a", "host.name": "h1"}, {"deployment.environment": "prod"}},
		[2]map[string]string{{"service.name": "b"}, {"os.type": "linux"}},
	)
	h := newMux(t, r)

	code, raw := doGetRaw(t, h, "/api/attributes?prefix=service")
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%s", code, raw)
	}
	var body struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(body.Keys, "service.name") {
		t.Errorf("expected service.name in %v", body.Keys)
	}
	for _, k := range body.Keys {
		if k == "host.name" || k == "os.type" || k == "deployment.environment" {
			t.Errorf("prefix=service should exclude %q", k)
		}
	}

	// Case-insensitive prefix filter (substring).
	code, raw = doGetRaw(t, h, "/api/attributes?prefix=HOST")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(body.Keys, "host.name") {
		t.Errorf("keys = %v, want host.name", body.Keys)
	}
}

func TestListAttributeValuesRequiresKey(t *testing.T) {
	h := newMux(t, newRegistry(t))
	code, raw := doGetRaw(t, h, "/api/attributes/values")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", code, raw)
	}
	if !strings.Contains(string(raw), "key") {
		t.Errorf("body = %q, want key required message", raw)
	}
}

func TestListAttributeValuesPrefixFilter(t *testing.T) {
	r := newAgentRegistry(t,
		[2]map[string]string{{"service.name": "alpha-collector"}, nil},
		[2]map[string]string{{"service.name": "beta-gateway"}, nil},
		[2]map[string]string{{"service.name": "alpha-agent"}, nil},
	)
	h := newMux(t, r)

	code, raw := doGetRaw(t, h, "/api/attributes/values?key=service.name&prefix=alpha")
	if code != http.StatusOK {
		t.Fatalf("status = %d body=%s", code, raw)
	}
	var body struct {
		Key    string   `json:"key"`
		Values []string `json:"values"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Key != "service.name" {
		t.Errorf("key = %q", body.Key)
	}
	want := []string{"alpha-agent", "alpha-collector"}
	if !slices.Equal(body.Values, want) {
		t.Errorf("values = %v, want %v", body.Values, want)
	}

	// Empty prefix returns all values.
	code, raw = doGetRaw(t, h, "/api/attributes/values?key="+url.QueryEscape("service.name"))
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Values) != 3 {
		t.Errorf("all values = %v, want 3", body.Values)
	}
}

func TestAttributeKeysEmptyFleet(t *testing.T) {
	t.Parallel()
	keys := AttributeKeys(nil)
	if len(keys) != 0 {
		t.Fatalf("keys = %v", keys)
	}
}
