// Package hub is the central aggregator. Workers stream reconstructed entries
// into it over WebSocket; front-end clients subscribe (also over WebSocket) and
// receive a live, server-side-filtered feed plus periodic stats. A REST API
// exposes recent history for cold loads and for the CLI/MCP.
package hub

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/pkg/api"
)

// Options configures a hub. The zero value is a working local-dev hub.
type Options struct {
	// UIDir, when non-empty, is a directory whose static files are served at
	// "/" (used for local runs; in-cluster the front is a separate nginx pod).
	UIDir string
	// APIToken, when non-empty, requires `Authorization: Bearer <token>` (or a
	// ?token= query param, for browser WebSockets) on /api/* and the WebSocket
	// endpoints. Empty keeps the API open (local dev, trusted clusters).
	APIToken string
	// BufferSize overrides the in-memory entry ring size (0 = default).
	BufferSize int
}

// Server is the hub. Construct with New and start with Run.
type Server struct {
	store    *store
	log      *slog.Logger
	upgrader websocket.Upgrader
	apiToken string

	mu           sync.RWMutex
	frontClients map[*frontClient]struct{}
	workerCount  int32

	// wmu guards workers (the per-node registry behind /api/workers) and
	// workerConns (live connections' send channels, used to deliver
	// pause/resume commands). Separate from mu so per-entry bookkeeping
	// never contends with the broadcast path.
	wmu         sync.Mutex
	workers     map[string]*workerInfo
	workerConns map[string]chan []byte

	broadcastDropped int64 // entries dropped to slow front clients (atomic)

	resolver *resolver // k8s IP -> pod/service name enrichment (no-op off-cluster)

	uiDir string // optional: serve a built front from here (local dev)

	statsHistMu sync.RWMutex
	statsHist   []api.StatsPoint // rolling throughput history, capped at statsHistoryCap
}

// statsHistoryCap bounds the rolling stats history: at the statsLoop's 2s
// sample interval this covers 10 minutes, enough for a "requests/sec" trend
// sparkline without unbounded growth.
const statsHistoryCap = 300

// workerGCTTL bounds how long a disconnected worker's row survives in the
// registry after it was last seen. Without this, an autoscaling or spot-node
// cluster that churns through node names accumulates stale rows (and their
// per-node Prometheus series) in /api/workers forever. A currently-connected
// worker is never pruned regardless of LastSeen; the window is kept
// generous enough that "the worker was here, went away N minutes ago" stays
// answerable during an incident.
const workerGCTTL = time.Hour

// New builds a hub.
func New(log *slog.Logger, opts Options) *Server {
	size := opts.BufferSize
	if size <= 0 {
		size = config.EntryBufferSize
	}
	return &Server{
		store:        newStore(size),
		log:          log,
		apiToken:     opts.APIToken,
		frontClients: map[*frontClient]struct{}{},
		workers:      map[string]*workerInfo{},
		workerConns:  map[string]chan []byte{},
		resolver:     newResolver(log),
		uiDir:        opts.UIDir,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// Dashboards are typically reached cross-origin (port-forward,
			// ingress); allow any origin. Tighten via ingress auth in prod.
			CheckOrigin: func(*http.Request) bool { return true },
		},
	}
}

