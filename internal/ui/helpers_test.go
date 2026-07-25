package ui

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dennisme/grex/internal/fleet"
)

func TestParsePagination(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		q          url.Values
		wantLimit  int
		wantOffset int
		wantErr    bool
	}{
		{"defaults", url.Values{}, defaultLimit, 0, false},
		{"valid", url.Values{"limit": {"10"}, "offset": {"5"}}, 10, 5, false},
		{"cap limit", url.Values{"limit": {"99999"}}, maxLimit, 0, false},
		{"bad limit", url.Values{"limit": {"0"}}, 0, 0, true},
		{"neg limit", url.Values{"limit": {"-1"}}, 0, 0, true},
		{"nan limit", url.Values{"limit": {"x"}}, 0, 0, true},
		{"neg offset", url.Values{"offset": {"-1"}}, 0, 0, true},
		{"nan offset", url.Values{"offset": {"x"}}, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lim, off, err := parsePagination(tc.q)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if lim != tc.wantLimit || off != tc.wantOffset {
				t.Fatalf("got limit=%d offset=%d, want %d/%d", lim, off, tc.wantLimit, tc.wantOffset)
			}
		})
	}
}

func TestCollectMatchesAttrKeyAndBare(t *testing.T) {
	t.Parallel()
	q := url.Values{
		"attr_key":     {"team"},
		"attr_value":   {"sre"},
		"service.name": {"otel"},
		"match":        {"host.name=box"},
		"limit":        {"10"}, // reserved, ignored
	}
	got := collectMatches(q)
	joined := strings.Join(got, ",")
	for _, want := range []string{"host.name=box", "team=sre", "service.name=otel"} {
		if !strings.Contains(joined, want) {
			t.Errorf("collectMatches missing %q in %v", want, got)
		}
	}
}

func TestParseSortLastSeenDefaultDesc(t *testing.T) {
	t.Parallel()
	// Selecting last_seen without order defaults to desc.
	key, order := parseSort(url.Values{"sort": {"last_seen"}})
	if key != "last_seen" || order != "desc" {
		t.Fatalf("got %s %s, want last_seen desc", key, order)
	}
	// Explicit asc wins.
	key, order = parseSort(url.Values{"sort": {"last_seen"}, "order": {"asc"}})
	if key != "last_seen" || order != "asc" {
		t.Fatalf("got %s %s, want last_seen asc", key, order)
	}
}

func TestSortAgentsAllColumns(t *testing.T) {
	t.Parallel()
	t1 := time.Unix(100, 0).UTC()
	t2 := time.Unix(200, 0).UTC()
	agents := []fleet.Agent{
		{
			InstanceUID: "b",
			LastSeen:    t1,
			Identifying: map[string]string{
				"service.name":      "zeta",
				"service.component": "agent",
				"service.version":   "2.0",
			},
			Conn:           fleet.ConnMeta{ViaGateway: true, Transport: "ws"},
			Connected:      true,
			HealthReported: true,
			Healthy:        false,
		},
		{
			InstanceUID: "a",
			LastSeen:    t2,
			Identifying: map[string]string{
				"service.name":      "alpha",
				"service.component": "gateway",
				"service.version":   "1.0",
			},
			Conn:           fleet.ConnMeta{ViaGateway: false, Transport: "http"},
			Connected:      true,
			HealthReported: false,
		},
	}

	// Copy for each sort so order is independent.
	for _, col := range []string{"role", "version", "via", "transport", "instance", "status"} {
		cp := append([]fleet.Agent(nil), agents...)
		sortAgents(cp, col, "asc")
		if cp[0].InstanceUID == "" {
			t.Fatalf("sort %s produced empty uid", col)
		}
		// Desc path + tie-break when equal instance compare
		sortAgents(cp, col, "desc")
	}

	// Equal last_seen → stable instance tie-break.
	same := []fleet.Agent{
		{InstanceUID: "z", LastSeen: t1},
		{InstanceUID: "a", LastSeen: t1},
	}
	sortAgents(same, "last_seen", "asc")
	if same[0].InstanceUID != "a" {
		t.Fatalf("tie-break: first=%s", same[0].InstanceUID)
	}

	// Equal last_seen with Before/After neither (same time already covered).
	// statusRank unknown connected
	unknown := []fleet.Agent{
		{InstanceUID: "u", Connected: true, HealthReported: false},
		{InstanceUID: "h", Connected: true, HealthReported: true, Healthy: true},
	}
	sortAgents(unknown, "status", "asc")
	if unknown[0].InstanceUID != "h" {
		t.Fatalf("status healthy first, got %s", unknown[0].InstanceUID)
	}
}

