package hub

import (
	"fmt"
	"testing"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

func TestStoreGetByID(t *testing.T) {
	s := newStore(4)
	e := &api.Entry{ID: "abc", Protocol: api.ProtocolHTTP, Timestamp: time.Now()}
	s.add(e)

	if got := s.get("abc"); got != e {
		t.Fatalf("get(abc) = %v, want the added entry", got)
	}
	if got := s.get("missing"); got != nil {
		t.Fatalf("get(missing) = %v, want nil", got)
	}

	// Overwrite the whole ring so the first entry is evicted; its id must no
	// longer resolve (the index is kept in sync with the buffer).
	for i := 0; i < 4; i++ {
		s.add(&api.Entry{ID: fmt.Sprintf("e%d", i), Protocol: api.ProtocolHTTP, Timestamp: time.Now()})
	}
	if got := s.get("abc"); got != nil {
		t.Fatalf("get(abc) after eviction = %v, want nil", got)
	}
	if got := s.get("e3"); got == nil || got.ID != "e3" {
		t.Fatalf("get(e3) = %v, want the live entry", got)
	}
}

func TestStoreRecentBefore(t *testing.T) {
	s := newStore(10)
	for i := 0; i < 5; i++ {
		s.add(&api.Entry{ID: fmt.Sprintf("e%d", i), Protocol: api.ProtocolHTTP, Timestamp: time.Now()})
	}
	// Buffer holds e0..e4, newest (e4) first. Paging before e2 should yield
	// e1 then e0 — the entries strictly older than the anchor.
	got := s.recentBefore("e2", 10, nil)
	if len(got) != 2 || got[0].ID != "e1" || got[1].ID != "e0" {
		t.Fatalf("recentBefore(e2) = %v, want [e1 e0]", ids(got))
	}

	// Paging before the oldest entry yields nothing.
	if got := s.recentBefore("e0", 10, nil); len(got) != 0 {
		t.Fatalf("recentBefore(e0) = %v, want empty", ids(got))
	}

	// An anchor that isn't in the buffer (aged out or never existed) is a
	// safe no-op rather than a guess.
	if got := s.recentBefore("missing", 10, nil); len(got) != 0 {
		t.Fatalf("recentBefore(missing) = %v, want empty", ids(got))
	}

	// limit is respected.
	if got := s.recentBefore("e4", 1, nil); len(got) != 1 || got[0].ID != "e3" {
		t.Fatalf("recentBefore(e4, limit=1) = %v, want [e3]", ids(got))
	}

	// match filters the paged results too.
	onlyE0 := func(e *api.Entry) bool { return e.ID == "e0" }
	if got := s.recentBefore("e2", 10, onlyE0); len(got) != 1 || got[0].ID != "e0" {
		t.Fatalf("recentBefore(e2, match=e0) = %v, want [e0]", ids(got))
	}
}

// recentBeforeSeq (HUB-3) offers the same "strictly older than" pagination as
// recentBefore, but by numeric comparison instead of first locating an
// anchor entry by ID -- so it works even for a seq that isn't (or is no
// longer) any live entry's, unlike recentBefore which requires the exact
// anchor ID to still be present in the ring.
func TestStoreRecentBeforeSeq(t *testing.T) {
	s := newStore(10)
	for i := 0; i < 5; i++ {
		s.add(&api.Entry{ID: fmt.Sprintf("e%d", i), Protocol: api.ProtocolHTTP, Timestamp: time.Now()})
	}
	if s.buf[2].Seq != 3 {
		t.Fatalf("e2.Seq = %d, want 3 (1-indexed ingestion order)", s.buf[2].Seq)
	}

	// Paging before e2's seq (3) yields e1, e0 -- same result recentBefore("e2", ...) gives.
	if got := s.recentBeforeSeq(3, 10, nil); len(got) != 2 || got[0].ID != "e1" || got[1].ID != "e0" {
		t.Fatalf("recentBeforeSeq(3) = %v, want [e1 e0]", ids(got))
	}

	// A seq beyond any assigned value still works -- no anchor lookup needed,
	// just a comparison, unlike recentBefore("missing", ...) which must give up.
	if got := s.recentBeforeSeq(1000, 10, nil); len(got) != 5 {
		t.Fatalf("recentBeforeSeq(1000) = %v, want all 5 entries", ids(got))
	}

	// At or below the oldest entry's seq yields nothing.
	if got := s.recentBeforeSeq(1, 10, nil); len(got) != 0 {
		t.Fatalf("recentBeforeSeq(1) = %v, want empty", ids(got))
	}

	// limit is respected.
	if got := s.recentBeforeSeq(5, 1, nil); len(got) != 1 || got[0].ID != "e3" {
		t.Fatalf("recentBeforeSeq(5, limit=1) = %v, want [e3]", ids(got))
	}

	// match filters the paged results too.
	onlyE0 := func(e *api.Entry) bool { return e.ID == "e0" }
	if got := s.recentBeforeSeq(3, 10, onlyE0); len(got) != 1 || got[0].ID != "e0" {
		t.Fatalf("recentBeforeSeq(3, match=e0) = %v, want [e0]", ids(got))
	}
}

func ids(es []*api.Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

// Trailing windows: only entries inside 1m/5m count, and errors are tallied.
func TestStoreWindowStats(t *testing.T) {
	s := newStore(16)
	now := time.Now()
	add := func(id string, age time.Duration, status string) {
		s.add(&api.Entry{ID: id, Protocol: api.ProtocolHTTP, Timestamp: now.Add(-age), Status: status})
	}
	add("old", 10*time.Minute, "error") // outside both windows
	add("w5", 3*time.Minute, "error")   // 5m only
	add("w1a", 30*time.Second, "success")
	add("w1b", 5*time.Second, "error")

	st := s.stats(1)
	if st.Last1m == nil || st.Last5m == nil {
		t.Fatal("windows missing from stats")
	}
	if st.Last1m.Entries != 2 || st.Last1m.Errors != 1 {
		t.Errorf("last1m = %d entries %d errors, want 2/1", st.Last1m.Entries, st.Last1m.Errors)
	}
	if st.Last5m.Entries != 3 || st.Last5m.Errors != 2 {
		t.Errorf("last5m = %d entries %d errors, want 3/2", st.Last5m.Entries, st.Last5m.Errors)
	}
}