// Run serves until ctx is cancelled (e.g. SIGINT/SIGTERM), then drains
// in-flight requests via a bounded graceful shutdown.
func (s *Server) Run(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/worker", s.handleWorker)
	mux.HandleFunc("/ws", s.handleFront)
	mux.HandleFunc("/api/entries", s.handleEntries)
	mux.HandleFunc("/api/entry/", s.handleEntry)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/stats/history", s.handleStatsHistory)
	mux.HandleFunc("/api/summary", s.handleSummary)
	mux.HandleFunc("/api/timeline", s.handleTimeline)
	mux.HandleFunc("/api/workers", s.handleWorkers)
	mux.HandleFunc("/api/workers/capture", s.handleWorkerCapture)
	mux.HandleFunc("/api/fields", s.handleFields)
	mux.HandleFunc("/api/fields/", s.handleFieldValues)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	if s.uiDir != "" {
		mux.Handle("/", http.FileServer(http.Dir(s.uiDir)))
	}

	go s.statsLoop(ctx)
	go s.resolver.run(ctx)

	s.log.Info("hub listening", "addr", addr, "ui", s.uiDir != "", "auth", s.apiToken != "")
	srv := &http.Server{
		Addr:              addr,
		Handler:           withCORS(s.withAuth(mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.log.Info("hub shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// --- worker channel --------------------------------------------------------

// workerInfo is one row of the /api/workers registry: a worker's lifecycle
// plus its self-reported counters, kept after disconnect so "worker was here,
// went away 2 min ago" is answerable during an incident.
type workerInfo struct {
	Node          string    `json:"node"`
	Version       string    `json:"version,omitempty"`
	Connected     bool      `json:"connected"`
	ConnectedAt   time.Time `json:"connectedAt"`
	LastSeen      time.Time `json:"lastSeen"`
	Entries       int64     `json:"entries"`       // entries the hub received from this worker
	Dropped       uint64    `json:"dropped"`       // worker-reported sink-buffer drops
	CaptureLive   bool      `json:"captureLive"`   // AF_PACKET source active on the worker
	CaptureTLS    bool      `json:"captureTls"`    // eBPF TLS capture active on the worker
	CapturePaused bool      `json:"capturePaused"` // hub told this worker to stop turning capture into entries
	RingPackets   uint64    `json:"ringPackets"`   // AF_PACKET kernel ring: cumulative packets delivered
	RingDrops     uint64    `json:"ringDrops"`     // AF_PACKET kernel ring: cumulative packets dropped before userspace saw them
	FlowsEvicted  uint64    `json:"flowsEvicted"`  // generic L4 flows dropped by the worker's maxFlows cap
}

func (s *Server) handleWorker(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("worker upgrade failed", "err", err)
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20) // bound per-message allocation (1 MiB)

	atomic.AddInt32(&s.workerCount, 1)
	defer atomic.AddInt32(&s.workerCount, -1)

	// send carries commands (pause/resume) down to this worker; workerWriter
	// drains it concurrently with the read loop below so a command can go
	// out at any time without waiting on (or blocking) message reads.
	send := make(chan []byte, 8)
	go s.workerWriter(conn, send)

	node := "unknown"
	defer func() {
		s.workerUpdate(node, func(wi *workerInfo) { wi.Connected = false })
		s.wmu.Lock()
		// Only remove if this connection's channel is still the registered
		// one — a reconnect for the same node may have already replaced it.
		if s.workerConns[node] == send {
			delete(s.workerConns, node)
		}
		s.wmu.Unlock()
		close(send)
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			s.log.Debug("worker disconnected", "node", node, "err", err)
			return
		}
		var env api.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		switch env.Type {
		case api.MsgHello:
			if env.Hello != nil {
				node = env.Hello.Node
				version := env.Hello.Version
				s.log.Info("worker connected", "node", node, "version", version)
				now := time.Now()
				s.workerUpdate(node, func(wi *workerInfo) {
					wi.Version = version
					wi.Connected = true
					wi.ConnectedAt = now
					wi.LastSeen = now
				})
				s.wmu.Lock()
				s.workerConns[node] = send
				s.wmu.Unlock()
			}
		case api.MsgEntry:
			if env.Entry != nil {
				s.resolver.enrich(env.Entry)
				s.store.add(env.Entry)
				s.broadcast(env.Entry)
				s.workerUpdate(node, func(wi *workerInfo) {
					wi.Entries++
					wi.LastSeen = time.Now()
				})
			}
		case api.MsgWorkerStats:
			if ws := env.WorkerStats; ws != nil {
				s.workerUpdate(node, func(wi *workerInfo) {
					wi.Dropped = ws.Dropped
					wi.CaptureLive = ws.CaptureLive
					wi.CaptureTLS = ws.CaptureTLS
					wi.CapturePaused = ws.CapturePaused
					wi.RingPackets = ws.RingPackets
					wi.RingDrops = ws.RingDrops
					wi.FlowsEvicted = ws.FlowsEvicted
					wi.LastSeen = time.Now()
				})
			}
		}
	}
}

// workerWriter drains send to conn until the channel closes (worker
// disconnected) or a write fails (dead connection — the read loop's next
// ReadMessage will notice and tear things down).
func (s *Server) workerWriter(conn *websocket.Conn, send chan []byte) {
	for b := range send {
		if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
			return
		}
	}
}

// sendWorkerCommand delivers cmd to node's connection, or to every currently
// connected worker when node is "". Returns how many it reached — 0 for an
// unknown/disconnected node is a normal, silent no-op (the front end's
// toggle reflects registry state, not a per-call delivery guarantee).
func (s *Server) sendWorkerCommand(node string, cmd api.WorkerCommand) int {
	b, err := json.Marshal(api.Envelope{Type: api.MsgWorkerCommand, WorkerCommand: &cmd})
	if err != nil {
		return 0
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	sent := 0
	deliver := func(ch chan []byte) {
		select {
		case ch <- b:
			sent++
		default:
			// Worker's inbound command buffer is full (very small, 8 slots) —
			// drop rather than block the caller; the next state fetch will
			// still reflect whatever the worker was last told.
		}
	}
	if node == "" {
		for _, ch := range s.workerConns {
			deliver(ch)
		}
		return sent
	}
	if ch, ok := s.workerConns[node]; ok {
		deliver(ch)
	}
	return sent
}

// handleWorkerCapture pauses or resumes capture on one worker (node) or all
// of them (node omitted/empty). Pausing doesn't stop AF_PACKET/eBPF at the
// source or drop the hub connection — the worker just stops turning what it
// reads into entries — so resuming is instant.
func (s *Server) handleWorkerCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Node   string `json:"node"`
		Paused bool   `json:"paused"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	sent := s.sendWorkerCommand(req.Node, api.WorkerCommand{Paused: req.Paused})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"sent": sent})
}

// workerUpdate applies fn to node's registry row, creating it on first sight.
// A pre-hello connection ("unknown" node) is not tracked.
func (s *Server) workerUpdate(node string, fn func(*workerInfo)) {
	if node == "" || node == "unknown" {
		return
	}
	s.wmu.Lock()
	wi := s.workers[node]
	if wi == nil {
		wi = &workerInfo{Node: node}
		s.workers[node] = wi
	}
	fn(wi)
	s.wmu.Unlock()
}

// gcWorkers removes registry rows for workers disconnected for more than
// workerGCTTL. Called periodically from statsLoop, which already runs a 2s
// ticker on the hub's lifetime.
func (s *Server) gcWorkers() {
	cutoff := time.Now().Add(-workerGCTTL)
	s.wmu.Lock()
	for node, wi := range s.workers {
		if !wi.Connected && wi.LastSeen.Before(cutoff) {
			delete(s.workers, node)
		}
	}
	s.wmu.Unlock()
}

// workerSnapshot copies the registry, sorted by node name.
func (s *Server) workerSnapshot() []workerInfo {
	s.wmu.Lock()
	out := make([]workerInfo, 0, len(s.workers))
	for _, wi := range s.workers {
		out = append(out, *wi)
	}
	s.wmu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

// --- front channel ---------------------------------------------------------

type frontClient struct {
	conn *websocket.Conn
	send chan []byte
	mu   sync.RWMutex
	pred Predicate
}

func (s *Server) handleFront(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("front upgrade failed", "err", err)
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20) // bound per-message allocation (1 MiB)
	// Read deadline + pong handler so the 30s ping in frontWriter actually
	// detects a dead peer: every pong pushes the deadline out; a silent client
	// trips ReadMessage in frontReader once the window elapses, which then
	// deregisters and closes the send channel.
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})

	pred, filterErr := CompileFilter(r.URL.Query().Get("filter"))
	if filterErr != nil {
		// A bad ?filter= must not fall back to nil (which matches everything and
		// replays the whole firehose); match nothing until the client sends a
		// valid filter frame.
		s.log.Debug("front filter compile failed; using match-nothing", "err", filterErr)
		pred = func(*api.Entry) bool { return false }
	}
	c := &frontClient{
		conn: conn,
		send: make(chan []byte, 256),
		pred: pred,
	}

	s.mu.Lock()
	s.frontClients[c] = struct{}{}
	s.mu.Unlock()

	s.log.Debug("front client connected", "remote", r.RemoteAddr)

	if filterErr != nil {
		if b, err := json.Marshal(api.Envelope{Type: api.MsgFilterError, Error: filterErr.Error()}); err == nil {
			c.trySend(b)
		}
	}

	// Replay recent history that matches the initial filter, then the stats.
	s.replayHistory(c)
	c.trySend(s.statsBytes())

	go s.frontReader(c)
	s.frontWriter(c)
}

// frontReader consumes control frames from a front client (filter updates).
func (s *Server) frontReader(c *frontClient) {
	defer func() {
		s.mu.Lock()
		delete(s.frontClients, c)
		s.mu.Unlock()
		close(c.send)
	}()
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var env api.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		if env.Type == api.MsgFilter {
			pred, err := CompileFilter(env.Filter)
			if err != nil {
				if b, merr := json.Marshal(api.Envelope{Type: api.MsgFilterError, Error: err.Error()}); merr == nil {
					c.trySend(b)
				}
				continue // keep previous filter on parse error
			}
			c.mu.Lock()
			c.pred = pred
			c.mu.Unlock()
			// Resend matching history so a live filter swap surfaces the past,
			// not just future traffic (the client cleared its table on change).
			s.replayHistory(c)
		}
	}
}

// replayHistory resends up to 500 recent entries matching the client's current
// predicate, newest last so the UI appends chronologically. Used on initial
// connect and after a live filter swap.
func (s *Server) replayHistory(c *frontClient) {
	c.mu.RLock()
	pred := c.pred
	c.mu.RUnlock()
	history := s.store.recent(500, pred)
	for i := len(history) - 1; i >= 0; i-- {
		if b, err := json.Marshal(api.Envelope{Type: api.MsgEntry, Entry: history[i]}); err == nil {
			c.trySend(b)
		}
	}
}

// frontWriter drains the send channel to the socket.
func (s *Server) frontWriter(c *frontClient) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case b, ok := <-c.send:
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// trySend queues b for the client without blocking. It reports false when the
// buffer is full (a slow client), so the caller can account for the drop.
func (c *frontClient) trySend(b []byte) bool {
	select {
	case c.send <- b:
		return true
	default:
		// Slow client: drop rather than block the broadcaster.
		return false
	}
}

func (c *frontClient) matches(e *api.Entry) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pred == nil || c.pred(e)
}

// broadcast fans an entry out to every front client whose filter accepts it.
func (s *Server) broadcast(e *api.Entry) {
	// Fast path: with no front clients, skip the marshal entirely (the worker
	// ingest path calls this for every entry).
	s.mu.RLock()
	empty := len(s.frontClients) == 0
	s.mu.RUnlock()
	if empty {
		return
	}

	b, err := json.Marshal(api.Envelope{Type: api.MsgEntry, Entry: e})
	if err != nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for c := range s.frontClients {
		if c.matches(e) {
			if !c.trySend(b) {
				atomic.AddInt64(&s.broadcastDropped, 1)
			}
		}
	}
}

// --- stats -----------------------------------------------------------------

func (s *Server) statsLoop(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.gcWorkers()
		st := s.statsSnapshot()
		s.recordStatsPoint(st)
		b := s.marshalStats(st)
		s.mu.RLock()
		for c := range s.frontClients {
			c.trySend(b)
		}
		s.mu.RUnlock()
	}
}

// statsSnapshot returns the store's aggregate stats plus hub-level counters
// the store doesn't own (broadcastDropped is tracked on Server, see broadcast).
func (s *Server) statsSnapshot() api.Stats {
	st := s.store.stats(int(atomic.LoadInt32(&s.workerCount)))
	st.BroadcastDropped = atomic.LoadInt64(&s.broadcastDropped)
	return st
}

// recordStatsPoint appends one sample to the rolling stats history, trimming
// the oldest entry once statsHistoryCap is exceeded.
func (s *Server) recordStatsPoint(st api.Stats) {
	s.statsHistMu.Lock()
	s.statsHist = append(s.statsHist, api.StatsPoint{
		Timestamp:     time.Now(),
		EntriesPerSec: st.EntriesPerSec,
		TotalEntries:  st.TotalEntries,
	})
	if len(s.statsHist) > statsHistoryCap {
		s.statsHist = s.statsHist[len(s.statsHist)-statsHistoryCap:]
	}
	s.statsHistMu.Unlock()
}

func (s *Server) marshalStats(st api.Stats) []byte {
	b, _ := json.Marshal(api.Envelope{Type: api.MsgStats, Stats: &st})
	return b
}

func (s *Server) statsBytes() []byte {
	return s.marshalStats(s.statsSnapshot())
}

// --- REST ------------------------------------------------------------------

func (s *Server) handleEntries(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		// Invalid or non-positive keeps the default; oversized clamps to the
		// buffer instead of silently snapping back to 200.
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = clampInt(n, 1, s.store.capacity)
		}
	}
	pred, err := s.queryPredicate(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if sortField := r.URL.Query().Get("sort"); sortField != "" {
		desc, err := sortOrder(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !validSortField(sortField) {
			http.Error(w, "unknown or non-numeric sort field: "+sortField, http.StatusBadRequest)
			return
		}
		matched := s.store.recent(s.store.capacity, pred)
		writeJSON(w, topNBySort(matched, fieldGetter(sortField), desc, limit))
		return
	}

	if before := r.URL.Query().Get("before"); before != "" {
		writeJSON(w, s.store.recentBefore(before, limit, pred))
		return
	}
	writeJSON(w, s.store.recent(limit, pred))
}

// sortOrder parses ?order=asc|desc for handleEntries' ?sort=, defaulting to
// desc (the common "biggest/slowest first" case) when order is omitted.
func sortOrder(r *http.Request) (desc bool, err error) {
	switch v := r.URL.Query().Get("order"); v {
	case "", "desc":
		return true, nil
	case "asc":
		return false, nil
	default:
		return false, fmt.Errorf("invalid order %q (want asc or desc)", v)
	}
}

// queryPredicate compiles the shared ?filter=&since=&until= query params into
// one predicate. since/until accept RFC3339, unix seconds, or a relative
// duration meaning "that long ago" (e.g. since=15m).
func (s *Server) queryPredicate(r *http.Request) (Predicate, error) {
	pred, err := CompileFilter(r.URL.Query().Get("filter"))
	if err != nil {
		return nil, fmt.Errorf("invalid filter: %w", err)
	}
	since, until, err := timeRange(r)
	if err != nil {
		return nil, err
	}
	if since.IsZero() && until.IsZero() {
		return pred, nil
	}
	return func(e *api.Entry) bool {
		if !since.IsZero() && e.Timestamp.Before(since) {
			return false
		}
		if !until.IsZero() && e.Timestamp.After(until) {
			return false
		}
		return pred(e)
	}, nil
}

// timeRange parses the optional since/until query params.
func timeRange(r *http.Request) (since, until time.Time, err error) {
	if v := r.URL.Query().Get("since"); v != "" {
		if since, err = parseTimeParam(v); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid since: %w", err)
		}
	}
	if v := r.URL.Query().Get("until"); v != "" {
		if until, err = parseTimeParam(v); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid until: %w", err)
		}
	}
	return since, until, nil
}

// parseTimeParam accepts RFC3339 ("2026-07-15T14:00:00Z"), unix seconds
// ("1752588000"), or a Go duration meaning that long ago ("15m", "1h30m").
func parseTimeParam(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(secs, 0), nil
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("want RFC3339, unix seconds, or a duration like 15m: %q", v)
}

// sortedKeys returns m's keys sorted ascending, for deterministic /metrics
// output ordering.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func (s *Server) handleEntry(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/api/entry/"):]
	e := s.store.get(id)
	if e == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, e)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.statsSnapshot())
}

// handleStatsHistory serves the rolling throughput history (one point per
// statsLoop tick, ~10 minutes at the current 2s interval) so a freshly
// connected client can render a trend sparkline immediately instead of
// waiting to accumulate its own samples live.
func (s *Server) handleStatsHistory(w http.ResponseWriter, r *http.Request) {
	s.statsHistMu.RLock()
	out := make([]api.StatsPoint, len(s.statsHist))
	copy(out, s.statsHist)
	s.statsHistMu.RUnlock()
	writeJSON(w, out)
}

// handleSummary serves GET /api/summary?groupBy=&filter=&since=&until=&limit=,
// aggregating the buffered entries into per-group counts, error totals and
// latency percentiles. See summarize (summary.go) for the groupBy keys.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	groupBy := r.URL.Query().Get("groupBy")
	if groupBy == "" {
		groupBy = "workload"
	}
	if !validGroupBy(groupBy) {
		http.Error(w, "unknown groupBy field: "+groupBy, http.StatusBadRequest)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = clampInt(n, 1, 1000)
		}
	}
	pred, err := s.queryPredicate(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	entries := s.store.recent(s.store.capacity, pred)
	groups := summarize(entries, groupBy)
	if len(groups) > limit {
		groups = groups[:limit]
	}
	writeJSON(w, map[string]any{
		"groupBy": groupBy,
		"total":   len(entries),
		"groups":  groups,
	})
}

// handleTimeline serves GET /api/timeline?bucket=&filter=&since=&until=,
// bucketing matching entries into a fixed-step time series (gaps included as
// zero buckets) so "when did this start" is answerable at a glance.
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	bucket := 60 * time.Second
	if v := r.URL.Query().Get("bucket"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			bucket = time.Duration(clampInt(n, 1, 3600)) * time.Second
		}
	}
	since, until, err := timeRange(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if until.IsZero() {
		until = time.Now()
	}
	if since.IsZero() {
		since = until.Add(-15 * time.Minute)
	}
	pred, err := s.queryPredicate(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	entries := s.store.recent(s.store.capacity, pred)
	writeJSON(w, map[string]any{
		"bucketSeconds": int(bucket / time.Second),
		"buckets":       timeline(entries, since, until, bucket),
	})
}

// handleWorkers serves GET /api/workers: every worker ever seen (connected or
// not), with self-reported drop/capture state.
func (s *Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.workerSnapshot())
}

// handleMetrics emits the hub's self-metrics in Prometheus text exposition
// format, hand-rolled to avoid a client_golang dependency (stdlib only).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	st := s.store.stats(int(atomic.LoadInt32(&s.workerCount)))
	s.mu.RLock()
	frontClients := len(s.frontClients)
	s.mu.RUnlock()
	dropped := atomic.LoadInt64(&s.broadcastDropped)

	var b strings.Builder
	metric := func(name, typ, help, value string) {
		fmt.Fprintf(&b, "# HELP %s %s\n", name, help)
		fmt.Fprintf(&b, "# TYPE %s %s\n", name, typ)
		fmt.Fprintf(&b, "%s %s\n", name, value)
	}
	metric("k8shark_hub_entries_total", "counter",
		"Total entries ingested by the hub.", strconv.FormatInt(st.TotalEntries, 10))
	metric("k8shark_hub_front_clients", "gauge",
		"Currently connected front-end clients.", strconv.Itoa(frontClients))
	metric("k8shark_hub_workers", "gauge",
		"Currently connected workers.", strconv.Itoa(st.Workers))
	metric("k8shark_hub_broadcast_dropped_total", "counter",
		"Entries dropped to slow front clients.", strconv.FormatInt(dropped, 10))
	metric("k8shark_hub_entries_per_sec", "gauge",
		"Entries ingested per second, trailing 5s window.", strconv.FormatFloat(st.EntriesPerSec, 'f', -1, 64))
	metric("k8shark_hub_buffer_entries", "gauge",
		"Ring buffer slots currently filled.", strconv.Itoa(s.store.size()))
	metric("k8shark_hub_buffer_capacity", "gauge",
		"Ring buffer capacity (max entries retained).", strconv.Itoa(s.store.capacity))
	metric("k8shark_hub_k8s_enrich_failures_total", "counter",
		"Failed k8s enrichment refresh cycles (e.g. broken RBAC).", strconv.FormatInt(s.resolver.failures(), 10))

	if len(st.ByProtocol) > 0 {
		fmt.Fprintf(&b, "# HELP k8shark_hub_entries_by_protocol_total Entries ingested, by protocol.\n")
		fmt.Fprintf(&b, "# TYPE k8shark_hub_entries_by_protocol_total counter\n")
		for _, proto := range sortedKeys(st.ByProtocol) {
			fmt.Fprintf(&b, "k8shark_hub_entries_by_protocol_total{protocol=%q} %d\n", proto, st.ByProtocol[proto])
		}
	}
	if len(st.ByStatus) > 0 {
		fmt.Fprintf(&b, "# HELP k8shark_hub_entries_by_status_total Entries ingested, by normalised status.\n")
		fmt.Fprintf(&b, "# TYPE k8shark_hub_entries_by_status_total counter\n")
		for _, status := range sortedKeys(st.ByStatus) {
			fmt.Fprintf(&b, "k8shark_hub_entries_by_status_total{status=%q} %d\n", status, st.ByStatus[status])
		}
	}

	// Per-worker series, from the registry (self-reported drops make silent
	// capture loss visible to alerting).
	workers := s.workerSnapshot()
	if len(workers) > 0 {
		fmt.Fprintf(&b, "# HELP k8shark_worker_entries_total Entries received from each worker.\n")
		fmt.Fprintf(&b, "# TYPE k8shark_worker_entries_total counter\n")
		for _, wi := range workers {
			fmt.Fprintf(&b, "k8shark_worker_entries_total{node=%q} %d\n", wi.Node, wi.Entries)
		}
		fmt.Fprintf(&b, "# HELP k8shark_worker_dropped_total Worker-reported entries dropped before reaching the hub.\n")
		fmt.Fprintf(&b, "# TYPE k8shark_worker_dropped_total counter\n")
		for _, wi := range workers {
			fmt.Fprintf(&b, "k8shark_worker_dropped_total{node=%q} %d\n", wi.Node, wi.Dropped)
		}
		fmt.Fprintf(&b, "# HELP k8shark_worker_connected Whether the worker is currently connected.\n")
		fmt.Fprintf(&b, "# TYPE k8shark_worker_connected gauge\n")
		for _, wi := range workers {
			v := 0
			if wi.Connected {
				v = 1
			}
			fmt.Fprintf(&b, "k8shark_worker_connected{node=%q} %d\n", wi.Node, v)
		}
		fmt.Fprintf(&b, "# HELP k8shark_worker_ring_drops_total AF_PACKET kernel ring packets dropped before userspace saw them.\n")
		fmt.Fprintf(&b, "# TYPE k8shark_worker_ring_drops_total counter\n")
		for _, wi := range workers {
			fmt.Fprintf(&b, "k8shark_worker_ring_drops_total{node=%q} %d\n", wi.Node, wi.RingDrops)
		}
		fmt.Fprintf(&b, "# HELP k8shark_worker_flows_evicted_total Generic L4 flows dropped by the worker's maxFlows cap.\n")
		fmt.Fprintf(&b, "# TYPE k8shark_worker_flows_evicted_total counter\n")
		for _, wi := range workers {
			fmt.Fprintf(&b, "k8shark_worker_flows_evicted_total{node=%q} %d\n", wi.Node, wi.FlowsEvicted)
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// --- IFL autocomplete (fields/values) ---------------------------------------

// fieldMeta is the /api/fields JSON shape for a single field.
type fieldMeta struct {
	Name      string       `json:"name"`
	Type      FieldType    `json:"type"`
	Operators []string     `json:"operators"`
	Values    []FieldValue `json:"values,omitempty"`
}

// handleFields returns the full field catalog, with current top-N observed
// values (merged with any static EnumValues) inlined for tracked fields.
func (s *Server) handleFields(w http.ResponseWriter, r *http.Request) {
	out := make([]fieldMeta, 0, len(fieldCatalog))
	for _, spec := range fieldCatalog {
		fm := fieldMeta{Name: spec.Name, Type: spec.Type, Operators: spec.Operators}
		if spec.TrackValues {
			fm.Values = s.fieldValuesFor(spec, "", facetTopN)
		}
		out = append(out, fm)
	}
	// Header field names are dynamic (any header key can appear) so they
	// aren't in the static fieldCatalog above; append one synthetic entry per
	// observed key instead, with no value list (header values are
	// effectively freetext -- see DIS-12).
	reqHeaders, respHeaders := s.store.facets.headerFieldNames()
	for _, h := range reqHeaders {
		out = append(out, fieldMeta{Name: "request.header." + h, Type: FieldTypeString, Operators: opsString})
	}
	for _, h := range respHeaders {
		out = append(out, fieldMeta{Name: "response.header." + h, Type: FieldTypeString, Operators: opsString})
	}
	writeJSON(w, map[string]any{"fields": out})
}

// handleFieldValues serves GET /api/fields/{field}/values?prefix=&limit=.
func (s *Server) handleFieldValues(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/fields/")
	field := strings.TrimSuffix(rest, "/values")
	if field == rest || field == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// 404 only for a genuinely unknown field. A known but untracked field
	// (freetext / high-cardinality, e.g. request.path) returns 200 with an empty
	// value list — "no suggestions" is a valid answer, not an error.
	spec, ok := fieldSpecByName[field]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			switch {
			case n < 1:
				n = 1
			case n > 200:
				n = 200
			}
			limit = n
		}
	}
	prefix := r.URL.Query().Get("prefix")

	writeJSON(w, map[string]any{
		"field":  field,
		"type":   spec.Type,
		"values": s.fieldValuesFor(spec, prefix, limit),
	})
}

// fieldValuesFor returns spec's observed values (via the store's facet
// index) merged with any static EnumValues -- so an enum domain (protocol,
// status, ...) is fully suggestable even before matching traffic arrives.
// Static-only values get count 0; observed counts win when a value appears
// in both. Filtered by prefix (case-insensitive) and truncated to limit.
// Only meaningful when spec.TrackValues is true.
func (s *Server) fieldValuesFor(spec FieldSpec, prefix string, limit int) []FieldValue {
	observed, _ := s.store.facets.values(spec.Name, prefix, facetTrackCap)
	byValue := make(map[string]int64, len(observed)+len(spec.EnumValues))
	for _, v := range observed {
		byValue[v.Value] = v.Count
	}
	lowerPrefix := strings.ToLower(prefix)
	for _, ev := range spec.EnumValues {
		if _, exists := byValue[ev]; exists {
			continue
		}
		if prefix == "" || strings.HasPrefix(strings.ToLower(ev), lowerPrefix) {
			byValue[ev] = 0
		}
	}
	out := make([]FieldValue, 0, len(byValue))
	for v, c := range byValue {
		out = append(out, FieldValue{Value: v, Count: c})
	}
	sortFieldValues(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// withAuth enforces the optional API token on /api/* and the WebSocket
// endpoints. Static UI assets, /healthz and /metrics stay open (the data is
// behind /api; metrics scraping is cluster-internal). Browsers cannot set
// headers on a WebSocket, so a ?token= query param is accepted as well.
func (s *Server) withAuth(h http.Handler) http.Handler {
	if s.apiToken == "" {
		return h
	}
	want := []byte(s.apiToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.HasPrefix(p, "/api/") && p != "/api" && p != "/ws" && !strings.HasPrefix(p, "/ws/") {
			h.ServeHTTP(w, r)
			return
		}
		got := r.URL.Query().Get("token")
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			got = strings.TrimPrefix(auth, "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}
