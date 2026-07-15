//go:build linux

package ebpf

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	cebpf "github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
)

// eventHeaderSize is the byte offset of struct event's data[] field in
// bpf/tls.bpf.c — see that file's field-order comment. Kept in sync by hand
// since this package decodes ring buffer records itself rather than trusting
// bpf2go's -type codegen (whose exact generated field names we can't inspect
// without a working bpf target toolchain, which macOS's Apple clang lacks —
// see gen.go).
const (
	eventOffPID       = 0
	eventOffTID       = 4
	eventOffSSLCtx    = 8
	eventOffSAddr     = 16
	eventOffDAddr     = 20
	eventOffDataLen   = 24
	eventOffSPort     = 28
	eventOffDPort     = 30
	eventOffDirection = 32
	eventOffData      = 33
)

func decodeEvent(raw []byte) (TLSRecord, error) {
	if len(raw) < eventOffData {
		return TLSRecord{}, fmt.Errorf("ebpf: short ring buffer record (%d bytes)", len(raw))
	}
	dataLen := binary.LittleEndian.Uint32(raw[eventOffDataLen:])
	end := eventOffData + int(dataLen)
	if end > len(raw) {
		end = len(raw) // defensive: never index past what the kernel actually gave us
	}
	rec := TLSRecord{
		PID:       binary.LittleEndian.Uint32(raw[eventOffPID:]),
		TID:       binary.LittleEndian.Uint32(raw[eventOffTID:]),
		ConnID:    binary.LittleEndian.Uint64(raw[eventOffSSLCtx:]),
		Direction: TLSDirection(raw[eventOffDirection]),
		SrcPort:   binary.LittleEndian.Uint16(raw[eventOffSPort:]),
		DstPort:   binary.LittleEndian.Uint16(raw[eventOffDPort:]),
	}
	// saddr/daddr are network-order IPv4 as the kernel stored them; a zero
	// address means the tcp_sendmsg/tcp_recvmsg kprobe hasn't resolved this
	// thread's socket yet (the Go side then keeps the synthetic pid:<n>
	// endpoint).
	if sa := binary.BigEndian.Uint32(raw[eventOffSAddr : eventOffSAddr+4]); sa != 0 {
		rec.SrcIP = ipv4String(raw[eventOffSAddr : eventOffSAddr+4])
	}
	if da := binary.BigEndian.Uint32(raw[eventOffDAddr : eventOffDAddr+4]); da != 0 {
		rec.DstIP = ipv4String(raw[eventOffDAddr : eventOffDAddr+4])
	}
	if end > eventOffData {
		rec.Data = append([]byte(nil), raw[eventOffData:end]...)
	}
	return rec, nil
}

// ipv4String formats 4 network-order bytes as a dotted-quad.
func ipv4String(b []byte) string {
	return net.IP(b[:4]).To4().String()
}

// probeNames maps our stable program-name strings (used by attach.go's
// sslSymbols table) to the *cebpf.Program the collection loaded for the
// matching SEC() in bpf/tls.bpf.c. Isolating the bpf2go-generated field names
// to this one function keeps attach.go decoupled from codegen naming.
func probeNames(objs *tlsObjects) map[string]*cebpf.Program {
	return map[string]*cebpf.Program{
		"uprobe_ssl_write":       objs.UprobeSslWrite,
		"uprobe_ssl_write_ex":    objs.UprobeSslWriteEx,
		"uretprobe_ssl_write_ex": objs.UretprobeSslWriteEx,
		"uprobe_ssl_read":        objs.UprobeSslRead,
		"uretprobe_ssl_read":     objs.UretprobeSslRead,
		"uprobe_ssl_read_ex":     objs.UprobeSslReadEx,
		"uretprobe_ssl_read_ex":  objs.UretprobeSslReadEx,
	}
}

// linuxSource is the real Source: a loaded BPF collection, a ring buffer
// reader draining it, and a rescanning attacher discovering new uprobe
// targets as pods churn.
type linuxSource struct {
	cfg  Config
	objs tlsObjects
	rd   *ringbuf.Reader

	out chan TLSRecord

	mu       sync.Mutex
	attached map[string]bool // devIno -> attached, guards re-attach on rescan
	links    []link.Link

	closeOnce sync.Once
	stop      chan struct{}
	wg        sync.WaitGroup
}

