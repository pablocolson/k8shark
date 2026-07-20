// Package mcp implements a Model Context Protocol server that exposes the hub's
// captured L7 traffic to AI agents. It hand-rolls the MCP wire protocol
// (JSON-RPC 2.0 over newline-delimited stdio) so it pulls in no extra
// dependencies. stdout is reserved for the protocol; all logs go to stderr.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pablocolson/k8shark/internal/config"
	"github.com/pablocolson/k8shark/pkg/api"
)

// protocolVersion is the MCP revision this server speaks.
const protocolVersion = "2024-11-05"

// serverInstructions is returned from initialize to steer an agent toward an
// efficient investigation order instead of pulling raw entries first.
const serverInstructions = "Start with get_stats or get_traffic_summary for the current-state overview " +
	"(counts, error rates, latency percentiles per workload/namespace) — cheaper and more informative than " +
	"raw entries. Call list_filter_fields before writing a non-trivial IFL filter (unknown fields are " +
	"rejected, not silently ignored). Use list_entries to dig into a specific slice once you know what " +
	"you're looking for, and get_entry for one entry's full detail (headers, bodies, timings). " +
	"find_error_clusters and diff_traffic answer \"what's failing\" and \"what changed\" directly, without " +
	"needing to page through entries yourself."

// Server exposes the hub REST API to an AI agent over MCP/stdio.
type Server struct {
	hubURL       string
	hubToken     string // bearer token for the hub API ("" = no auth)
	allowCapture bool
	log          *slog.Logger
	http         *http.Client
	tools        []*tool
}

// tool is one registry entry: its advertised schema plus a handler that turns
// arguments into a text result.
type tool struct {
	name        string
	description string
	inputSchema map[string]any
	handler     func(ctx context.Context, args map[string]any) (string, error)
	// mutating marks a tool that changes state (currently only start_pcap);
	// everything else only reads from the hub. Surfaced as
	// annotations.readOnlyHint so MCP clients can auto-approve read-only
	// calls without a confirmation prompt.
	mutating bool
}

// New builds an MCP server that talks to the hub at hubURL, authenticating
// with hubToken when non-empty. When allowCapture is true the (placeholder)
// PCAP capture tool is registered as well. log must write to stderr — stdout
// is the protocol channel.
func New(hubURL, hubToken string, allowCapture bool, log *slog.Logger) *Server {
	s := &Server{
		hubURL:       strings.TrimRight(hubURL, "/"),
		hubToken:     hubToken,
		allowCapture: allowCapture,
		log:          log,
		http:         &http.Client{Timeout: 10 * time.Second},
	}
	s.registerTools()
	return s
}

// --- JSON-RPC wire types ---------------------------------------------------

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// toolDef is the shape advertised by tools/list.
type toolDef struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	InputSchema map[string]any   `json:"inputSchema"`
	Annotations *toolAnnotations `json:"annotations,omitempty"`
}

// toolAnnotations carries MCP's tool behavior hints (2025-03-26 revision;
// harmless extra fields for a 2024-11-05 client, which ignores what it
// doesn't recognize).
type toolAnnotations struct {
	ReadOnlyHint bool `json:"readOnlyHint"`
}

// ServeStdio runs the JSON-RPC loop, reading one request per line from stdin
// and writing one response per line to stdout, until stdin reaches EOF or ctx
// is cancelled. It returns nil on a clean shutdown.
func (s *Server) ServeStdio(ctx context.Context) error {
	return s.serve(ctx, os.Stdin, os.Stdout)
}

// serve is ServeStdio over injectable streams (tested with in-memory pipes).
// Each request is dispatched on its own goroutine, so a slow tools/call (the
// hub HTTP timeout is 10s) can't block a concurrent ping or another call —
// JSON-RPC matches responses by id, ordering is not part of the contract — and
// a mutex serializes writes so concurrent responses can't interleave on the
// wire.
func (s *Server) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.log.Info("mcp server starting", "hub", s.hubURL, "allowCapture", s.allowCapture, "tools", len(s.tools))

	reader := bufio.NewReader(in)
	enc := json.NewEncoder(out)
	var wmu sync.Mutex
	write := func(resp rpcResponse) {
		wmu.Lock()
		defer wmu.Unlock()
		if err := enc.Encode(resp); err != nil {
			s.log.Error("writing response", "err", err)
		}
	}

	// Read input on a goroutine so the loop can still observe ctx cancellation
	// while a read is blocked.
	lines := make(chan string)
	go func() {
		defer close(lines)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					s.log.Error("reading stdin", "err", err)
				}
				return
			}
		}
	}()

	var wg sync.WaitGroup
	defer wg.Wait() // in-flight handlers finish (and respond) before returning
	for {
		select {
		case <-ctx.Done():
			s.log.Info("mcp server shutting down")
			return nil
		case line, ok := <-lines:
			if !ok {
				s.log.Info("stdin closed, exiting")
				return nil
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.handleLine(ctx, line, write)
			}()
		}
	}
}

