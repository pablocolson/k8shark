package worker

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/gopacket"
	"github.com/pablocolson/k8shark/pkg/api"
)

// AMQP 0-9-1 frame types and bounds.
const (
	amqpFrameEnd       = 0xCE
	amqpFrameMethod    = 1
	amqpFrameHeader    = 2
	amqpFrameBody      = 3
	amqpFrameHeartbeat = 8
	amqpMaxFrame       = 16 << 20 // guard vs misparse/TLS; real frame-max default is 131072
)

// AMQP class IDs.
const (
	amqpClassConnection = 10
	amqpClassChannel    = 20
	amqpClassExchange   = 40
	amqpClassQueue      = 50
	amqpClassBasic      = 60
	amqpClassTx         = 90
)

var errAMQPFrame = errors.New("amqp: bad frame")

// consumeAMQP dissects one direction of an AF_PACKET-discovered AMQP 0-9-1
// connection. Thin wrapper over consumeAMQPID (see conn.go).
func (p *pipeline) consumeAMQP(netFlow, transport gopacket.Flow, r io.Reader, isClient bool) {
	p.consumeAMQPID(connIDFromFlows(netFlow, transport), r, isClient)
}

// consumeAMQPID dissects one direction of an AMQP 0-9-1 connection. isClient
// is true for the client->broker direction (dst == 5672). AMQP is
// asynchronous and channel-multiplexed, so methods are emitted as standalone
// entries (like the Redis-push model) rather than FIFO-paired. Only AMQP
// 0-9-1 is handled; a 1.0 (or TLS) stream is detected and skipped without
// emitting garbage. AMQPS (port 5671) is out of scope for the eBPF TLS layer
// too — it is not fed through here.
func (p *pipeline) consumeAMQPID(c connID, r io.Reader, isClient bool) {
	r, cr := p.capture(r)
	br := bufio.NewReader(r)

	// The client opens with an 8-byte protocol header "AMQP" + version. Consume
	// it (and bail on non-0-9-1 versions). The server direction has no header.
	if isClient {
		if peek, err := br.Peek(8); err == nil && string(peek[:4]) == "AMQP" {
			if !(peek[4] == 0x00 && peek[5] == 0x00 && peek[6] == 0x09 && peek[7] == 0x01) {
				p.log.Debug("amqp: unsupported protocol version, skipping", "version", peek[4:8])
				io.Copy(io.Discard, br)
				return
			}
			br.Discard(8)
		}
	}

	channels := map[uint16]*amqpPending{} // per-channel content assembly
	for {
		ftype, ch, payload, err := readAMQPFrame(br)
		if err != nil {
			io.Copy(io.Discard, br)
			return
		}
		switch ftype {
		case amqpFrameMethod:
			classID, methodID, args, ok := parseAMQPMethod(payload)
			if !ok {
				continue
			}
			info, surfaced := parseAMQPArgs(classID, methodID, args)
			if !surfaced {
				continue // skip handshake/*Ok chatter
			}
			if info.content {
				channels[ch] = &amqpPending{info: info}
			} else {
				p.emitAMQPMethod(isClient, c, cr, info)
			}
		case amqpFrameHeader:
			if pend := channels[ch]; pend != nil {
				if len(payload) >= 12 {
					pend.bodySize = binary.BigEndian.Uint64(payload[4:12])
				}
				if pend.bodySize == 0 {
					p.emitAMQPContent(isClient, c, cr, pend)
					delete(channels, ch)
				}
			}
		case amqpFrameBody:
			if pend := channels[ch]; pend != nil {
				pend.total += len(payload)
				if room := p.bodyCap - len(pend.body); room > 0 {
					take := len(payload)
					if take > room {
						take = room
					}
					pend.body = append(pend.body, payload[:take]...)
				}
				if uint64(pend.total) >= pend.bodySize {
					p.emitAMQPContent(isClient, c, cr, pend)
					delete(channels, ch)
				}
			}
		case amqpFrameHeartbeat:
			continue
		}
	}
}

// amqpMethodInfo is the extracted, surfaced fields of one AMQP method.
type amqpMethodInfo struct {
	class, method                           string
	exchange, routingKey, queue             string
	consumerTag, exchType, vhost, replyText string
	deliveryTag                             uint64
	replyCode                               uint16
	content                                 bool // Publish/Deliver/Return/GetOk carry a message body
}

