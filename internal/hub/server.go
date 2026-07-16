// Package hub is the central aggregator. Workers stream reconstructed entries
// into it over WebSocket; front-end clients subscribe (also over WebSocket) and
// receive a live, server-side-filtered feed plus periodic stats. A REST API
// exposes recent history for cold loads and for the CLI/MCP.
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/pkg/api"
)

// Server is the hub. Construct with New and start with Run.
type Server struct {
	store    *store
	log      *slog.Logger
	upgrader websocket.Upgrader

	mu           sync.RWMutex
	frontClients map[*frontClient]struct{}
	workerCount  int32

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

// New builds a hub. uiDir, when non-empty, is a directory whose static files
// are served at "/" (used for local runs; in-cluster the front is a separate
// nginx pod).
func New(log *slog.Logger, uiDir string) *Server {
	return &Server{
		store:        newStore(config.EntryBufferSize),
		log:          log,
		frontClients: map[*frontClient]struct{}{},
		resolver:     newResolver(log),
		uiDir:        uiDir,
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

	go s.statsLoop()
	go s.resolver.run(ctx)

	s.log.Info("hub listening", "addr", addr, "ui", s.uiDir != "")
	srv := &http.Server{
		Addr:              addr,
		Handler:           withCORS(mux),
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

	node := "unknown"
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
				s.log.Info("worker connected", "node", node, "version", env.Hello.Version)
			}
		case api.MsgEntry:
			if env.Entry != nil {
				s.resolver.enrich(env.Entry)
				s.store.add(env.Entry)
				s.broadcast(env.Entry)
			}
		}
	}
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

func (s *Server) statsLoop() {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
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
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= config.EntryBufferSize {
			limit = n
		}
	}
	pred, err := CompileFilter(r.URL.Query().Get("filter"))
	if err != nil {
		http.Error(w, "invalid filter: "+err.Error(), http.StatusBadRequest)
		return
	}
	if before := r.URL.Query().Get("before"); before != "" {
		writeJSON(w, s.store.recentBefore(before, limit, pred))
		return
	}
	writeJSON(w, s.store.recent(limit, pred))
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