// handleLine parses one line and, when it is a request (has an id), writes a
// single response via write. Notifications produce no output; an unparseable
// line gets the JSON-RPC parse error (-32700, id null — or -32600 when the
// JSON itself is valid but isn't a request object) so a client blocked
// waiting on its response fails fast instead of hanging forever.
func (s *Server) handleLine(ctx context.Context, line string, write func(rpcResponse)) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var req rpcRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		s.log.Warn("malformed JSON-RPC line", "err", err)
		if json.Valid([]byte(line)) {
			write(s.fail(json.RawMessage("null"), -32600, "invalid request"))
		} else {
			write(s.fail(json.RawMessage("null"), -32700, "parse error"))
		}
		return
	}
	// Notifications carry no id and must never be answered.
	if len(req.ID) == 0 {
		s.log.Debug("notification", "method", req.Method)
		return
	}
	write(s.dispatch(ctx, req))
}

// dispatch routes a request to the matching method handler.
func (s *Server) dispatch(ctx context.Context, req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		return s.ok(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": config.Name, "version": config.Ver()},
			"instructions":    serverInstructions,
		})
	case "ping":
		return s.ok(req.ID, map[string]any{})
	case "tools/list":
		return s.ok(req.ID, map[string]any{"tools": s.toolDefs()})
	case "tools/call":
		return s.callTool(ctx, req)
	default:
		return s.fail(req.ID, -32601, "method not found")
	}
}

// callTool dispatches a tools/call. Tool failures are reported as a successful
// JSON-RPC result carrying isError:true (per MCP convention), not as a
// protocol-level error object.
func (s *Server) callTool(ctx context.Context, req rpcRequest) rpcResponse {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return s.ok(req.ID, toolError(fmt.Sprintf("invalid params: %v", err)))
		}
	}
	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}
	t := s.lookup(p.Name)
	if t == nil {
		return s.ok(req.ID, toolError(fmt.Sprintf("unknown tool: %q", p.Name)))
	}
	text, err := t.handler(ctx, p.Arguments)
	if err != nil {
		s.log.Warn("tool failed", "tool", p.Name, "err", err)
		return s.ok(req.ID, toolError(err.Error()))
	}
	return s.ok(req.ID, toolResult(capText(text), false))
}

// maxToolTextBytes bounds every tool's returned text so a large query (many
// entries via list_entries, or a get_entry whose captured body/headers are
// big) can't blow past the calling agent's context window.
const maxToolTextBytes = 100_000

