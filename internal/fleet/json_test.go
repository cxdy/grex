package fleet

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
)

func TestAgentMarshalJSON(t *testing.T) {
	r, _ := testRegistry()
	uid := testUID()
	meta := ConnMeta{RemoteAddr: "10.0.0.5:1234", ViaGateway: true, Transport: "ws"}
	r.Report(statusMsg(uid), meta)
	r.Report(&protobufs.AgentToServer{
		InstanceUid: uid[:],
		Capabilities: uint64(protobufs.AgentCapabilities_AgentCapabilities_ReportsStatus) |
			uint64(protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth),
		Health: &protobufs.ComponentHealth{Healthy: true, Status: "ok"},
		PackageStatuses: &protobufs.PackageStatuses{
			Packages: map[string]*protobufs.PackageStatus{
				"otelcol": {Name: "otelcol", AgentHasVersion: "0.157.0"},
			},
		},
	}, meta)
	agent, ok := r.Get(uid.String())
	if !ok {
		t.Fatal("agent missing")
	}

	data, err := json.Marshal(agent)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if out["instance_uid"] != uid.String() {
		t.Errorf("instance_uid = %v", out["instance_uid"])
	}
	identifying, _ := out["identifying_attributes"].(map[string]any)
	if identifying["service.name"] != "otelcol-contrib" {
		t.Errorf("identifying_attributes = %v", out["identifying_attributes"])
	}
	conn, _ := out["connection"].(map[string]any)
	if conn["via_gateway"] != true || conn["transport"] != "ws" {
		t.Errorf("connection = %v", out["connection"])
	}
	if out["healthy"] != true || out["health_status"] != "ok" {
		t.Errorf("health fields = healthy:%v health_status:%v", out["healthy"], out["health_status"])
	}

	// The raw bitmask is present, decoded flags are present alongside it,
	// not instead of it.
	if out["capabilities"] == nil {
		t.Error("raw capabilities bitmask missing from JSON")
	}
	flags, _ := out["capability_flags"].(map[string]any)
	if flags["reports_status"] != true || flags["reports_health"] != true {
		t.Errorf("capability_flags = %v, want reports_status and reports_health true", flags)
	}
	if flags["accepts_packages"] != false {
		t.Errorf("capability_flags.accepts_packages = %v, want false", flags["accepts_packages"])
	}

	pkgs, _ := out["packages"].(map[string]any)
	otelcol, _ := pkgs["otelcol"].(map[string]any)
	if otelcol["agent_has_version"] != "0.157.0" {
		t.Errorf("packages.otelcol = %v", otelcol)
	}

	if out["effective_config"] != nil {
		t.Errorf("effective_config = %v, want omitted when never reported", out["effective_config"])
	}
}

func TestAgentMarshalJSONRoundTripsThroughList(t *testing.T) {
	r, _ := testRegistry()
	r.Report(statusMsg(testUID()), ConnMeta{})
	r.Report(statusMsg(testUID()), ConnMeta{})

	data, err := json.Marshal(r.List())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
}

func TestAgentJSONTimestampsRFC3339(t *testing.T) {
	r, _ := testRegistry()
	uid := testUID()
	r.Report(statusMsg(uid), ConnMeta{})
	agent, _ := r.Get(uid.String())

	data, err := json.Marshal(agent)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, err := time.Parse(time.RFC3339, out["first_seen"].(string)); err != nil {
		t.Errorf("first_seen not RFC3339: %v (%v)", out["first_seen"], err)
	}
	if _, err := time.Parse(time.RFC3339, out["last_seen"].(string)); err != nil {
		t.Errorf("last_seen not RFC3339: %v (%v)", out["last_seen"], err)
	}
}
