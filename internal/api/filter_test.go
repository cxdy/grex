package api

import (
	"testing"

	"github.com/dennisme/grex/internal/fleet"
)

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