func TestStatusRankUnhealthy(t *testing.T) {
	t.Parallel()
	if statusRank(fleet.Agent{Connected: true, HealthReported: true, Healthy: false}) != "1-unhealthy" {
		t.Fatal("expected unhealthy rank")
	}
	if statusRank(fleet.Agent{Connected: true, HealthReported: false}) != "2-unknown" {
		t.Fatal("expected unknown rank")
	}
}

func TestSortHrefAndAria(t *testing.T) {
	t.Parallel()
	// Empty query → root path.
	if href := sortHref(url.Values{}, "name", "", ""); href != "/?order=asc&sort=name" && !strings.Contains(href, "sort=name") {
		// Accept either encoding order
		u, err := url.Parse(href)
		if err != nil || u.Query().Get("sort") != "name" {
			t.Fatalf("href = %s", href)
		}
	}
	// last_seen defaults next order to desc when not active.
	href := sortHref(url.Values{}, "last_seen", "name", "asc")
	u, err := url.Parse(href)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("order") != "desc" {
		t.Fatalf("last_seen default order = %s", u.Query().Get("order"))
	}
	// Toggle desc → asc when already active.
	href = sortHref(url.Values{"sort": {"name"}, "order": {"desc"}}, "name", "name", "desc")
	u, _ = url.Parse(href)
	if u.Query().Get("order") != "asc" {
		t.Fatalf("toggle to asc: %s", href)
	}
	// Empty encode edge: only possible with empty sort set deleted — covered via Del offset.

	if sortAria("name", "name", "desc") != "descending" {
		t.Fatal("aria descending")
	}
	if sortAria("name", "name", "asc") != "ascending" {
		t.Fatal("aria ascending")
	}
	if sortAria("name", "other", "asc") != "none" {
		t.Fatal("aria none")
	}
	if sortClass("name", "name", "asc") != "th-sort is-active order-asc" {
		t.Fatal("sortClass active")
	}
}

func TestFormatPollShortUIDRelTime(t *testing.T) {
	t.Parallel()
	if formatPoll(0) != "5s" {
		t.Fatalf("formatPoll(0) = %s", formatPoll(0))
	}
	if formatPoll(2*time.Second) != "2s" {
		t.Fatalf("formatPoll(2s) = %s", formatPoll(2*time.Second))
	}
	if shortUID("abc") != "abc" {
		t.Fatal("short uid short")
	}
	if got := shortUID("0123456789abcdef"); !strings.HasPrefix(got, "01234567") || !strings.Contains(got, "…") {
		t.Fatalf("shortUID long = %s", got)
	}

	if relTime(time.Time{}) != "—" {
		t.Fatal("zero relTime")
	}
	now := time.Now()
	if relTime(now.Add(-500*time.Millisecond)) != "just now" {
		t.Fatalf("sub-second: %s", relTime(now.Add(-500*time.Millisecond)))
	}
	if !strings.HasSuffix(relTime(now.Add(-30*time.Second)), "s ago") {
		t.Fatalf("seconds: %s", relTime(now.Add(-30*time.Second)))
	}
	if !strings.HasSuffix(relTime(now.Add(-5*time.Minute)), "m ago") {
		t.Fatalf("minutes: %s", relTime(now.Add(-5*time.Minute)))
	}
	if !strings.HasSuffix(relTime(now.Add(-3*time.Hour)), "h ago") {
		t.Fatalf("hours: %s", relTime(now.Add(-3*time.Hour)))
	}
	if !strings.HasSuffix(relTime(now.Add(-48*time.Hour)), "d ago") {
		t.Fatalf("days: %s", relTime(now.Add(-48*time.Hour)))
	}
	// Future timestamp clamps.
	if relTime(now.Add(2*time.Second)) != "just now" {
		t.Fatalf("future: %s", relTime(now.Add(2*time.Second)))
	}
}

