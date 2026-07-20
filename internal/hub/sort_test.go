package hub

import (
	"testing"

	"github.com/pablocolson/k8shark/pkg/api"
)

func elapsed(ms int64) *api.Entry {
	return &api.Entry{ElapsedMs: ms}
}

func entryElapsed(entries []*api.Entry) []int64 {
	out := make([]int64, len(entries))
	for i, e := range entries {
		out[i] = e.ElapsedMs
	}
	return out
}

func TestTopNBySortDescLimitsAndOrders(t *testing.T) {
	entries := []*api.Entry{elapsed(10), elapsed(50), elapsed(5), elapsed(90), elapsed(30)}
	got := topNBySort(entries, fieldGetter("elapsedMs"), true, 3)
	want := []int64{90, 50, 30}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", entryElapsed(got), want)
	}
	for i, w := range want {
		if got[i].ElapsedMs != w {
			t.Errorf("desc top-3 = %v, want %v", entryElapsed(got), want)
			break
		}
	}
}

func TestTopNBySortAscLimitsAndOrders(t *testing.T) {
	entries := []*api.Entry{elapsed(10), elapsed(50), elapsed(5), elapsed(90), elapsed(30)}
	got := topNBySort(entries, fieldGetter("elapsedMs"), false, 3)
	want := []int64{5, 10, 30}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", entryElapsed(got), want)
	}
	for i, w := range want {
		if got[i].ElapsedMs != w {
			t.Errorf("asc bottom-3 = %v, want %v", entryElapsed(got), want)
			break
		}
	}
}

// A limit at or beyond the input size returns everything, still ordered.
func TestTopNBySortLimitBeyondInputSize(t *testing.T) {
	entries := []*api.Entry{elapsed(3), elapsed(1), elapsed(2)}
	got := topNBySort(entries, fieldGetter("elapsedMs"), true, 10)
	want := []int64{3, 2, 1}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", entryElapsed(got), want)
	}
	for i, w := range want {
		if got[i].ElapsedMs != w {
			t.Errorf("got %v, want %v", entryElapsed(got), want)
			break
		}
	}
}

// An entry missing the sorted field (l4.rttms is "" without L4) is excluded
// rather than sorted as if it were zero.
func TestTopNBySortExcludesNonNumeric(t *testing.T) {
	entries := []*api.Entry{{ElapsedMs: 1}, {ElapsedMs: 2}}
	got := topNBySort(entries, fieldGetter("l4.rttms"), true, 10)
	if len(got) != 0 {
		t.Errorf("got %d entries, want 0 (neither entry has L4)", len(got))
	}
}

func TestValidSortField(t *testing.T) {
	if !validSortField("elapsedMs") {
		t.Error("elapsedMs should be a valid (numeric) sort field")
	}
	if validSortField("protocol") {
		t.Error("protocol is enum/string, should not be a valid sort field")
	}
	if validSortField("nonexistent.field") {
		t.Error("unknown field should not be a valid sort field")
	}
}
