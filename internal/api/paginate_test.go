package api

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"github.com/dennisme/grex/internal/fleet"
)

// referencePage is the naive full-sort-then-slice implementation
// pageByInstanceUID must match exactly, just without sorting the whole
// slice when only a small window of it is needed.
func referencePage(agents []fleet.Agent, start, end int) []fleet.Agent {
	sorted := slices.Clone(agents)
	sortByInstanceUID(sorted)
	return sorted[start:end]
}

func agentsWithUIDs(uids ...string) []fleet.Agent {
	agents := make([]fleet.Agent, len(uids))
	for i, u := range uids {
		agents[i] = fleet.Agent{InstanceUID: u}
	}
	return agents
}

func TestPageByInstanceUIDSmallWindow(t *testing.T) {
	agents := agentsWithUIDs("c", "a", "e", "b", "d")
	got := pageByInstanceUID(agents, 1, 3)
	want := referencePage(agents, 1, 3)
	assertSameUIDOrder(t, got, want)
}

func TestPageByInstanceUIDFullSortFallback(t *testing.T) {
	// end >= len(agents): the "no smaller window to select" branch.
	agents := agentsWithUIDs("c", "a", "b")
	got := pageByInstanceUID(agents, 0, 3)
	want := referencePage(agents, 0, 3)
	assertSameUIDOrder(t, got, want)
}

func TestPageByInstanceUIDEmptyWindow(t *testing.T) {
	agents := agentsWithUIDs("a", "b")
	got := pageByInstanceUID(agents, 2, 2) // offset == total
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestPageByInstanceUIDEmptyInput(t *testing.T) {
	got := pageByInstanceUID(nil, 0, 0)
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

// TestPageByInstanceUIDMatchesFullSort is the real correctness guarantee:
// across many random fleets and windows, pageByInstanceUID's output must be
// byte-for-byte identical to sorting the entire slice and taking the same
// slice window, since it's a performance-only change (O(N log end) instead
// of O(N log N), see docs/spec/design.md's Scaling gaps section) — the API
// contract (exact page contents/order) does not change.
func TestPageByInstanceUIDMatchesFullSort(t *testing.T) {
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // test-only fixture generation, not a security context
	for trial := range 50 {
		n := rng.Intn(200)
		uids := make([]string, n)
		for i := range uids {
			uids[i] = fmt.Sprintf("agent-%04d", rng.Intn(1000))
		}
		agents := agentsWithUIDs(uids...)

		limit := rng.Intn(20) + 1
		offset := rng.Intn(n + 5)
		start := min(offset, n)
		end := min(start+limit, n)

		got := pageByInstanceUID(agents, start, end)
		want := referencePage(agents, start, end)
		if len(got) != len(want) {
			t.Fatalf("trial %d: len(got)=%d, len(want)=%d", trial, len(got), len(want))
		}
		for i := range want {
			if got[i].InstanceUID != want[i].InstanceUID {
				t.Fatalf("trial %d: got[%d]=%s, want[%d]=%s (n=%d start=%d end=%d)",
					trial, i, got[i].InstanceUID, i, want[i].InstanceUID, n, start, end)
			}
		}
	}
}

func assertSameUIDOrder(t *testing.T, got, want []fleet.Agent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len(got)=%d, len(want)=%d", len(got), len(want))
	}
	for i := range want {
		if got[i].InstanceUID != want[i].InstanceUID {
			t.Errorf("got[%d]=%s, want[%d]=%s", i, got[i].InstanceUID, i, want[i].InstanceUID)
		}
	}
}
