package api

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/dennisme/grex/internal/fleet"
)

// reservedParams are pagination (and UI-only) controls, never treated as
// filters even if an agent happens to report an attribute with the same key.
var reservedParams = map[string]bool{
	"limit":      true,
	"offset":     true,
	"sort":       true, // UI fleet table column sort
	"order":      true, // asc | desc
	"match":      true, // Prometheus-style attribute matchers (repeatable)
	"attr_key":   true, // legacy UI freeform fields
	"attr_value": true,
}

// boolFields are well-known top-level Agent fields filterable as
// ?key=true|false. These take precedence over AgentDescription attribute
// filtering for the same key: an agent-reported attribute literally named
// "healthy" (unusual, but attribute keys are arbitrary) cannot be filtered
// on since the top-level field always wins. The key set here must match
// fleet.ReservedAttributeKeys exactly (see handler_test.go); that list is
// what the registry uses to warn and count when an agent's own attributes
// collide with these names.
var boolFields = map[string]func(fleet.Agent) bool{
	"healthy":     func(a fleet.Agent) bool { return a.Healthy },
	"connected":   func(a fleet.Agent) bool { return a.Connected },
	"via_gateway": func(a fleet.Agent) bool { return a.Conn.ViaGateway },
}

// Matcher operators mirror Prometheus label matchers.
const (
	OpEqual    = "="
	OpNotEqual = "!="
	OpRegex    = "=~"
	OpNotRegex = "!~"
)

// AttrMatcher is one attribute constraint (Prometheus-style).
type AttrMatcher struct {
	Key   string
	Op    string // =, !=, =~, !~
	Value string
	re    *regexp.Regexp // compiled for =~ / !~
}

// String formats the matcher as key<op>value (no spaces).
func (m AttrMatcher) String() string {
	return m.Key + m.Op + m.Value
}

// Filters holds parsed query filters: attribute matchers and well-known
// top-level boolean fields.
type Filters struct {
	matchers []AttrMatcher
	bools    map[string]bool
}

// Empty reports whether no filters are set.
func (f Filters) Empty() bool { return len(f.matchers) == 0 && len(f.bools) == 0 }

// Matchers returns a copy of the attribute matchers.
func (f Filters) Matchers() []AttrMatcher {
	if len(f.matchers) == 0 {
		return nil
	}
	out := make([]AttrMatcher, len(f.matchers))
	copy(out, f.matchers)
	return out
}

// matcherRE splits key, operator, value with optional surrounding spaces.
// Longer operators (!~, =~, !=) are tried before =.
var matcherRE = regexp.MustCompile(`^(.+?)\s*(!~|=~|!=|=)\s*(.*)$`)

// ParseMatcher parses a Prometheus-style attribute matcher string.
// Regex values may be wrapped in single or double quotes (stripped before
// compile), e.g. service.instance.id !~ "9.+". Patterns use Go's RE2 engine
// (same family Prometheus uses; common PCRE syntax works for typical filters).
// Matching is against the full attribute value (anchored), so "9.+" matches
// values that start with 9 and have at least one more character.
func ParseMatcher(s string) (AttrMatcher, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return AttrMatcher{}, fmt.Errorf("empty matcher")
	}
	parts := matcherRE.FindStringSubmatch(s)
	if parts == nil {
		return AttrMatcher{}, fmt.Errorf("invalid matcher %q: want key=value, key!=value, key=~regex, or key!~regex", s)
	}
	m := AttrMatcher{
		Key:   strings.TrimSpace(parts[1]),
		Op:    parts[2],
		Value: unquoteMatcherValue(parts[3]),
	}
	if m.Key == "" {
		return AttrMatcher{}, fmt.Errorf("matcher missing attribute key")
	}
	if m.Op == OpRegex || m.Op == OpNotRegex {
		re, err := compileMatcherRE(m.Value)
		if err != nil {
			return AttrMatcher{}, fmt.Errorf("invalid regex in matcher %q: %w", s, err)
		}
		m.re = re
	}
	return m, nil
}

