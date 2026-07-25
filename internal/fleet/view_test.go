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
