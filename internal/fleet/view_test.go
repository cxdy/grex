package fleet

import "testing"

func TestRoleOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a    Agent
		want string
	}{
		{
			name: "service.component wins",
			a: Agent{
				Identifying: map[string]string{
					"service.component": "agent",
					"service.name":      "my-gateway",
				},
			},
			want: "agent",
		},
		{
			name: "gateway name heuristic",
			a: Agent{
				Identifying: map[string]string{"service.name": "otelcol-gateway"},
			},
			want: "Gateway",
		},
		{
			name: "default collector",
			a: Agent{
				Identifying: map[string]string{"service.name": "otelcol-contrib"},
			},
			want: "Collector",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RoleOf(tc.a); got != tc.want {
				t.Errorf("RoleOf = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSupervisorManaged(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a    Agent
		want bool
	}{
		{
			name: "declared attribute present",
			a:    Agent{NonIdentifying: map[string]string{"opamp.managed_by": "opentelemetry-opampsupervisor"}},
			want: true,
		},
		{
			name: "attribute absent (bare opamp extension)",
			a:    Agent{},
			want: false,
		},
		{
			name: "unrelated value doesn't count",
			a:    Agent{NonIdentifying: map[string]string{"opamp.managed_by": "something-else"}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SupervisorManaged(tc.a); got != tc.want {
				t.Errorf("SupervisorManaged = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDisplayNameOf(t *testing.T) {
	t.Parallel()
	a := Agent{
		InstanceUID:    "uid-1",
		Identifying:    map[string]string{"service.name": "otelcol"},
		NonIdentifying: map[string]string{"host.name": "host-a"},
	}
	if got := DisplayNameOf(a); got != "otelcol" {
		t.Errorf("DisplayNameOf = %q, want otelcol", got)
	}
	a.Identifying = nil
	if got := DisplayNameOf(a); got != "host-a" {
		t.Errorf("DisplayNameOf = %q, want host-a", got)
	}
	a.NonIdentifying = nil
	if got := DisplayNameOf(a); got != "uid-1" {
		t.Errorf("DisplayNameOf = %q, want uid-1", got)
	}
}

func TestSummaryViewOmitsBulkyFields(t *testing.T) {
	t.Parallel()
	a := Agent{
		InstanceUID:     "uid-1",
		Identifying:     map[string]string{"service.name": "x", "service.version": "1.0"},
		EffectiveConfig: map[string]string{"": "receivers: {}"},
		Packages:        map[string]Package{"p": {Name: "p"}},
	}
	v := SummaryView(a)
	if v.EffectiveConfig != nil {
		t.Errorf("EffectiveConfig = %v, want nil", v.EffectiveConfig)
	}
	if v.Packages != nil {
		t.Errorf("Packages = %v, want nil", v.Packages)
	}
	if v.DisplayName != "x" || v.Version != "1.0" || v.Role != "Collector" {
		t.Errorf("helpers = name=%q ver=%q role=%q", v.DisplayName, v.Version, v.Role)
	}
}

func TestDetailViewIncludesBulkyFields(t *testing.T) {
	t.Parallel()
	a := Agent{
		InstanceUID:     "uid-detail",
		Identifying:     map[string]string{"service.name": "otel", "service.version": "0.9"},
		NonIdentifying:  map[string]string{"host.name": "node-1"},
		EffectiveConfig: map[string]string{"": "receivers:\n  otlp: {}"},
		Packages:        map[string]Package{"core": {Name: "core", AgentHasVersion: "1"}},
		Healthy:         true,
		HealthReported:  true,
		Connected:       true,
	}
	v := DetailView(a)
	if v.EffectiveConfig[""] == "" {
		t.Error("DetailView should include effective config")
	}
	if v.Packages["core"].Name != "core" {
		t.Errorf("Packages = %v", v.Packages)
	}
	if v.DisplayName != "otel" || v.HostName != "node-1" || v.Version != "0.9" {
		t.Errorf("helpers name=%q host=%q ver=%q", v.DisplayName, v.HostName, v.Version)
	}
}

func TestAttr(t *testing.T) {
	t.Parallel()
	a := Agent{
		Identifying:    map[string]string{"service.name": "from-id"},
		NonIdentifying: map[string]string{"host.name": "from-ni"},
	}
	if Attr(a, "service.name") != "from-id" {
		t.Fatal("identifying")
	}
	if Attr(a, "host.name") != "from-ni" {
		t.Fatal("non-identifying")
	}
	if Attr(a, "missing") != "" {
		t.Fatal("missing")
	}
}
