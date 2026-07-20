package hub

import (
	"sync"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

// store is a bounded, thread-safe ring buffer of the most recent entries plus
// rolling aggregate counters. It is the hub's only source of truth for the REST
// API and for replaying history to newly-connected front clients.
type store struct {
	mu       sync.RWMutex
	buf      []*api.Entry
	capacity int
	next     int // write cursor into buf
	full     bool

	// byID indexes live buffer entries for O(1) get(). Kept in sync with buf:
	// an entry is added on write and removed when its slot is overwritten.
	byID map[string]*api.Entry

	total      int64
	byProtocol map[string]int64
	byStatus   map[string]int64

	// rate tracking
	rateWindow []time.Time

	// facets tracks observed field values for IFL autocomplete. It owns its
	// own mutex, fully decoupled from s.mu.
	facets *facetIndex
}

func newStore(capacity int) *store {
	return &store{
		buf:        make([]*api.Entry, capacity),
		capacity:   capacity,
		byID:       map[string]*api.Entry{},
		byProtocol: map[string]int64{},
		byStatus:   map[string]int64{},
		facets:     newFacetIndex(),
	}
}

// add records an entry and updates aggregates.
func (s *store) add(e *api.Entry) {
	s.mu.Lock()

	// Evict the entry currently in this slot from the id index before overwriting
	// it (guarding against a same-id re-add having already replaced the mapping).
	if old := s.buf[s.next]; old != nil && s.byID[old.ID] == old {
		delete(s.byID, old.ID)
	}
	s.buf[s.next] = e
	s.byID[e.ID] = e
	s.next = (s.next + 1) % s.capacity
	if s.next == 0 {
		s.full = true
	}

	s.total++
	e.Seq = s.total
	s.byProtocol[string(e.Protocol)]++
	if e.Status != "" {
		s.byStatus[e.Status]++
	}

	s.rateWindow = append(s.rateWindow, e.Timestamp)
	s.trimRate(e.Timestamp)

	s.mu.Unlock()

	// Facet observation uses its own mutex and must run outside store's
	// critical section, so it never extends store.mu's hold time.
	s.facets.observe(e)
}

// trimRate drops rate samples older than the 5s window. Caller holds the lock.
func (s *store) trimRate(now time.Time) {
	cutoff := now.Add(-5 * time.Second)
	i := 0
	for i < len(s.rateWindow) && s.rateWindow[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		s.rateWindow = s.rateWindow[i:]
	}
}

// recent returns up to limit of the most recent entries, newest first, that
// satisfy match. A nil match accepts everything.
func (s *store) recent(limit int, match func(*api.Entry) bool) []*api.Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*api.Entry, 0, limit)
	// Walk backwards from the most recently written slot.
	n := s.capacity
	if !s.full {
		n = s.next
	}
	for i := 0; i < n && len(out) < limit; i++ {
		idx := (s.next - 1 - i + s.capacity) % s.capacity
		e := s.buf[idx]
		if e == nil {
			continue
		}
		if match == nil || match(e) {
			out = append(out, e)
		}
	}
	return out
}

// recentBefore returns up to limit entries strictly older than beforeID,
// newest first, satisfying match — the walk-back that powers "load older"
// pagination beyond what the WS replay/live buffer already surfaced. If
// beforeID isn't found in the ring buffer (aged out), it returns no results
// rather than guessing, since there's no reliable anchor for "older than".
func (s *store) recentBefore(beforeID string, limit int, match func(*api.Entry) bool) []*api.Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*api.Entry, 0, limit)
	n := s.capacity
	if !s.full {
		n = s.next
	}
	skipping := true
	for i := 0; i < n && len(out) < limit; i++ {
		idx := (s.next - 1 - i + s.capacity) % s.capacity
		e := s.buf[idx]
		if e == nil {
			continue
		}
		if skipping {
			if e.ID == beforeID {
				skipping = false
			}
			continue
		}
		if match == nil || match(e) {
			out = append(out, e)
		}
	}
	return out
}