func TestFormatUptime(t *testing.T) {
	t.Parallel()
	if formatUptime(-time.Second) != "0s" {
		t.Fatalf("neg: %s", formatUptime(-time.Second))
	}
	if formatUptime(45*time.Second) != "45s" {
		t.Fatalf("sec: %s", formatUptime(45*time.Second))
	}
	if formatUptime(5*time.Minute+3*time.Second) != "5m 3s" {
		t.Fatalf("min: %s", formatUptime(5*time.Minute+3*time.Second))
	}
	if formatUptime(2*time.Hour+1*time.Minute+4*time.Second) != "2h 1m 4s" {
		t.Fatalf("hour: %s", formatUptime(2*time.Hour+time.Minute+4*time.Second))
	}
}

func TestStatusLabelClassTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		a     fleet.AgentView
		label string
		class string
	}{
		{"disc", fleet.AgentView{Connected: false}, "Disconnected", "badge-disconnected"},
		{"unk", fleet.AgentView{Connected: true, HealthReported: false}, "Unknown", "badge-unknown"},
		{"ok", fleet.AgentView{Connected: true, HealthReported: true, Healthy: true}, "Healthy", "badge-healthy"},
		{"bad", fleet.AgentView{Connected: true, HealthReported: true, Healthy: false}, "Unhealthy", "badge-unhealthy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if statusLabel(tc.a) != tc.label {
				t.Errorf("label = %s", statusLabel(tc.a))
			}
			if statusClass(tc.a) != tc.class {
				t.Errorf("class = %s", statusClass(tc.a))
			}
			_ = statusTitle(tc.a) // exercise all title branches via dedicated cases below
		})
	}

	titles := []fleet.AgentView{
		{Connected: false, HealthReported: false},
		{Connected: false, HealthReported: true, Healthy: true},
		{Connected: false, HealthReported: true, Healthy: false},
		{Connected: true, HealthReported: false},
		{Connected: true, HealthReported: true, Healthy: true},
		{Connected: true, HealthReported: true, Healthy: false},
	}
	for _, a := range titles {
		if statusTitle(a) == "" {
			t.Errorf("empty title for %+v", a)
		}
	}
}

func TestYAMLDisplayEmptyAndGCDNeg(t *testing.T) {
	t.Parallel()
	if yamlDisplay("") != "" {
		t.Fatal("empty yaml")
	}
	// gcd with negative path (defensive).
	if gcd(-6, 9) != 3 {
		t.Fatalf("gcd = %d", gcd(-6, 9))
	}
	if gcd(0, 5) != 5 {
		t.Fatalf("gcd 0,5 = %d", gcd(0, 5))
	}
}

func TestQueryWithAndPollQuery(t *testing.T) {
	t.Parallel()
	q := url.Values{"a": {"1"}, "b": {"2"}}
	got := queryWith(q, "a", "")
	if strings.Contains(got, "a=") {
		t.Fatalf("delete a: %s", got)
	}
	if !strings.HasPrefix(got, "?") {
		t.Fatalf("want leading ?: %s", got)
	}
	got = queryWith(q, "c", "3")
	if !strings.Contains(got, "c=3") {
		t.Fatalf("set c: %s", got)
	}
	// Empty result when all deleted.
	empty := queryWith(url.Values{"x": {"1"}}, "x", "")
	if empty != "" {
		t.Fatalf("empty queryWith = %q", empty)
	}
	if pollQuery(nil) != "" {
		t.Fatal("nil pollQuery")
	}
	if pollQuery(url.Values{}) != "" {
		t.Fatal("empty pollQuery")
	}
	if pollQuery(url.Values{"k": {"v"}}) != "?k=v" {
		t.Fatalf("pollQuery = %s", pollQuery(url.Values{"k": {"v"}}))
	}
}

