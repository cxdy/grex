package api

import (
	"container/heap"
	"slices"

	"github.com/dennisme/grex/internal/fleet"
)

// pageByInstanceUID returns agents[start:end] as if agents were fully
// sorted ascending by InstanceUID, without sorting the whole slice first.
// GET /api/agents pages a fixed-size window (limit capped at maxLimit) out
// of a matched set that can be far larger — sorting all of it just to
// return the first page is O(N log N) work wasted on rows the caller never
// sees, see docs/spec/design.md's Scaling gaps section. This is O(N log
// end) instead: a bounded max-heap of size end keeps the end smallest
// elements seen so far, then only those get sorted before slicing.
//
// Falls back to a plain full sort when end is not meaningfully smaller than
// len(agents) — the heap round trip buys nothing once the "window" is most
// of the data anyway, and a plain sort is simpler and just as fast there.
func pageByInstanceUID(agents []fleet.Agent, start, end int) []fleet.Agent {
	if end == 0 {
		return nil
	}
	if end >= len(agents) {
		sorted := slices.Clone(agents)
		sortByInstanceUID(sorted)
		return sorted[start:end]
	}

	h := make(agentMaxHeapByInstanceUID, 0, end)
	for _, a := range agents {
		if h.Len() < end {
			heap.Push(&h, a)
			continue
		}
		if a.InstanceUID < h[0].InstanceUID {
			h[0] = a
			heap.Fix(&h, 0)
		}
	}
	page := []fleet.Agent(h)
	sortByInstanceUID(page)
	return page[start:end]
}

func sortByInstanceUID(agents []fleet.Agent) {
	slices.SortFunc(agents, func(a, b fleet.Agent) int {
		if a.InstanceUID < b.InstanceUID {
			return -1
		}
		if a.InstanceUID > b.InstanceUID {
			return 1
		}
		return 0
	})
}

// agentMaxHeapByInstanceUID is a max-heap on InstanceUID: container/heap's
// standard "keep the k smallest" shape, where the root is always the
// largest of the k elements kept so far, cheap to evict when a smaller
// candidate arrives.
type agentMaxHeapByInstanceUID []fleet.Agent

func (h agentMaxHeapByInstanceUID) Len() int           { return len(h) }
func (h agentMaxHeapByInstanceUID) Less(i, j int) bool { return h[i].InstanceUID > h[j].InstanceUID }
func (h agentMaxHeapByInstanceUID) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *agentMaxHeapByInstanceUID) Push(x any)        { *h = append(*h, x.(fleet.Agent)) }
func (h *agentMaxHeapByInstanceUID) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}
