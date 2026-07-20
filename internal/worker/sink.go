package worker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
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
	dialer   *websocket.Dialer // DefaultDialer unless setHubCA installed a custom root pool

	ch      chan *api.Entry
	dropped atomic.Uint64 // entries dropped on a full buffer
	sent    atomic.Uint64 // entries successfully written to the hub

	// capture state, set by worker.Run / captureLoop and self-reported to the
	// hub so a dead capture source is visible cluster-side, not just in logs.
	captureLive atomic.Bool // AF_PACKET source active
	captureTLS  atomic.Bool // eBPF TLS capture active

	// capturePaused is set remotely by the hub (MsgWorkerCommand, see
	// reader()) via POST /api/workers/capture. AF_PACKET/eBPF sources stay
	// open either way — route()/consumeTLS check this and drop what they
	// read before doing any reassembly/dissection work, so toggling it back
	// off is instant rather than needing a reconnect.
	capturePaused atomic.Bool

	// ringPackets/ringDrops mirror the AF_PACKET kernel ring's own cumulative
	// counters (captureLoop probes capture.PacketSource.Stats periodically).
	// Distinct from dropped above, which only counts entries lost after the
	// pipeline already turned them into dissected output — a rising
	// ringDrops means traffic was lost before the worker ever saw it.
	ringPackets atomic.Uint64
	ringDrops   atomic.Uint64

	// flowsEvicted counts generic L4 flows dropped by dissect_l4.go's
	// maxFlows cap (a burst of new connections between flushFlows cycles),
	// set directly by the pipeline via p.sink.flowsEvicted.
	flowsEvicted atomic.Uint64

	// tlsLagDrops counts eBPF TLS streams abandoned because backpressure
	// dropped one of their interior chunks (see ebpf.TLSRecord.Lagged) —
	// closed with a clean truncation instead of misparsing past a hole.
	tlsLagDrops atomic.Uint64

	mu   sync.Mutex
	conn *websocket.Conn
}

func newSink(hubURL, hubToken, node string, log *slog.Logger) *sink {
	return &sink{
		hubURL:   hubURL,
		hubToken: hubToken,
		node:     node,
		log:      log,
		dialer:   websocket.DefaultDialer,
		ch:       make(chan *api.Entry, 1024),
	}
}

// setHubCA installs a custom CA (PEM file) for verifying a wss:// hub
// certificate — needed when the hub's cert is issued by a private CA
// (cert-manager) rather than one in the system roots. Must be called before
// run.
func (s *sink) setHubCA(path string) error {
	pem, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return fmt.Errorf("no CA certificates found in %s", path)
	}
	d := *websocket.DefaultDialer
	d.TLSClientConfig = &tls.Config{RootCAs: pool}
	s.dialer = &d
	return nil
}

// paused reports whether the hub has told this worker to stop turning
// capture into entries. Checked by route() / consumeTLS on every
// packet/record, so it stays a plain atomic load.
func (s *sink) paused() bool {
	return s.capturePaused.Load()
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
	conn, _, err := s.dialer.Dial(u.String(), hdr)
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

// reader consumes control frames (currently just pause/resume) from the hub
// on conn until it closes. Runs concurrently with pump's writes — gorilla's
// websocket.Conn supports one concurrent reader alongside one concurrent
// writer, which is exactly this split.
func (s *sink) reader(conn *websocket.Conn) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var env api.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		if env.Type == api.MsgWorkerCommand && env.WorkerCommand != nil {
			s.capturePaused.Store(env.WorkerCommand.Paused)
			s.log.Info("capture pause state set by hub", "paused", env.WorkerCommand.Paused)
		}
	}
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
	go s.reader(conn)

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
				Node:          s.node,
				EntriesSent:   s.sent.Load(),
				Dropped:       s.dropped.Load(),
				CaptureLive:   s.captureLive.Load(),
				CaptureTLS:    s.captureTLS.Load(),
				CapturePaused: s.capturePaused.Load(),
				RingPackets:   s.ringPackets.Load(),
				RingDrops:     s.ringDrops.Load(),
				FlowsEvicted:  s.flowsEvicted.Load(),
				TLSLagDrops:   s.tlsLagDrops.Load(),
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