// unquoteMatcherValue strips optional surrounding ' or " quotes and trims
// space, so Grafana/Prom-style quoted patterns work.
func unquoteMatcherValue(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// compileMatcherRE compiles a full-value regex. Anchors are applied unless
// the pattern already provides them, so "9.+" matches values that entirely
// match 9.+ (e.g. start with 9), not a substring search mid-value.
func compileMatcherRE(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return regexp.Compile("^$")
	}
	hasStart := strings.HasPrefix(pattern, "^") || strings.HasPrefix(pattern, `\A`)
	hasEnd := strings.HasSuffix(pattern, "$") || strings.HasSuffix(pattern, `\z`)
	switch {
	case hasStart && hasEnd:
		return regexp.Compile(pattern)
	case !hasStart && !hasEnd:
		return regexp.Compile("^(?:" + pattern + ")$")
	case !hasStart:
		return regexp.Compile("^(?:" + pattern + ")")
	default:
		return regexp.Compile("(?:" + pattern + ")$")
	}
}

// ParseFilters turns query params into filters.
//
//   - Well-known bools: healthy, connected, via_gateway (true/false).
//   - Bare attribute params: ?service.name=foo → exact match (back-compat).
//   - match= (repeatable): Prometheus-style key=value | key!=value |
//     key=~regex | key!~regex. Spaces around the operator are allowed.
//   - Legacy attr_key + attr_value → exact match on that attribute.
//
// Multiple matchers are ANDed. Blank values are ignored (HTML "Any" selects).
func ParseFilters(q url.Values) (Filters, error) {
	f := Filters{bools: make(map[string]bool)}
	for key, values := range q {
		if reservedParams[key] || len(values) == 0 {
			continue
		}
		val := strings.TrimSpace(values[0])
		if val == "" {
			continue
		}
		if _, ok := boolFields[key]; ok {
			b, err := strconv.ParseBool(val)
			if err != nil {
				return Filters{}, fmt.Errorf("%s must be a boolean (true or false)", key)
			}
			f.bools[key] = b
			continue
		}
		// Bare query key=value is exact attribute match (upstream API).
		m, err := ParseMatcher(key + OpEqual + val)
		if err != nil {
			return Filters{}, err
		}
		f.matchers = append(f.matchers, m)
	}

	for _, raw := range q["match"] {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		m, err := ParseMatcher(raw)
		if err != nil {
			return Filters{}, err
		}
		f.matchers = append(f.matchers, m)
	}

	// Legacy freeform attr form
	if key := strings.TrimSpace(q.Get("attr_key")); key != "" {
		m, err := ParseMatcher(key + OpEqual + q.Get("attr_value"))
		if err != nil {
			return Filters{}, err
		}
		f.matchers = append(f.matchers, m)
	}
	return f, nil
}

// MatchingAgents returns agents satisfying every filter.
func MatchingAgents(agents []fleet.Agent, f Filters) []fleet.Agent {
	if f.Empty() {
		return agents
	}
	matched := make([]fleet.Agent, 0, len(agents))
	for _, agent := range agents {
		if AgentMatches(agent, f) {
			matched = append(matched, agent)
		}
	}
	return matched
}

// AgentMatches reports whether agent satisfies every filter.
func AgentMatches(agent fleet.Agent, f Filters) bool {
	for _, m := range f.matchers {
		if !matchAttr(agent, m) {
			return false
		}
	}
	for key, want := range f.bools {
		if boolFields[key](agent) != want {
			return false
		}
	}
	return true
}

func matchAttr(agent fleet.Agent, m AttrMatcher) bool {
	got, ok := attrValue(agent, m.Key)
	switch m.Op {
	case OpEqual:
		return ok && got == m.Value
	case OpNotEqual:
		// Prometheus: absent label still matches !=
		return !ok || got != m.Value
	case OpRegex:
		return ok && m.re != nil && m.re.MatchString(got)
	case OpNotRegex:
		// Prometheus: absent label still matches !~
		return !ok || m.re == nil || !m.re.MatchString(got)
	default:
		return false
	}
}

func attrValue(agent fleet.Agent, key string) (string, bool) {
	if v, ok := agent.Identifying[key]; ok {
		return v, true
	}
	if v, ok := agent.NonIdentifying[key]; ok {
		return v, true
	}
	return "", false
}