// capText truncates s to at most maxToolTextBytes (at a valid UTF-8
// boundary), appending an explicit notice — never a silent, misleadingly
// "complete-looking" cut — that also says how to get a smaller result.
func capText(s string) string {
	if len(s) <= maxToolTextBytes {
		return s
	}
	cut := s[:maxToolTextBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return fmt.Sprintf("%s\n\n... [truncated: %d of %d bytes shown — narrow with a more specific filter, "+
		"a smaller limit, or page with before_seq]", cut, len(cut), len(s))
}

func (s *Server) lookup(name string) *tool {
	for _, t := range s.tools {
		if t.name == name {
			return t
		}
	}
	return nil
}

func (s *Server) toolDefs() []toolDef {
	defs := make([]toolDef, 0, len(s.tools))
	for _, t := range s.tools {
		defs = append(defs, toolDef{
			Name:        t.name,
			Description: t.description,
			InputSchema: t.inputSchema,
			Annotations: &toolAnnotations{ReadOnlyHint: !t.mutating},
		})
	}
	return defs
}

func (s *Server) ok(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *Server) fail(id json.RawMessage, code int, msg string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

// toolResult builds the MCP tools/call result payload.
func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}

func toolError(text string) map[string]any { return toolResult(text, true) }

// --- tool registry ---------------------------------------------------------

func (s *Server) registerTools() {
	noArgs := func() map[string]any {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	// filterDesc documents IFL once for every tool that accepts a filter.
	const filterDesc = "IFL filter, e.g. `response.status >= 500 and dst.namespace == \"shop\"` or " +
		"`elapsedMs > 500`. Fields include protocol, status, elapsedMs, http.method, response.status, " +
		"src.name, src.namespace, src.workload, dst.name, dst.namespace, dst.workload, request.path, " +
		"dns.rcode, redis.command, postgres.query. Operators: == != contains > < >= <=, combined with " +
		"and/or/not. Unknown field names are rejected — call list_filter_fields for the full catalog."
	// timeFormatDesc documents the accepted since/until formats once for every
	// tool with a time argument, diff_traffic's four included.
	const timeFormatDesc = "RFC3339 (\"2026-07-15T14:00:00Z\"), unix seconds, or a relative duration meaning that long ago (\"15m\", \"1h\")."
	timeProps := func() (map[string]any, map[string]any) {
		return map[string]any{
				"type":        "string",
				"description": "Only entries at/after this time: " + timeFormatDesc,
			}, map[string]any{
				"type":        "string",
				"description": "Only entries at/before this time (same formats as since).",
			}
	}
	sinceProp, untilProp := timeProps()
	// groupByDesc documents the group_by grouping key once for both
	// get_traffic_summary and diff_traffic.
	const groupByDesc = "Grouping key: \"workload\" (default; namespace/workload across both endpoints), " +
		"\"namespace\", or any IFL field (e.g. dst.name, protocol, node, http.method)."
	s.tools = []*tool{
		{
			name: "get_stats",
			description: "Get aggregate traffic statistics from the hub: total entries, entries/sec, worker count, " +
				"counts by protocol and by status, plus trailing 1m/5m windows (entries, errors, rate) for current-state questions.",
			inputSchema: noArgs(),
			handler:     s.handleGetStats,
		},
		{
			name: "list_entries",
			description: "List recent captured L7 entries (newest first) as compact records " +
				"(id, seq, protocol, time, src, dst, summary, response, status, latency). Narrow with an IFL filter " +
				"and/or a time range. When the result is a full page, it ends with a before_seq hint for the next " +
				"page — pass that back to keep paging older.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{"type": "string", "description": filterDesc},
					"limit": map[string]any{
						"type":        "number",
						"description": "Maximum entries to return (default 100, clamped to 1..1000).",
					},
					"since": sinceProp,
					"until": untilProp,
					"before_seq": map[string]any{
						"type": "number",
						"description": "Pagination cursor: only entries older than this seq value (from a " +
							"previous call's \"seq\" field or its next-page hint), for paging back past `limit`.",
					},
				},
			},
			handler: s.handleListEntries,
		},
		{
			name:        "get_entry",
			description: "Fetch the full JSON of a single captured entry by its ID (headers, bodies, timings, L4 metadata).",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "string", "description": "The entry ID."},
				},
				"required": []string{"id"},
			},
			handler: s.handleGetEntry,
		},
		{
			name: "get_traffic_summary",
			description: "Aggregate the buffered traffic per group: entry count, error/warning counts, protocols and " +
				"latency percentiles (p50/p95/max) per workload, namespace, or any filter field. The fastest way to answer " +
				"\"which service is failing/slow?\" — prefer this over pulling raw entries.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"group_by": map[string]any{"type": "string", "description": groupByDesc},
					"filter":   map[string]any{"type": "string", "description": filterDesc},
					"since":    sinceProp,
					"until":    untilProp,
					"limit": map[string]any{
						"type":        "number",
						"description": "Maximum groups to return (default 25, busiest first).",
					},
				},
			},
			handler: s.handleTrafficSummary,
		},
		{
			name: "diff_traffic",
			description: "Compare traffic between two time windows per group (e.g. a baseline \"before\" period vs " +
				"a \"during/after\" period around an incident): volume, error-rate and p95 latency deltas, sorted by " +
				"the strongest error-rate regression first. Flags groups that appeared (new traffic/errors, absent " +
				"from the baseline) or disappeared (traffic stopped, absent from the current window). Calls " +
				"get_traffic_summary's underlying endpoint twice and diffs the results — the fastest way to answer " +
				"\"what changed since the incident started?\" without doing that comparison yourself.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"baseline_since": map[string]any{"type": "string", "description": "Start of the baseline (\"before\") window: " + timeFormatDesc},
					"baseline_until": map[string]any{"type": "string", "description": "End of the baseline window (same formats as baseline_since)."},
					"current_since":  map[string]any{"type": "string", "description": "Start of the current (\"during\"/\"after\") window: " + timeFormatDesc},
					"current_until":  map[string]any{"type": "string", "description": "End of the current window (same formats as current_since)."},
					"group_by":       map[string]any{"type": "string", "description": groupByDesc},
					"filter":         map[string]any{"type": "string", "description": filterDesc},
					"limit": map[string]any{
						"type":        "number",
						"description": "Maximum groups to return (default 20, strongest regressions first).",
					},
				},
				"required": []string{"baseline_since", "baseline_until", "current_since", "current_until"},
			},
			handler: s.handleDiffTraffic,
		},
		{
			name: "find_error_clusters",
			description: "Group error/warning entries over a time window into clusters sharing a signature (protocol, " +
				"destination workload, status/status code, and a normalized response summary — numeric IDs collapsed so " +
				"\"user 42 not found\" and \"user 99 not found\" cluster together), sorted by cluster size descending. " +
				"Each cluster reports its count, first/last-seen time and a few example entry IDs (pass one to get_entry " +
				"for the full record). The direct answer to \"what families of errors are happening right now?\" and the " +
				"natural first step of a debug session — prefer this over listing error entries one by one.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{
						"type":        "string",
						"description": "Optional extra IFL filter narrowing which error/warning entries to cluster (e.g. `dst.namespace == \"shop\"`). " + filterDesc,
					},
					"since": sinceProp,
					"until": untilProp,
					"limit": map[string]any{
						"type":        "number",
						"description": "Maximum clusters to return (default 20, biggest first).",
					},
				},
			},
			handler: s.handleFindErrorClusters,
		},
		{
			name: "get_timeline",
			description: "Bucket matching traffic into a fixed-step time series (entries, errors, warnings per bucket, " +
				"zero-filled) — use it to spot when a problem started or whether it is ongoing.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"filter": map[string]any{"type": "string", "description": filterDesc},
					"bucket_seconds": map[string]any{
						"type":        "number",
						"description": "Bucket width in seconds (default 60, clamped to 1..3600).",
					},
					"since": sinceProp,
					"until": untilProp,
				},
			},
			handler: s.handleTimeline,
		},
		{
			name: "get_workers",
			description: "List every capture worker the hub has seen: node, version, connected or not, last-seen time, " +
				"entries received, self-reported drop count and capture state. Check this first when traffic from a node " +
				"seems missing — it distinguishes \"nothing to capture\" from \"worker down or dropping\".",
			inputSchema: noArgs(),
			handler:     s.handleGetWorkers,
		},
		{
			name: "list_filter_fields",
			description: "List every IFL filter field with its type, operators and most-seen values — call this before " +
				"writing non-trivial filters.",
			inputSchema: noArgs(),
			handler:     s.handleListFilterFields,
		},
		{
			name:        "list_namespaces",
			description: "List the Kubernetes namespaces seen in buffered traffic, with entry/error counts and latency percentiles.",
			inputSchema: noArgs(),
			handler:     s.handleListNamespaces,
		},
		{
			name: "list_workloads",
			description: "List the workloads (namespace/name) seen in buffered traffic, with protocols, entry/error counts " +
				"and latency percentiles.",
			inputSchema: noArgs(),
			handler:     s.handleListWorkloads,
		},
	}
	if s.allowCapture {
		s.tools = append(s.tools, &tool{
			name:        "start_pcap",
			description: "Start a per-namespace PCAP capture. Placeholder: the capture backend is still in design and this tool does not yet capture anything.",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"namespace":       map[string]any{"type": "string", "description": "Namespace to capture (optional)."},
					"filter":          map[string]any{"type": "string", "description": "IFL filter to scope the capture (optional)."},
					"durationSeconds": map[string]any{"type": "number", "description": "Capture duration in seconds (optional)."},
				},
			},
			handler:  s.handleStartPcap,
			mutating: true,
		})
	}
}