// recentBeforeSeq returns up to limit entries with Seq strictly less than
// beforeSeq, newest first, satisfying match. Unlike recentBefore (anchored on
// an entry ID that must still be present in the ring to find the starting
// point), this needs no such lookup: Seq is a hub-assigned monotonic counter
// (see add), so "older than this point" is a plain comparison that keeps
// working even once the entry a client is paging from has aged out of the
// buffer.
func (s *store) recentBeforeSeq(beforeSeq int64, limit int, match func(*api.Entry) bool) []*api.Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*api.Entry, 0, limit)
	n := s.capacity
	if !s.full {
		n = s.next
	}
	for i := 0; i < n && len(out) < limit; i++ {
		idx := (s.next - 1 - i + s.capacity) % s.capacity
		e := s.buf[idx]
		if e == nil || e.Seq >= beforeSeq {
			continue
		}
		if match == nil || match(e) {
			out = append(out, e)
		}
	}
	return out
}

// get returns the entry with the given id, or nil. O(1) via the byID index.
func (s *store) get(id string) *api.Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byID[id]
}

// size returns how many ring buffer slots are currently filled (0..capacity)
// -- the buffer's fill level, exposed via /metrics so an operator can tell how
// much history depth the buffer is actually holding under the current load.
func (s *store) size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.full {
		return s.capacity
	}
	return s.next
}

// stats snapshots the current aggregates. workers is supplied by the caller
// since worker connections are tracked by the server, not the store.
func (s *store) stats(workers int) api.Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	byProto := make(map[string]int64, len(s.byProtocol))
	for k, v := range s.byProtocol {
		byProto[k] = v
	}
	byStatus := make(map[string]int64, len(s.byStatus))
	for k, v := range s.byStatus {
		byStatus[k] = v
	}

	// Count samples inside the trailing 5s window without mutating rateWindow
	// (stats holds only RLock, and the rate must decay to 0 when traffic stops
	// rather than freezing at the last trimmed length). rateWindow is appended
	// in timestamp order, so walk from the newest end until we fall out of the
	// window.
	cutoff := time.Now().Add(-5 * time.Second)
	recent := 0
	for i := len(s.rateWindow) - 1; i >= 0; i-- {
		if s.rateWindow[i].Before(cutoff) {
			break
		}
		recent++
	}

	w1, w5 := s.windows(time.Now())

	return api.Stats{
		TotalEntries:  s.total,
		EntriesPerSec: float64(recent) / 5.0,
		Workers:       workers,
		ByProtocol:    byProto,
		ByStatus:      byStatus,
		Last1m:        w1,
		Last5m:        w5,
	}
}

// windows tallies the trailing 1m/5m slices by walking the ring newest-first.
// Entries land in arrival order, which tracks capture time closely enough for
// coarse windows, so the walk stops at the first entry older than 5m. Caller
// holds at least RLock.
func (s *store) windows(now time.Time) (last1m, last5m *api.WindowStats) {
	cut1, cut5 := now.Add(-time.Minute), now.Add(-5*time.Minute)
	w1, w5 := &api.WindowStats{}, &api.WindowStats{}
	n := s.capacity
	if !s.full {
		n = s.next
	}
	for i := 0; i < n; i++ {
		e := s.buf[(s.next-1-i+s.capacity)%s.capacity]
		if e == nil {
			continue
		}
		if e.Timestamp.Before(cut5) {
			break
		}
		tally := func(w *api.WindowStats) {
			w.Entries++
			switch e.Status {
			case "error":
				w.Errors++
			case "warning":
				w.Warnings++
			}
		}
		tally(w5)
		if !e.Timestamp.Before(cut1) {
			tally(w1)
		}
	}
	w1.EntriesPerSec = float64(w1.Entries) / 60.0
	w5.EntriesPerSec = float64(w5.Entries) / 300.0
	return w1, w5
}
