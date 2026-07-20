package hub

import (
	"container/heap"
	"strconv"

	"github.com/pablocolson/k8shark/pkg/api"
)

// validSortField reports whether field is usable with ?sort=: a known
// numeric IFL field (fieldCatalog's FieldSpec.Type == FieldTypeNumber), so
// the heap comparison in topNBySort below is a well-defined numeric compare
// rather than a lexicographic string one.
func validSortField(field string) bool {
	spec, ok := fieldSpecByName[field]
	return ok && spec.Type == FieldTypeNumber
}

// topNBySort returns up to limit of entries ordered by the numeric value of
// get, best-first (largest first for desc, smallest first for asc). Entries
// where get returns "" or a non-numeric value are excluded rather than
// distorting the ordering. get must be non-nil and numeric (see
// validSortField) -- the caller validates the field before resolving it.
//
// Uses a heap bounded to limit -- O(n log limit) over the full matched set
// instead of copying and fully sorting it, which matters once limit is small
// relative to the buffer (e.g. "20 slowest requests" out of a 10000-entry
// ring).
func topNBySort(entries []*api.Entry, get func(*api.Entry) string, desc bool, limit int) []*api.Entry {
	h := &sortHeap{desc: desc}
	for _, e := range entries {
		v, err := strconv.ParseFloat(get(e), 64)
		if err != nil {
			continue
		}
		switch {
		case h.Len() < limit:
			heap.Push(h, sortedEntry{e, v})
		case (desc && v > h.items[0].val) || (!desc && v < h.items[0].val):
			// h.items[0] is the current worst of the kept set (smallest for
			// desc, largest for asc); replace it only if v is better.
			h.items[0] = sortedEntry{e, v}
			heap.Fix(h, 0)
		}
	}
	out := make([]*api.Entry, h.Len())
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = heap.Pop(h).(sortedEntry).e
	}
	return out
}

// sortedEntry pairs an entry with its already-parsed sort value, so
// topNBySort never re-parses during heap comparisons.
type sortedEntry struct {
	e   *api.Entry
	val float64
}

// sortHeap is a container/heap, bounded to `limit` by topNBySort (not
// enforced here), that keeps the *worst* of its currently-held set at the
// root so it's cheap to test/evict on each new candidate. desc=true keeps
// the largest values seen (a min-heap: root is the smallest of the kept set,
// evicted first); desc=false keeps the smallest values seen (a max-heap:
// root is the largest of the kept set, evicted first).
type sortHeap struct {
	items []sortedEntry
	desc  bool
}

func (h sortHeap) Len() int { return len(h.items) }
func (h sortHeap) Less(i, j int) bool {
	if h.desc {
		return h.items[i].val < h.items[j].val
	}
	return h.items[i].val > h.items[j].val
}
func (h sortHeap) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *sortHeap) Push(x any)   { h.items = append(h.items, x.(sortedEntry)) }
func (h *sortHeap) Pop() any {
	old := h.items
	n := len(old)
	item := old[n-1]
	h.items = old[:n-1]
	return item
}