func newSource(cfg Config) (Source, error) {
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("ebpf: remove memlock rlimit: %w", err)
	}

	s := &linuxSource{
		cfg:      cfg,
		out:      make(chan TLSRecord, 4096),
		attached: map[string]bool{},
		stop:     make(chan struct{}),
	}
	if err := loadTlsObjects(&s.objs, nil); err != nil {
		var ve *cebpf.VerifierError
		if errors.As(err, &ve) {
			return nil, fmt.Errorf("ebpf: load program (verifier): %w\n%+v", err, ve)
		}
		return nil, fmt.Errorf("ebpf: load program: %w", err)
	}

	rd, err := ringbuf.NewReader(s.objs.Events)
	if err != nil {
		s.objs.Close()
		return nil, fmt.Errorf("ebpf: open ring buffer: %w", err)
	}
	s.rd = rd
	return s, nil
}

func (s *linuxSource) Records() <-chan TLSRecord { return s.out }

// Attach starts the ring-buffer-drain goroutine and the periodic
// discoverTargets/attach scan. It never returns an error for "no targets
// found yet" (pods matching TLS libraries may not exist at startup) — only
// for conditions that make capture structurally impossible.
func (s *linuxSource) Attach() error {
	// Global kprobes for 4-tuple resolution (Phase 2b): attached once, not
	// per-pid. A failure here is non-fatal — the SSL uprobes still work, the
	// records just keep their synthetic pid:<n> endpoints.
	for fn, prog := range map[string]*cebpf.Program{
		"tcp_sendmsg": s.objs.KprobeTcpSendmsg,
		"tcp_recvmsg": s.objs.KprobeTcpRecvmsg,
	} {
		if prog == nil {
			continue
		}
		l, err := link.Kprobe(fn, prog, nil)
		if err != nil {
			s.cfg.Log.Warn("ebpf: kprobe attach failed (endpoints stay synthetic)", "fn", fn, "err", err)
			continue
		}
		s.mu.Lock()
		s.links = append(s.links, l)
		s.mu.Unlock()
	}

	s.wg.Add(2)
	go s.drainLoop()
	go s.scanLoop()
	return nil
}

// drainLoop copies ring buffer records into s.out, dropping the oldest
// buffered record on backpressure (mirrors sink.emit's drop-oldest policy —
// a slow/dead hub connection must never make the worker's memory grow
// unbounded, and a stalled consumer here must never block uprobe delivery).
func (s *linuxSource) drainLoop() {
	defer s.wg.Done()
	for {
		rec, err := s.rd.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) {
				return
			}
			s.cfg.Log.Warn("ebpf: ring buffer read error", "err", err)
			continue
		}
		ev, err := decodeEvent(rec.RawSample)
		if err != nil {
			s.cfg.Log.Debug("ebpf: drop malformed record", "err", err)
			continue
		}
		select {
		case s.out <- ev:
		default:
			select {
			case <-s.out:
			default:
			}
			select {
			case s.out <- ev:
			default:
			}
		}
	}
}

// scanLoop runs discoverTargets/attachTarget immediately and then every
// cfg.Rescan, so pods started after the worker are picked up without a
// restart.
func (s *linuxSource) scanLoop() {
	defer s.wg.Done()
	s.rescan()
	t := time.NewTicker(s.cfg.Rescan)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.rescan()
		}
	}
}

func (s *linuxSource) rescan() {
	targets, err := discoverTargets(s.cfg.ProcRoot, s.cfg.Log)
	if err != nil {
		s.cfg.Log.Warn("ebpf: discover targets", "err", err)
		return
	}
	prog := probeNames(&s.objs)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range targets {
		if s.attached[t.devIno] {
			continue
		}
		links, err := attachTarget(prog, t, s.cfg.Log)
		if err != nil {
			s.cfg.Log.Debug("ebpf: attach failed", "lib", t.pathname, "pid", t.pid, "err", err)
			// Not marked attached: retry on the next rescan (the process
			// might still be starting up, e.g. its libssl mapping raced us).
			continue
		}
		s.attached[t.devIno] = true
		s.links = append(s.links, links...)
		s.cfg.Log.Info("ebpf: attached TLS uprobes", "lib", t.pathname, "pid", t.pid, "hooks", len(links))
	}
}

func (s *linuxSource) Close() error {
	s.closeOnce.Do(func() {
		close(s.stop)
		_ = s.rd.Close() // unblocks drainLoop's Read()
		s.wg.Wait()

		s.mu.Lock()
		for _, l := range s.links {
			_ = l.Close()
		}
		s.mu.Unlock()

		s.objs.Close()
		close(s.out)
	})
	return nil
}
