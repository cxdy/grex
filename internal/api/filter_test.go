package api

import (
	"net/url"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/fleet"
)

func TestAttrMatcherString(t *testing.T) {
	t.Parallel()
	m := AttrMatcher{Key: "service.name", Op: OpEqual, Value: "x"}
	if m.String() != "service.name=x" {
		t.Fatalf("String = %q", m.String())
	}
}

func TestFiltersMatchersCopy(t *testing.T) {
	t.Parallel()
	var empty Filters
	if empty.Matchers() != nil {
		t.Fatal("empty Matchers should be nil")
	}
	f, err := ParseFilters(url.Values{"match": {"a=b", "c!=d"}})
	if err != nil {
		t.Fatal(err)
	}
	got := f.Matchers()
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	got[0].Key = "mutated"
	if f.Matchers()[0].Key == "mutated" {
		t.Fatal("Matchers should return a copy")
	}
}

func TestCompileMatcherREAnchors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		value   string
		match   bool
	}{
		{"", "", true},
		{"", "x", false},
		{"^foo$", "foo", true},
		{`\Afoo\z`, "foo", true},
		// End-only: wrapped as ^(?:foo$) → full-string match of foo.
		{"foo$", "foo", true},
		{"foo$", "xfoo", false},
		// Start-only: wrapped as (?:^foo)$ → full-string match of foo.
		{"^foo", "foo", true},
		{"^foo", "foobar", false},
		// Neither: full-value anchor around pattern.
		{"bar", "bar", true},
		{"bar", "xbarx", false},
		{"9.+", "9abc", true},
		{"9.+", "a9bc", false},
	}
	for _, tc := range cases {
		re, err := compileMatcherRE(tc.pattern)
		if err != nil {
			t.Fatalf("pattern %q: %v", tc.pattern, err)
		}
		if got := re.MatchString(tc.value); got != tc.match {
			t.Errorf("pattern %q value %q: match=%v want %v", tc.pattern, tc.value, got, tc.match)
		}
	}
}

func TestMatchAttrOperators(t *testing.T) {
	t.Parallel()
	agent := fleet.Agent{Identifying: map[string]string{"k": "v"}}
	absent := fleet.Agent{}

	eq, _ := ParseMatcher("k=v")
	if !matchAttr(agent, eq) || matchAttr(absent, eq) {
		t.Error("=")
	}
	neq, _ := ParseMatcher("k!=other")
	if !matchAttr(agent, neq) || !matchAttr(absent, neq) {
		t.Error("!=")
	}
	re, _ := ParseMatcher("k=~v.*")
	if !matchAttr(agent, re) || matchAttr(absent, re) {
		t.Error("=~")
	}
	// Non-identifying path.
	ni := fleet.Agent{NonIdentifying: map[string]string{"env": "prod"}}
	eq2, _ := ParseMatcher("env=prod")
	if !matchAttr(ni, eq2) {
		t.Error("non-identifying =")
	}
	// Unknown op falls through to false.
	if matchAttr(agent, AttrMatcher{Key: "k", Op: "??", Value: "v"}) {
		t.Error("unknown op should not match")
	}
	// Regex with nil re.
	if matchAttr(agent, AttrMatcher{Key: "k", Op: OpRegex, Value: "v", re: nil}) {
		t.Error("nil re =~")
	}
	if !matchAttr(agent, AttrMatcher{Key: "k", Op: OpNotRegex, Value: "v", re: nil}) {
		t.Error("nil re !~ should match (Prometheus absent-or-not)")
	}
}

func TestParseMatcherErrors(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"", "noperator", "=novaluekey", "k=~["} {
		if _, err := ParseMatcher(s); err == nil {
			t.Errorf("ParseMatcher(%q) expected error", s)
		}
	}
}

func TestParseMatcherQuotedRegex(t *testing.T) {
	t.Parallel()
	m, err := ParseMatcher(`service.instance.id !~ "9.+"`)
	if err != nil {
		t.Fatal(err)
	}
	if m.Key != "service.instance.id" || m.Op != OpNotRegex || m.Value != "9.+" {
		t.Fatalf("got key=%q op=%q value=%q", m.Key, m.Op, m.Value)
	}
	if m.re == nil || !m.re.MatchString("9abc-def") {
		t.Error("expected 9abc-def to match pattern 9.+")
	}
	if m.re.MatchString("a7af-def") {
		t.Error("expected a7af-def not to match 9.+")
	}
}

func TestMatchAttrNotRegex(t *testing.T) {
	t.Parallel()
	m, err := ParseMatcher(`service.instance.id !~ "9.+"`)
	if err != nil {
		t.Fatal(err)
	}
	keep := fleet.Agent{Identifying: map[string]string{"service.instance.id": "a7af-xxx"}}
	drop := fleet.Agent{Identifying: map[string]string{"service.instance.id": "9abc-xxx"}}
	if !matchAttr(keep, m) {
		t.Error("keep agent should match !~ 9.+")
	}
	if matchAttr(drop, m) {
		t.Error("drop agent should not match !~ 9.+")
	}
}

func TestMergeAgentsAddsDBOnlyAgents(t *testing.T) {
	t.Parallel()
	local := []fleet.Agent{{InstanceUID: "local-1"}}
	db := []fleet.Agent{{InstanceUID: "db-1"}}

	merged := MergeAgents(local, db)

	if len(merged) != 2 {
		t.Fatalf("len(merged) = %d, want 2", len(merged))
	}
}

func TestMergeAgentsLocalWinsOnOverlap(t *testing.T) {
	t.Parallel()
	local := []fleet.Agent{{InstanceUID: "agent-1", HealthStatus: "local"}}
	db := []fleet.Agent{{InstanceUID: "agent-1", HealthStatus: "stale-from-db"}}

	merged := MergeAgents(local, db)

	if len(merged) != 1 {
		t.Fatalf("len(merged) = %d, want 1 (no duplicate for overlapping instance_uid)", len(merged))
	}
	if merged[0].HealthStatus != "local" {
		t.Errorf("HealthStatus = %q, want local registry's value to win", merged[0].HealthStatus)
	}
}

func TestMergeAgentsExcludesEvictedDBAgents(t *testing.T) {
	t.Parallel()
	evictedAt := time.Now()
	local := []fleet.Agent{{InstanceUID: "local-1"}}
	db := []fleet.Agent{{InstanceUID: "db-1", EvictedAt: &evictedAt}}

	merged := MergeAgents(local, db)

	if len(merged) != 1 {
		t.Fatalf("len(merged) = %d, want 1 (soft-deleted db-only agent excluded)", len(merged))
	}
	if merged[0].InstanceUID != "local-1" {
		t.Errorf("merged = %v, want only local-1", merged)
	}
}

func TestUnquoteMatcherValue(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{`"9.+"`, "9.+"},
		{`'9.+'`, "9.+"},
		{`9.+`, "9.+"},
		{`  "x"  `, "x"},
	}
	for _, tc := range cases {
		if got := unquoteMatcherValue(tc.in); got != tc.want {
			t.Errorf("unquote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
