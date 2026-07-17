package hub

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pablocolson/k8shark/pkg/api"
)

// Predicate reports whether an entry matches a compiled filter.
type Predicate func(*api.Entry) bool

const (
	// maxFilterLen bounds the raw expression length accepted by CompileFilter.
	// The filter endpoint (?filter=) is reachable unauthenticated, so cap the
	// input up front rather than lexing an arbitrarily large string.
	maxFilterLen = 4096
	// maxFilterDepth bounds parser recursion (nested parens / chained "not") so
	// a pathological expression can't overflow the goroutine stack and crash the
	// hub.
	maxFilterDepth = 64
)

// CompileFilter parses an IFL (k8shark filter language) expression into a
// Predicate. IFL is a small but real query language inspired by Kubeshark's
// KFL:
//
//	http.method == "GET" and response.status >= 500
//	protocol == "dns" or dst.namespace == "kube-system"
//	not (src.name contains "canary")
//	"checkout"                      # bare token = full-text substring match
//
// Supported operators: == != contains > < >= <= ; boolean and/or/not with
// parentheses. An empty expression matches everything.
func CompileFilter(expr string) (Predicate, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return func(*api.Entry) bool { return true }, nil
	}
	if len(expr) > maxFilterLen {
		return nil, fmt.Errorf("filter too long (%d bytes, max %d)", len(expr), maxFilterLen)
	}
	toks, err := lex(expr)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	pred, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("unexpected token %q", p.cur().val)
	}
	return pred, nil
}

// --- lexer -----------------------------------------------------------------

type tokKind int

const (
	tIdent tokKind = iota
	tString
	tNumber
	tOp
	tLParen
	tRParen
	tAnd
	tOr
	tNot
)

type token struct {
	kind tokKind
	val  string
}

func lex(s string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c == '(':
			toks = append(toks, token{tLParen, "("})
			i++
		case c == ')':
			toks = append(toks, token{tRParen, ")"})
			i++
		case c == '"' || c == '\'':
			quote := c
			j := i + 1
			var sb strings.Builder
			for j < len(s) && s[j] != quote {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				sb.WriteByte(s[j])
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("unterminated string")
			}
			toks = append(toks, token{tString, sb.String()})
			i = j + 1
		case strings.HasPrefix(s[i:], "=="), strings.HasPrefix(s[i:], "!="),
			strings.HasPrefix(s[i:], ">="), strings.HasPrefix(s[i:], "<="):
			toks = append(toks, token{tOp, s[i : i+2]})
			i += 2
		case c == '>' || c == '<':
			toks = append(toks, token{tOp, string(c)})
			i++
		default:
			// identifier / number / keyword run
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\n()=!<>\"'", rune(s[j])) {
				j++
			}
			word := s[i:j]
			if word == "" {
				return nil, fmt.Errorf("unexpected char %q", string(c))
			}
			switch strings.ToLower(word) {
			case "and":
				toks = append(toks, token{tAnd, word})
			case "or":
				toks = append(toks, token{tOr, word})
			case "not":
				toks = append(toks, token{tNot, word})
			case "contains":
				toks = append(toks, token{tOp, "contains"})
			default:
				if _, err := strconv.ParseFloat(word, 64); err == nil {
					toks = append(toks, token{tNumber, word})
				} else {
					toks = append(toks, token{tIdent, word})
				}
			}
			i = j
		}
	}
	return toks, nil
}

// --- parser (recursive descent) --------------------------------------------

type parser struct {
	toks  []token
	pos   int
	depth int // recursion depth guard (nested parens / chained "not")
}

func (p *parser) cur() token {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return token{tOp, ""}
}

func (p *parser) parseOr() (Predicate, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tOr {
		p.pos++
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(e *api.Entry) bool { return l(e) || r(e) }
	}
	return left, nil
}

func (p *parser) parseAnd() (Predicate, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tAnd {
		p.pos++
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(e *api.Entry) bool { return l(e) && r(e) }
	}
	return left, nil
}