// amqpPending buffers a content method until its header+body frames arrive.
type amqpPending struct {
	info     amqpMethodInfo
	bodySize uint64
	total    int    // true accumulated body length (for completion detection)
	body     []byte // stored body, capped at p.bodyCap
}

// readAMQPFrame reads one frame: type(1) channel(2) size(4) payload(size)
// frame-end(1)==0xCE. It mirrors readPGMessage's bounded-read discipline.
func readAMQPFrame(br *bufio.Reader) (ftype byte, channel uint16, payload []byte, err error) {
	var hdr [7]byte
	if _, err = io.ReadFull(br, hdr[:]); err != nil {
		return 0, 0, nil, err
	}
	ftype = hdr[0]
	channel = binary.BigEndian.Uint16(hdr[1:3])
	size := binary.BigEndian.Uint32(hdr[3:7])
	if size > amqpMaxFrame {
		return 0, 0, nil, errAMQPFrame
	}
	payload = make([]byte, size)
	if _, err = io.ReadFull(br, payload); err != nil {
		return 0, 0, nil, err
	}
	end, err := br.ReadByte()
	if err != nil {
		return 0, 0, nil, err
	}
	if end != amqpFrameEnd {
		return 0, 0, nil, errAMQPFrame
	}
	return ftype, channel, payload, nil
}

// parseAMQPMethod splits a METHOD frame payload into class/method IDs + args.
func parseAMQPMethod(payload []byte) (classID, methodID uint16, args []byte, ok bool) {
	if len(payload) < 4 {
		return 0, 0, nil, false
	}
	classID = binary.BigEndian.Uint16(payload[0:2])
	methodID = binary.BigEndian.Uint16(payload[2:4])
	return classID, methodID, payload[4:], true
}

// parseAMQPArgs extracts the surfaced fields for the method set in §2.5. It
// returns ok=false for methods we deliberately skip (handshake, *Ok chatter).
func parseAMQPArgs(classID, methodID uint16, args []byte) (amqpMethodInfo, bool) {
	class, method := amqpMethodName(classID, methodID)
	if class == "" {
		return amqpMethodInfo{}, false
	}
	info := amqpMethodInfo{class: class, method: method}
	o := 0
	switch {
	case classID == amqpClassBasic && methodID == 40: // Publish
		_, o = amqpShort(args, o) // reserved-1
		info.exchange, o = amqpShortStr(args, o)
		info.routingKey, o = amqpShortStr(args, o)
		info.content = true
	case classID == amqpClassBasic && methodID == 60: // Deliver
		info.consumerTag, o = amqpShortStr(args, o)
		info.deliveryTag, o = amqpLongLong(args, o)
		o = amqpSkipBits(args, o) // redelivered
		info.exchange, o = amqpShortStr(args, o)
		info.routingKey, o = amqpShortStr(args, o)
		info.content = true
	case classID == amqpClassBasic && methodID == 50: // Return
		info.replyCode, o = amqpShort(args, o)
		info.replyText, o = amqpShortStr(args, o)
		info.exchange, o = amqpShortStr(args, o)
		info.routingKey, o = amqpShortStr(args, o)
		info.content = true
	case classID == amqpClassBasic && methodID == 71: // GetOk
		info.deliveryTag, o = amqpLongLong(args, o)
		o = amqpSkipBits(args, o) // redelivered
		info.exchange, o = amqpShortStr(args, o)
		info.routingKey, o = amqpShortStr(args, o)
		_, o = amqpLong(args, o) // message-count
		info.content = true
	case classID == amqpClassBasic && methodID == 72: // GetEmpty
		// no args
	case classID == amqpClassBasic && methodID == 80: // Ack
		info.deliveryTag, o = amqpLongLong(args, o)
	case classID == amqpClassBasic && methodID == 120: // Nack
		info.deliveryTag, o = amqpLongLong(args, o)
	case classID == amqpClassBasic && methodID == 20: // Consume
		_, o = amqpShort(args, o) // reserved-1
		info.queue, o = amqpShortStr(args, o)
	case classID == amqpClassQueue && methodID == 10: // Queue.Declare
		_, o = amqpShort(args, o) // reserved-1
		info.queue, o = amqpShortStr(args, o)
	case classID == amqpClassQueue && methodID == 20: // Queue.Bind
		_, o = amqpShort(args, o) // reserved-1
		info.queue, o = amqpShortStr(args, o)
		info.exchange, o = amqpShortStr(args, o)
		info.routingKey, o = amqpShortStr(args, o)
	case classID == amqpClassExchange && methodID == 10: // Exchange.Declare
		_, o = amqpShort(args, o) // reserved-1
		info.exchange, o = amqpShortStr(args, o)
		info.exchType, o = amqpShortStr(args, o)
	case classID == amqpClassConnection && methodID == 40: // Connection.Open
		info.vhost, o = amqpShortStr(args, o)
	case classID == amqpClassConnection && methodID == 50: // Connection.Close
		info.replyCode, o = amqpShort(args, o)
		info.replyText, o = amqpShortStr(args, o)
	case classID == amqpClassChannel && methodID == 10: // Channel.Open
		// reserved
	case classID == amqpClassChannel && methodID == 40: // Channel.Close
		info.replyCode, o = amqpShort(args, o)
		info.replyText, o = amqpShortStr(args, o)
	}
	_ = o
	return info, true
}

