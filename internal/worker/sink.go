package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/pkg/api"
)

// sinkWriteTimeout bounds a single hub WriteMessage so a half-open connection
// can't wedge the pump for the OS TCP timeout (minutes) while the buffer
// silently drops all capture.
const sinkWriteTimeout = 10 * time.Second

// sinkStatsInterval is how often the sink self-reports drop counters and
// capture state to the hub (surfaced at /api/workers).
const sinkStatsInterval = 10 * time.Second

// sink is a reconnecting WebSocket client that ships entries to the hub. Entries
// are buffered on a channel; if the hub is unreachable the buffer drops the
// newest (incoming) entry rather than blocking capture.
type sink struct {
	hubURL   string
	hubToken string // bearer token sent on dial ("" = no auth)
	node     string
	log      *slog.Logger

	ch      chan *api.Entry
	dropped atomic.Uint64 // entries dropped on a full buffer
	sent    atomic.Uint64 // entries successfully written to the hub

	// capture state, set by worker.Run / captureLoop and self-reported to the
	// hub so a dead capture source is visible cluster-side, not just in logs.
	captureLive atomic.Bool // AF_PACKET source active
	captureTLS  atomic.Bool // eBPF TLS capture active

	mu   sync.Mutex
	conn *websocket.Conn
}

func newSink(hubURL, hubToken, node string, log *slog.Logger) *sink {
	return &sink{
		hubURL:   hubURL,
		hubToken: hubToken,
		node:     node,
		log:      log,
		ch:       make(chan *api.Entry, 1024),
	}
}

// emit queues an entry, dropping the incoming (newest) entry if the buffer is
// full so capture never blocks. Full-buffer drops are counted and logged every
// 1000 so they aren't completely invisible.
func (s *sink) emit(e *api.Entry) {
	select {
	case s.ch <- e:
	default:
		// buffer full — drop this (newest) entry to keep capture non-blocking
		if n := s.dropped.Add(1); n%1000 == 0 {
			s.log.Warn("hub sink buffer full, dropping entries", "dropped", n)
		}
	}
}

// run maintains the connection and drains the buffer until ctx is cancelled.
func (s *sink) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := s.connect(); err != nil {
			s.log.Debug("hub connect failed, retrying", "url", s.hubURL, "err", err)
			if !sleepCtx(ctx, 2*time.Second) {
				return
			}
			continue
		}
		s.log.Info("connected to hub", "url", s.hubURL)
		s.pump(ctx)
		s.log.Debug("hub connection lost, reconnecting")
		if !sleepCtx(ctx, time.Second) {
			return
		}
	}
}

// sleepCtx waits d, returning false if ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (s *sink) connect() error {
	u, err := url.Parse(s.hubURL)
	if err != nil {
		return err
	}
	var hdr http.Header
	if s.hubToken != "" {
		hdr = http.Header{"Authorization": {"Bearer " + s.hubToken}}
	}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), hdr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	hello, _ := json.Marshal(api.Envelope{
		Type:  api.MsgHello,
		Hello: &api.Hello{Node: s.node, Version: config.Ver()},
	})
	return conn.WriteMessage(websocket.TextMessage, hello)
}

// pump writes buffered entries (plus a periodic self-report frame) to the
// current connection until it errors or ctx is cancelled.
func (s *sink) pump(ctx context.Context) {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return
	}
	defer conn.Close()

	write := func(b []byte) error {
		conn.SetWriteDeadline(time.Now().Add(sinkWriteTimeout))
		return conn.WriteMessage(websocket.TextMessage, b)
	}

	stats := time.NewTicker(sinkStatsInterval)
	defer stats.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-stats.C:
			b, err := json.Marshal(api.Envelope{Type: api.MsgWorkerStats, WorkerStats: &api.WorkerStats{
				Node:        s.node,
				EntriesSent: s.sent.Load(),
				Dropped:     s.dropped.Load(),
				CaptureLive: s.captureLive.Load(),
				CaptureTLS:  s.captureTLS.Load(),
			}})
			if err != nil {
				continue
			}
			if write(b) != nil {
				return
			}
		case e := <-s.ch:
			b, err := json.Marshal(api.Envelope{Type: api.MsgEntry, Entry: e})
			if err != nil {
				continue
			}
			if write(b) != nil {
				// Requeue the entry we failed to send, then bail to reconnect.
				s.emit(e)
				return
			}
			s.sent.Add(1)
		}
	}
}