// --- tool handlers ---------------------------------------------------------

func (s *Server) handleGetStats(ctx context.Context, _ map[string]any) (string, error) {
	var st api.Stats
	if err := s.getJSON(ctx, "/api/stats", &st); err != nil {
		return "", err
	}
	return marshalPretty(st, "stats")
}

func (s *Server) handleListEntries(ctx context.Context, args map[string]any) (string, error) {
	limit := argInt(args, "limit", 100)
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	q := url.Values{}
	if f := argString(args, "filter"); f != "" {
		q.Set("filter", f)
	}
	setTimeArgs(q, args)
	q.Set("limit", strconv.Itoa(limit))
	if bs := argInt(args, "before_seq", 0); bs > 0 {
		q.Set("before_seq", strconv.Itoa(bs))
	}

	var entries []api.Entry
	if err := s.getJSON(ctx, "/api/entries?"+q.Encode(), &entries); err != nil {
		return "", err
	}
	compact := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		rec := map[string]any{
			"id":         e.ID,
			"seq":        e.Seq,
			"protocol":   e.Protocol,
			"time":       e.Timestamp,
			"src":        endpointLabel(e.Source),
			"dst":        endpointLabel(e.Destination),
			"summary":    e.Request.Summary,
			"status":     e.Status,
			"statusCode": e.StatusCode,
			"elapsedMs":  e.ElapsedMs,
		}
		// The response summary carries the outcome text (error line, rcode,
		// redis reply type, ...) — key context an agent would otherwise
		// re-fetch entry by entry.
		if e.Response.Summary != "" {
			rec["response"] = e.Response.Summary
		}
		compact = append(compact, rec)
	}
	b, err := json.MarshalIndent(compact, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling entries: %w", err)
	}
	out := fmt.Sprintf("%d entries\n%s", len(compact), b)
	// A full page (len == limit) may not be the whole story — the buffer
	// could hold plenty more older entries. Hint the cursor explicitly
	// rather than letting the agent assume this is everything.
	if len(entries) == limit {
		out += fmt.Sprintf("\n\nnext page: call again with before_seq=%d to see older entries", entries[len(entries)-1].Seq)
	}
	return out, nil
}

