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