func (p *parser) parseUnary() (Predicate, error) {
	if p.cur().kind == tNot {
		p.pos++
		p.depth++
		if p.depth > maxFilterDepth {
			return nil, fmt.Errorf("filter nesting too deep")
		}
		inner, err := p.parseUnary()
		p.depth--
		if err != nil {
			return nil, err
		}
		return func(e *api.Entry) bool { return !inner(e) }, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Predicate, error) {
	t := p.cur()
	switch t.kind {
	case tLParen:
		p.pos++
		p.depth++
		if p.depth > maxFilterDepth {
			return nil, fmt.Errorf("filter nesting too deep")
		}
		inner, err := p.parseOr()
		p.depth--
		if err != nil {
			return nil, err
		}
		if p.cur().kind != tRParen {
			return nil, fmt.Errorf("expected ')'")
		}
		p.pos++
		return inner, nil
	case tString, tNumber:
		// bare literal -> full-text match
		p.pos++
		needle := strings.ToLower(t.val)
		return func(e *api.Entry) bool { return strings.Contains(fulltext(e), needle) }, nil
	case tIdent:
		// Either "field op value" or a bare token (full-text).
		if p.pos+1 < len(p.toks) && p.toks[p.pos+1].kind == tOp {
			return p.parseComparison()
		}
		p.pos++
		needle := strings.ToLower(t.val)
		return func(e *api.Entry) bool { return strings.Contains(fulltext(e), needle) }, nil
	default:
		return nil, fmt.Errorf("unexpected token %q", t.val)
	}
}

func (p *parser) parseComparison() (Predicate, error) {
	field := p.cur().val
	p.pos++
	op := p.cur().val
	p.pos++
	if p.pos >= len(p.toks) {
		return nil, fmt.Errorf("expected value after %q", op)
	}
	valTok := p.cur()
	p.pos++
	val := valTok.val

	// namespace/ns matches either side (src or dst) rather than a single
	// struct field, so it can't go through the single-getter-then-compare
	// path below. == and contains are true if EITHER side matches
	// (inclusion, "show me shop traffic wherever it touches shop"); != is
	// true only if NEITHER side matches (exclusion, "hide kube-system
	// noise") — the useful reading, not the De Morgan-literal "either side
	// differs" (which would be true for nearly every entry).
	fieldLower := strings.ToLower(field)
	if fieldLower == "namespace" || fieldLower == "ns" {
		return func(e *api.Entry) bool {
			if op == "!=" {
				return !compare(e.Source.Namespace, "==", val) && !compare(e.Destination.Namespace, "==", val)
			}
			return compare(e.Source.Namespace, op, val) || compare(e.Destination.Namespace, op, val)
		}, nil
	}

	// An unknown field must be a compile error, not a silent match-nothing: a
	// typo like `http.status_code == 500` returning zero entries reads as "no
	// errors" to whoever wrote it.
	getter := fieldGetter(field)
	if getter == nil {
		return nil, fmt.Errorf("unknown filter field %q (GET /api/fields lists the catalog)", field)
	}
	return func(e *api.Entry) bool {
		return compare(getter(e), op, val)
	}, nil
}

// compare evaluates "actual op want". Numeric comparison is used when both
// sides parse as numbers; otherwise string comparison (case-insensitive).
func compare(actual, op, want string) bool {
	af, aerr := strconv.ParseFloat(actual, 64)
	wf, werr := strconv.ParseFloat(want, 64)
	numeric := aerr == nil && werr == nil
	switch op {
	case "==":
		return strings.EqualFold(actual, want)
	case "!=":
		return !strings.EqualFold(actual, want)
	case "contains":
		return strings.Contains(strings.ToLower(actual), strings.ToLower(want))
	case ">":
		return numeric && af > wf
	case "<":
		return numeric && af < wf
	case ">=":
		return numeric && af >= wf
	case "<=":
		return numeric && af <= wf
	}
	return false
}

// fieldGetter resolves a dotted field path to an accessor. Unknown fields
// return nil (rejected at filter compile; skipped by the facet index).
func fieldGetter(field string) func(*api.Entry) string {
	switch strings.ToLower(field) {
	case "namespace", "ns":
		// The real either-side match/exclude logic lives in parseComparison
		// (which intercepts "namespace"/"ns" before ever calling this), so
		// this getter is never used for actual comparisons — it exists only
		// so the facet index has something to sample for tracked-value
		// autocomplete. Falls back to dst so a namespace that's only ever a
		// destination (e.g. one that solely receives traffic) still shows up.
		return func(e *api.Entry) string {
			if e.Source.Namespace != "" {
				return e.Source.Namespace
			}
			return e.Destination.Namespace
		}
	case "protocol":
		return func(e *api.Entry) string { return string(e.Protocol) }
	case "node":
		return func(e *api.Entry) string { return e.Node }
	case "status":
		return func(e *api.Entry) string { return e.Status }
	case "elapsedms", "elapsed", "latency":
		return func(e *api.Entry) string { return strconv.FormatInt(e.ElapsedMs, 10) }
	case "src.ip":
		return func(e *api.Entry) string { return e.Source.IP }
	case "src.port":
		return func(e *api.Entry) string { return strconv.Itoa(e.Source.Port) }
	case "src.name":
		return func(e *api.Entry) string { return e.Source.Name }
	case "src.namespace", "src.ns":
		return func(e *api.Entry) string { return e.Source.Namespace }
	case "src.workload":
		return func(e *api.Entry) string { return e.Source.Workload }
	case "dst.ip":
		return func(e *api.Entry) string { return e.Destination.IP }
	case "dst.port":
		return func(e *api.Entry) string { return strconv.Itoa(e.Destination.Port) }
	case "dst.name":
		return func(e *api.Entry) string { return e.Destination.Name }
	case "dst.namespace", "dst.ns":
		return func(e *api.Entry) string { return e.Destination.Namespace }
	case "dst.workload":
		return func(e *api.Entry) string { return e.Destination.Workload }
	case "http.method", "request.method", "method":
		return func(e *api.Entry) string { return e.Request.Method }
	case "http.path", "request.path", "path":
		return func(e *api.Entry) string { return e.Request.Path }
	case "http.host", "request.host", "host":
		return func(e *api.Entry) string { return e.Request.Host }
	case "http.status", "response.status", "status.code", "statuscode":
		return func(e *api.Entry) string { return strconv.Itoa(e.StatusCode) }
	case "request.body":
		return func(e *api.Entry) string { return e.Request.Body }
	case "response.body":
		return func(e *api.Entry) string { return e.Response.Body }
	case "dns.question", "question":
		return func(e *api.Entry) string { return e.Request.Question }
	case "dns.answer", "answer":
		return func(e *api.Entry) string { return e.Response.Answer }
	case "redis.command", "command":
		return func(e *api.Entry) string { return e.Request.Command }
	case "postgres.query", "query", "sql":
		return func(e *api.Entry) string { return e.Request.Query }
	case "bytes":
		return func(e *api.Entry) string { return strconv.FormatInt(e.Request.Bytes, 10) }
	case "packets":
		return func(e *api.Entry) string { return strconv.FormatInt(e.Request.Packets, 10) }
	case "flags":
		return func(e *api.Entry) string { return e.Request.Flags }
	case "summary":
		return func(e *api.Entry) string { return e.Request.Summary + " " + e.Response.Summary }

	// --- richer sub-object fields (WS3) — all nil-guarded ------------------
	case "http.version":
		return func(e *api.Entry) string {
			if e.Request.HTTP != nil {
				return e.Request.HTTP.Version
			}
			return ""
		}
	case "response.contenttype", "content-type", "contenttype":
		return func(e *api.Entry) string {
			if e.Response.ContentType != "" {
				return e.Response.ContentType
			}
			return e.Request.ContentType
		}
	case "dns.rcode":
		return func(e *api.Entry) string {
			if e.Response.DNS != nil {
				return e.Response.DNS.Rcode
			}
			return ""
		}
	case "dns.type":
		return func(e *api.Entry) string {
			if e.Request.DNS != nil && len(e.Request.DNS.Questions) > 0 {
				return e.Request.DNS.Questions[0].Type
			}
			return ""
		}
	case "redis.db":
		return func(e *api.Entry) string {
			if e.Request.Redis != nil {
				return strconv.Itoa(e.Request.Redis.DBIndex)
			}
			return ""
		}
	case "redis.reply":
		return func(e *api.Entry) string {
			if e.Response.Redis != nil {
				return e.Response.Redis.Reply
			}
			return ""
		}
	case "postgres.error", "pg.code":
		return func(e *api.Entry) string {
			if e.Response.Postgres != nil && e.Response.Postgres.Error != nil {
				return e.Response.Postgres.Error.Code
			}
			return ""
		}
	case "postgres.statement":
		return func(e *api.Entry) string {
			if e.Request.Postgres != nil {
				return e.Request.Postgres.StatementName
			}
			return ""
		}
	case "postgres.txstatus":
		return func(e *api.Entry) string {
			if e.Response.Postgres != nil {
				return e.Response.Postgres.TxStatus
			}
			return ""
		}
	case "l4.ttl":
		return func(e *api.Entry) string { return l4Int(e, func(l *api.L4Info) int { return l.TTL }) }
	case "l4.retransmits":
		return func(e *api.Entry) string { return l4Int(e, func(l *api.L4Info) int { return l.Retransmits }) }
	case "l4.window":
		return func(e *api.Entry) string { return l4Int(e, func(l *api.L4Info) int { return l.Window }) }
	case "l4.mss":
		return func(e *api.Entry) string { return l4Int(e, func(l *api.L4Info) int { return l.MSS }) }
	case "l4.rttms":
		return func(e *api.Entry) string {
			if e.L4 != nil {
				return strconv.FormatFloat(e.L4.RTTMs, 'f', -1, 64)
			}
			return ""
		}
	case "l4.durationms":
		return func(e *api.Entry) string {
			if e.L4 != nil {
				return strconv.FormatInt(e.L4.DurationMs, 10)
			}
			return ""
		}
	case "l4.clientbytes":
		return func(e *api.Entry) string {
			if e.L4 != nil {
				return strconv.FormatInt(e.L4.ClientBytes, 10)
			}
			return ""
		}
	case "l4.serverbytes":
		return func(e *api.Entry) string {
			if e.L4 != nil {
				return strconv.FormatInt(e.L4.ServerBytes, 10)
			}
			return ""
		}
	case "tls.sni":
		return func(e *api.Entry) string {
			if e.L4 != nil && e.L4.TLS != nil {
				return e.L4.TLS.SNI
			}
			return ""
		}

	// --- AMQP (WS5) -------------------------------------------------------
	case "amqp.exchange", "exchange":
		return func(e *api.Entry) string { return e.Request.Exchange }
	case "amqp.routingkey", "amqp.routing-key", "routingkey", "routing-key":
		return func(e *api.Entry) string { return e.Request.RoutingKey }
	case "amqp.queue", "queue":
		return func(e *api.Entry) string { return e.Request.Queue }
	case "amqp.deliverytag", "deliverytag":
		return func(e *api.Entry) string { return strconv.FormatUint(e.Request.DeliveryTag, 10) }
	case "amqp.class":
		return func(e *api.Entry) string { return e.Request.Class }
	case "amqp.method":
		// Method is shared with HTTP; scope this to AMQP so the facet/filter
		// isn't polluted by HTTP verbs.
		return func(e *api.Entry) string {
			if e.Protocol == api.ProtocolAMQP {
				return e.Request.Method
			}
			return ""
		}

	// --- previously display-only fields, now filterable too ----------------
	case "redis.pipelinedepth":
		return func(e *api.Entry) string {
			if e.Request.Redis != nil {
				return strconv.Itoa(e.Request.Redis.PipelineDepth)
			}
			return ""
		}
	case "postgres.portal":
		return func(e *api.Entry) string {
			if e.Request.Postgres != nil {
				return e.Request.Postgres.Portal
			}
			return ""
		}
	case "dns.authoritative":
		return func(e *api.Entry) string {
			if e.Response.DNS != nil {
				return strconv.FormatBool(e.Response.DNS.Authoritative)
			}
			return ""
		}
	case "dns.recursionavailable", "dns.recursionavl":
		return func(e *api.Entry) string {
			if e.Response.DNS != nil {
				return strconv.FormatBool(e.Response.DNS.RecursionAvl)
			}
			return ""
		}
	case "request.size":
		return func(e *api.Entry) string { return strconv.Itoa(e.Request.Size) }
	case "response.size", "size":
		return func(e *api.Entry) string { return strconv.Itoa(e.Response.Size) }
	case "postgres.rowcount", "rowcount":
		return func(e *api.Entry) string { return strconv.Itoa(e.Response.RowCount) }
	case "http.ttfbms":
		return func(e *api.Entry) string {
			if e.Response.HTTP != nil {
				return strconv.FormatInt(e.Response.HTTP.TTFBMs, 10)
			}
			return ""
		}

	// --- remaining L4Info fields (previously view-only) ---------------------
	case "l4.srcmac":
		return func(e *api.Entry) string { return l4Str(e, func(l *api.L4Info) string { return l.SrcMAC }) }
	case "l4.dstmac":
		return func(e *api.Entry) string { return l4Str(e, func(l *api.L4Info) string { return l.DstMAC }) }
	case "l4.ipversion":
		return func(e *api.Entry) string { return l4Int(e, func(l *api.L4Info) int { return l.IPVersion }) }
	case "l4.ipflags":
		return func(e *api.Entry) string { return l4Str(e, func(l *api.L4Info) string { return l.IPFlags }) }
	case "l4.clienttcpflags":
		return func(e *api.Entry) string { return l4Str(e, func(l *api.L4Info) string { return l.ClientTCPFlags }) }
	case "l4.servertcpflags":
		return func(e *api.Entry) string { return l4Str(e, func(l *api.L4Info) string { return l.ServerTCPFlags }) }
	case "l4.seqstart":
		return func(e *api.Entry) string {
			if e.L4 == nil {
				return ""
			}
			return strconv.FormatUint(uint64(e.L4.SeqStart), 10)
		}
	case "l4.ackstart":
		return func(e *api.Entry) string {
			if e.L4 == nil {
				return ""
			}
			return strconv.FormatUint(uint64(e.L4.AckStart), 10)
		}
	case "l4.clientpackets":
		return func(e *api.Entry) string {
			if e.L4 == nil {
				return ""
			}
			return strconv.FormatInt(e.L4.ClientPackets, 10)
		}
	case "l4.serverpackets":
		return func(e *api.Entry) string {
			if e.L4 == nil {
				return ""
			}
			return strconv.FormatInt(e.L4.ServerPackets, 10)
		}

	default:
		return nil
	}
}

// l4Int reads an int field off e.L4, returning "" (not "0") when L4 is absent so
// numeric comparisons don't spuriously match on missing data.
func l4Int(e *api.Entry, get func(*api.L4Info) int) string {
	if e.L4 == nil {
		return ""
	}
	return strconv.Itoa(get(e.L4))
}

// l4Str reads a string field off e.L4, returning "" when L4 is absent.
func l4Str(e *api.Entry, get func(*api.L4Info) string) string {
	if e.L4 == nil {
		return ""
	}
	return get(e.L4)
}

// fulltext builds a lowercase haystack of an entry's salient fields for bare
// full-text matching.
func fulltext(e *api.Entry) string {
	var sb strings.Builder
	sb.WriteString(string(e.Protocol))
	sb.WriteByte(' ')
	sb.WriteString(e.Node)
	sb.WriteByte(' ')
	sb.WriteString(e.Source.IP)
	sb.WriteByte(' ')
	sb.WriteString(e.Source.Name)
	sb.WriteByte(' ')
	sb.WriteString(e.Destination.IP)
	sb.WriteByte(' ')
	sb.WriteString(e.Destination.Name)
	sb.WriteByte(' ')
	sb.WriteString(e.Request.Summary)
	sb.WriteByte(' ')
	sb.WriteString(e.Request.Method)
	sb.WriteByte(' ')
	sb.WriteString(e.Request.Path)
	sb.WriteByte(' ')
	sb.WriteString(e.Request.Host)
	sb.WriteByte(' ')
	sb.WriteString(e.Request.Question)
	sb.WriteByte(' ')
	sb.WriteString(e.Request.Command)
	sb.WriteByte(' ')
	sb.WriteString(e.Request.Query)
	sb.WriteByte(' ')
	sb.WriteString(e.Response.Summary)
	// Richer sub-object text (WS3), nil-guarded.
	if e.Request.HTTP != nil && e.Request.HTTP.ContentType != "" {
		sb.WriteByte(' ')
		sb.WriteString(e.Request.HTTP.ContentType)
	}
	if e.Response.DNS != nil {
		for _, a := range e.Response.DNS.Answers {
			sb.WriteByte(' ')
			sb.WriteString(a.Data)
		}
	}
	if e.Request.Postgres != nil && e.Request.Postgres.StatementName != "" {
		sb.WriteByte(' ')
		sb.WriteString(e.Request.Postgres.StatementName)
	}
	if e.Request.Exchange != "" || e.Request.RoutingKey != "" || e.Request.Queue != "" {
		sb.WriteByte(' ')
		sb.WriteString(e.Request.Exchange)
		sb.WriteByte(' ')
		sb.WriteString(e.Request.RoutingKey)
		sb.WriteByte(' ')
		sb.WriteString(e.Request.Queue)
	}
	if e.L4 != nil && e.L4.TLS != nil && e.L4.TLS.SNI != "" {
		sb.WriteByte(' ')
		sb.WriteString(e.L4.TLS.SNI)
	}
	return strings.ToLower(sb.String())
}