// setTimeArgs copies the optional since/until tool args onto a hub query.
func setTimeArgs(q url.Values, args map[string]any) {
	if v := argString(args, "since"); v != "" {
		q.Set("since", v)
	}
	if v := argString(args, "until"); v != "" {
		q.Set("until", v)
	}
}

func (s *Server) handleGetEntry(ctx context.Context, args map[string]any) (string, error) {
	id := argString(args, "id")
	if id == "" {
		return "", fmt.Errorf("missing required argument: id")
	}
	body, status, err := s.get(ctx, "/api/entry/"+url.PathEscape(id))
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", fmt.Errorf("entry not found: %s", id)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("hub returned %d: %s", status, strings.TrimSpace(string(body)))
	}
	var e api.Entry
	if err := json.Unmarshal(body, &e); err != nil {
		return "", fmt.Errorf("decoding entry: %w", err)
	}
	return marshalPretty(e, "entry")
}

// handleTrafficSummary proxies /api/summary: per-group counts, error totals
// and latency percentiles, computed hub-side over the whole buffer.
func (s *Server) handleTrafficSummary(ctx context.Context, args map[string]any) (string, error) {
	q := url.Values{}
	if g := argString(args, "group_by"); g != "" {
		q.Set("groupBy", g)
	}
	if f := argString(args, "filter"); f != "" {
		q.Set("filter", f)
	}
	setTimeArgs(q, args)
	limit := argInt(args, "limit", 25)
	q.Set("limit", strconv.Itoa(limit))
	return s.getPretty(ctx, "/api/summary?"+q.Encode(), "summary")
}

// handleTimeline proxies /api/timeline: zero-filled per-bucket entry/error
// counts across the requested window (default: last 15 minutes).
func (s *Server) handleTimeline(ctx context.Context, args map[string]any) (string, error) {
	q := url.Values{}
	if f := argString(args, "filter"); f != "" {
		q.Set("filter", f)
	}
	setTimeArgs(q, args)
	if b := argInt(args, "bucket_seconds", 0); b > 0 {
		q.Set("bucket", strconv.Itoa(b))
	}
	return s.getPretty(ctx, "/api/timeline?"+q.Encode(), "timeline")
}