func TestAttrPairsAndChips(t *testing.T) {
	t.Parallel()
	if attrPairs(nil) != nil {
		t.Fatal("nil pairs")
	}
	pairs := attrPairs(map[string]string{"z": "1", "a": "2", "m": "3"})
	if len(pairs) != 3 || pairs[0][0] != "a" || pairs[1][0] != "m" || pairs[2][0] != "z" {
		t.Fatalf("sorted pairs = %v", pairs)
	}

	if attrChips(fleet.AgentView{}) != nil {
		t.Fatal("empty chips")
	}
	chips := attrChips(fleet.AgentView{
		Identifying:    map[string]string{"service.name": "x", "blank": "  "},
		NonIdentifying: map[string]string{"host.name": "h", "empty": ""},
	})
	// blank values skipped
	for _, c := range chips {
		if strings.TrimSpace(c[1]) == "" {
			t.Fatalf("blank chip: %v", c)
		}
	}
	if len(chips) != 2 {
		t.Fatalf("chips = %v, want 2", chips)
	}
}

func TestPaginationHelpers(t *testing.T) {
	t.Parallel()
	if pageCount(0, 10) != 0 || pageCount(10, 0) != 0 {
		t.Fatal("pageCount zero")
	}
	if pageCount(100, 10) != 10 {
		t.Fatalf("pageCount = %d", pageCount(100, 10))
	}
	if pageCount(11, 10) != 2 {
		t.Fatalf("pageCount ceil = %d", pageCount(11, 10))
	}
	if pageNum(0, 10) != 1 {
		t.Fatal("pageNum first")
	}
	if pageNum(20, 10) != 3 {
		t.Fatalf("pageNum = %d", pageNum(20, 10))
	}
	if pageNum(0, 0) != 1 {
		t.Fatal("pageNum limit 0")
	}
	if pageOffset(0, 10) != 0 {
		t.Fatal("pageOffset page<1")
	}
	if pageOffset(3, 10) != 20 {
		t.Fatalf("pageOffset = %d", pageOffset(3, 10))
	}
	if pageOffset(2, 0) != 0 {
		t.Fatal("pageOffset limit 0")
	}
	if pageList(0, 10) != nil {
		t.Fatal("pageList empty")
	}
	pl := pageList(25, 10)
	if len(pl) != 3 || pl[0] != 1 || pl[2] != 3 {
		t.Fatalf("pageList = %v", pl)
	}
}

func TestDict(t *testing.T) {
	t.Parallel()
	m, err := dict("a", 1, "b", "two")
	if err != nil {
		t.Fatal(err)
	}
	if m["a"] != 1 || m["b"] != "two" {
		t.Fatalf("dict = %v", m)
	}
	if _, err := dict("only"); err == nil {
		t.Fatal("expected odd-args error")
	}
	if _, err := dict(1, "x"); err == nil {
		t.Fatal("expected non-string key error")
	}
}

func TestHasFilterAndViaLabel(t *testing.T) {
	t.Parallel()
	if hasFilter(pageData{}) {
		t.Fatal("empty should not have filter")
	}
	if !hasFilter(pageData{Healthy: "true"}) {
		t.Fatal("healthy filter")
	}
	if viaLabel(true) != "gateway" || viaLabel(false) != "direct" {
		t.Fatal("viaLabel")
	}
}

func TestCloneQuery(t *testing.T) {
	t.Parallel()
	q := url.Values{"a": {"1", "2"}}
	cp := cloneQuery(q)
	cp.Set("a", "x")
	if q.Get("a") != "1" {
		t.Fatal("clone should be independent")
	}
}