// amqpMethodName returns the class/method names for the surfaced set, or ""/""
// for anything we skip.
func amqpMethodName(classID, methodID uint16) (class, method string) {
	switch classID {
	case amqpClassConnection:
		switch methodID {
		case 40:
			return "Connection", "Open"
		case 50:
			return "Connection", "Close"
		}
	case amqpClassChannel:
		switch methodID {
		case 10:
			return "Channel", "Open"
		case 40:
			return "Channel", "Close"
		}
	case amqpClassExchange:
		if methodID == 10 {
			return "Exchange", "Declare"
		}
	case amqpClassQueue:
		switch methodID {
		case 10:
			return "Queue", "Declare"
		case 20:
			return "Queue", "Bind"
		}
	case amqpClassBasic:
		switch methodID {
		case 20:
			return "Basic", "Consume"
		case 40:
			return "Basic", "Publish"
		case 50:
			return "Basic", "Return"
		case 60:
			return "Basic", "Deliver"
		case 71:
			return "Basic", "GetOk"
		case 72:
			return "Basic", "GetEmpty"
		case 80:
			return "Basic", "Ack"
		case 120:
			return "Basic", "Nack"
		}
	}
	return "", ""
}

// amqpSummary renders the one-line summary for a method (bodySize known only for
// content methods; 0 otherwise).
func amqpSummary(info amqpMethodInfo, bodySize uint64) string {
	switch info.class + "." + info.method {
	case "Basic.Publish":
		return fmt.Sprintf("PUBLISH %s/%s (%d B)", info.exchange, info.routingKey, bodySize)
	case "Basic.Deliver":
		return fmt.Sprintf("DELIVER %s/%s tag=%d", info.exchange, info.routingKey, info.deliveryTag)
	case "Basic.Return":
		return fmt.Sprintf("RETURN %d %s/%s", info.replyCode, info.exchange, info.routingKey)
	case "Basic.GetOk":
		return fmt.Sprintf("GET-OK %s/%s tag=%d", info.exchange, info.routingKey, info.deliveryTag)
	case "Basic.GetEmpty":
		return "GET-EMPTY"
	case "Basic.Ack":
		return fmt.Sprintf("ACK tag=%d", info.deliveryTag)
	case "Basic.Nack":
		return fmt.Sprintf("NACK tag=%d", info.deliveryTag)
	case "Basic.Consume":
		return "CONSUME " + info.queue
	case "Queue.Declare":
		return "QUEUE.DECLARE " + info.queue
	case "Queue.Bind":
		return fmt.Sprintf("QUEUE.BIND %s -> %s/%s", info.queue, info.exchange, info.routingKey)
	case "Exchange.Declare":
		return fmt.Sprintf("EXCHANGE.DECLARE %s (%s)", info.exchange, info.exchType)
	case "Connection.Open":
		return "CONNECTION.OPEN " + info.vhost
	case "Connection.Close":
		return fmt.Sprintf("CONNECTION.CLOSE %d %s", info.replyCode, info.replyText)
	case "Channel.Open":
		return "CHANNEL.OPEN"
	case "Channel.Close":
		return fmt.Sprintf("CHANNEL.CLOSE %d %s", info.replyCode, info.replyText)
	}
	return info.class + "." + info.method
}