// handleGetWorkers proxies /api/workers: the per-node capture health registry.
func (s *Server) handleGetWorkers(ctx context.Context, _ map[string]any) (string, error) {
	return s.getPretty(ctx, "/api/workers", "workers")
}

// handleListFilterFields renders the hub's field catalog, truncating each
// field's observed values so the result stays compact.
func (s *Server) handleListFilterFields(ctx context.Context, _ map[string]any) (string, error) {
	var catalog struct {
		Fields []struct {
			Name      string   `json:"name"`
			Type      string   `json:"type"`
			Operators []string `json:"operators"`
			Values    []struct {
				Value string `json:"value"`
				Count int64  `json:"count"`
			} `json:"values"`
		} `json:"fields"`
	}
	if err := s.getJSON(ctx, "/api/fields", &catalog); err != nil {
		return "", err
	}
	const maxValues = 8
	type field struct {
		Name      string   `json:"name"`
		Type      string   `json:"type"`
		Operators []string `json:"operators"`
		Values    []string `json:"values,omitempty"`
	}
	out := make([]field, 0, len(catalog.Fields))
	for _, f := range catalog.Fields {
		vals := make([]string, 0, min(len(f.Values), maxValues))
		for _, v := range f.Values[:min(len(f.Values), maxValues)] {
			vals = append(vals, v.Value)
		}
		out = append(out, field{Name: f.Name, Type: f.Type, Operators: f.Operators, Values: vals})
	}
	return marshalPretty(out, "fields")
}

func (s *Server) handleListNamespaces(ctx context.Context, _ map[string]any) (string, error) {
	return s.getPretty(ctx, "/api/summary?groupBy=namespace&limit=200", "namespaces")
}

func (s *Server) handleListWorkloads(ctx context.Context, _ map[string]any) (string, error) {
	return s.getPretty(ctx, "/api/summary?groupBy=workload&limit=200", "workloads")
}

func (s *Server) handleStartPcap(_ context.Context, _ map[string]any) (string, error) {
	return "PCAP capture is not yet available — the capture backend is still in design. " +
		"This tool is a wired placeholder; it will trigger real per-namespace PCAP captures once the backend lands.", nil
}

// --- hub HTTP helpers ------------------------------------------------------

// get performs a GET against the hub and returns the raw body and status code.
// Connection failures are wrapped with a hint about the hub being reachable.
func (s *Server) get(ctx context.Context, path string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.hubURL+path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("building request: %w", err)
	}
	if s.hubToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.hubToken)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot reach hub at %s: %w — is it running / port-forwarded?", s.hubURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading hub response: %w", err)
	}
	return body, resp.StatusCode, nil
}

// getJSON GETs path and decodes a 200 body into out.
func (s *Server) getJSON(ctx context.Context, path string, out any) error {
	body, status, err := s.get(ctx, path)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("hub returned %d: %s", status, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding hub response: %w", err)
	}
	return nil
}

// getPretty GETs path and re-renders the JSON body indented, for tools that
// pass a hub response through unchanged.
func (s *Server) getPretty(ctx context.Context, path, what string) (string, error) {
	var v any
	if err := s.getJSON(ctx, path, &v); err != nil {
		return "", err
	}
	return marshalPretty(v, what)
}

// --- small helpers ---------------------------------------------------------

func marshalPretty(v any, what string) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling %s: %w", what, err)
	}
	return string(b), nil
}

// endpointLabel renders an endpoint as "namespace/workload:port", preferring
// the stable workload name, then the pod/service name, then the raw IP.
func endpointLabel(ep api.Endpoint) string {
	name := ep.Workload
	if name == "" {
		name = ep.Name
	}
	if name == "" {
		name = ep.IP
	}
	if ep.Namespace != "" {
		name = ep.Namespace + "/" + name
	}
	if ep.Port > 0 {
		name += ":" + strconv.Itoa(ep.Port)
	}
	return name
}

func argString(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func argInt(args map[string]any, key string, def int) int {
	switch n := args[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return def
}
