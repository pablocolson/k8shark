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