// amqpStatus classifies an AMQP method: errors for abnormal closes, unroutable
// returns and negative acks. AMQP reply-success is 200, so any Close/Return with
// a reply-code >= 300 (e.g. 320 connection-forced, 312 no-route) is an error.
func amqpStatus(info amqpMethodInfo) string {
	switch {
	case (info.class == "Connection" || info.class == "Channel") && info.method == "Close" && info.replyCode >= 300:
		return "error"
	case info.class == "Basic" && info.method == "Return" && info.replyCode >= 300:
		return "error"
	case info.class == "Basic" && info.method == "Nack":
		return "error"
	default:
		return "success"
	}
}

func (p *pipeline) emitAMQPMethod(isClient bool, c connID, cr *capReader, info amqpMethodInfo) {
	summary := amqpSummary(info, 0)
	req := amqpPayload(info, summary, "", 0)
	req.Raw = rawOf(cr)
	resp := api.Payload{Summary: summary}
	if info.replyText != "" {
		resp.Summary = info.replyText
	}
	p.emitAMQP(isClient, c, req, resp, amqpStatus(info))
}

func (p *pipeline) emitAMQPContent(isClient bool, c connID, cr *capReader, pend *amqpPending) {
	summary := amqpSummary(pend.info, pend.bodySize)
	req := amqpPayload(pend.info, summary, safeBody(string(pend.body)), int(pend.bodySize))
	req.Raw = rawOf(cr)
	p.emitAMQP(isClient, c, req, api.Payload{Summary: summary}, amqpStatus(pend.info))
}

// amqpPayload builds the request Payload for an AMQP entry.
func amqpPayload(info amqpMethodInfo, summary, body string, size int) api.Payload {
	return api.Payload{
		Class:       info.class,
		Method:      info.method,
		Exchange:    info.exchange,
		RoutingKey:  info.routingKey,
		Queue:       info.queue,
		DeliveryTag: info.deliveryTag,
		Summary:     summary,
		Body:        body,
		Size:        size,
	}
}

// emitAMQP emits a standalone entry oriented by the capture direction.
func (p *pipeline) emitAMQP(isClient bool, c connID, req, resp api.Payload, status string) {
	cli, brk := c.endpoints()
	src, dst := cli, brk
	if !isClient {
		src, dst = brk, cli
	}
	p.sink.emit(&api.Entry{
		ID:          p.node + "-amqp-" + strconv.FormatUint(p.seq.Add(1), 36),
		Protocol:    api.ProtocolAMQP,
		Timestamp:   time.Now(),
		Node:        p.node,
		Source:      src,
		Destination: dst,
		Request:     req,
		Response:    resp,
		Status:      status,
	})
}

// --- bounds-checked AMQP primitive readers ----------------------------------
// Each returns the value and the new offset; on a short buffer it returns the
// zero value and an offset >= len(b) so the caller stops advancing (never panic).

func amqpShort(b []byte, off int) (uint16, int) {
	if off < 0 || off+2 > len(b) {
		return 0, len(b)
	}
	return binary.BigEndian.Uint16(b[off:]), off + 2
}

func amqpLong(b []byte, off int) (uint32, int) {
	if off < 0 || off+4 > len(b) {
		return 0, len(b)
	}
	return binary.BigEndian.Uint32(b[off:]), off + 4
}

func amqpLongLong(b []byte, off int) (uint64, int) {
	if off < 0 || off+8 > len(b) {
		return 0, len(b)
	}
	return binary.BigEndian.Uint64(b[off:]), off + 8
}

func amqpShortStr(b []byte, off int) (string, int) {
	if off < 0 || off+1 > len(b) {
		return "", len(b)
	}
	n := int(b[off])
	off++
	if off+n > len(b) {
		return "", len(b)
	}
	return string(b[off : off+n]), off + n
}

func amqpLongStr(b []byte, off int) (string, int) {
	if off < 0 || off+4 > len(b) {
		return "", len(b)
	}
	n := int(binary.BigEndian.Uint32(b[off:]))
	off += 4
	if n < 0 || off+n > len(b) {
		return "", len(b)
	}
	return string(b[off : off+n]), off + n
}

// amqpSkipBits advances over one packed-bit octet (bits share an octet; a
// non-bit arg after bits starts a fresh octet).
func amqpSkipBits(b []byte, off int) int {
	if off < len(b) {
		return off + 1
	}
	return off
}
