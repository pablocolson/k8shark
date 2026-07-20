package mcp

// TST-6: the MCP server hand-rolls JSON-RPC-over-stdio framing (handleLine,
// dispatch, callTool) and argument coercion (argString/argInt) with no SDK, so
// a regression here surfaces to an agent only as an opaque error. These tests
// pin the protocol behavior: method routing, the tool-error taxonomy, hub
// token propagation, argument coercion, and the stdout-is-JSON-RPC-only
// invariant that CLAUDE.md requires (logs must go to stderr).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pablocolson/k8shark/pkg/api"
)

// fakeHub returns an httptest server running handler, plus an mcp Server wired
// to it with the given token.
func fakeHub(handler http.HandlerFunc, token string) (*Server, *httptest.Server) {
	ts := httptest.NewServer(handler)
	s := New(ts.URL, token, false, discardLogger())
	return s, ts
}

// callToolResult runs a tools/call for name/args and returns the MCP result map
// and whether it was flagged isError.
func callToolResult(t *testing.T, s *Server, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"name": name, "arguments": args})
	resp := s.dispatch(context.Background(), rpcRequest{
		Method: "tools/call", ID: json.RawMessage("1"), Params: params,
	})
	if resp.Error != nil {
		t.Fatalf("tools/call returned a protocol error %+v; tool failures should be results with isError", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", resp.Result)
	}
	isErr, _ := m["isError"].(bool)
	return m, isErr
}

// resultText extracts the concatenated text content of a tools/call result.
func resultText(t *testing.T, m map[string]any) string {
	t.Helper()
	content, ok := m["content"].([]map[string]any)
	if !ok {
		t.Fatalf("result content type = %T, want []map[string]any", m["content"])
	}
	var b strings.Builder
	for _, c := range content {
		if txt, ok := c["text"].(string); ok {
			b.WriteString(txt)
		}
	}
	return b.String()
}

// TestDispatchMethods covers the four routed methods plus the unknown-method
// error.
func TestDispatchMethods(t *testing.T) {
	s := New("http://hub.invalid", "", false, discardLogger())
	ctx := context.Background()

	if resp := s.dispatch(ctx, rpcRequest{Method: "ping", ID: json.RawMessage("1")}); resp.Error != nil {
		t.Errorf("ping error = %+v, want nil", resp.Error)
	}

	resp := s.dispatch(ctx, rpcRequest{Method: "tools/list", ID: json.RawMessage("2")})
	if resp.Error != nil {
		t.Fatalf("tools/list error = %+v", resp.Error)
	}
	result := resp.Result.(map[string]any)
	tools, ok := result["tools"].([]toolDef)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools/list returned %T with %d tools, want a non-empty []toolDef", result["tools"], len(tools))
	}

	resp = s.dispatch(ctx, rpcRequest{Method: "no/such/method", ID: json.RawMessage("3")})
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("unknown method error = %+v, want code -32601", resp.Error)
	}
}

// TestCallToolUnknownTool: an unknown tool name is a result with isError:true,
// not a protocol-level error (so the agent gets an actionable message).
func TestCallToolUnknownTool(t *testing.T) {
	s := New("http://hub.invalid", "", false, discardLogger())
	m, isErr := callToolResult(t, s, "no_such_tool", nil)
	if !isErr {
		t.Fatal("unknown tool should return isError:true")
	}
	if txt := resultText(t, m); !strings.Contains(txt, "unknown tool") {
		t.Errorf("unknown-tool text = %q, want it to mention 'unknown tool'", txt)
	}
}

// TestCallToolInvalidParams: malformed tools/call params yield an isError
// result rather than a panic or a silent success.
func TestCallToolInvalidParams(t *testing.T) {
	s := New("http://hub.invalid", "", false, discardLogger())
	// Params is a bare number, not the expected {name, arguments} object.
	resp := s.callTool(context.Background(), rpcRequest{
		Method: "tools/call", ID: json.RawMessage("1"), Params: json.RawMessage("123"),
	})
	m := resp.Result.(map[string]any)
	if isErr, _ := m["isError"].(bool); !isErr {
		t.Fatal("invalid params should return isError:true")
	}
	if txt := resultText(t, m); !strings.Contains(txt, "invalid params") {
		t.Errorf("invalid-params text = %q, want it to mention 'invalid params'", txt)
	}
}

// TestCallToolMissingRequiredArg: get_entry without an id is reported as a tool
// error carrying the argument name.
func TestCallToolMissingRequiredArg(t *testing.T) {
	s := New("http://hub.invalid", "", false, discardLogger())
	m, isErr := callToolResult(t, s, "get_entry", map[string]any{})
	if !isErr {
		t.Fatal("get_entry without id should return isError:true")
	}
	if txt := resultText(t, m); !strings.Contains(txt, "id") {
		t.Errorf("missing-arg text = %q, want it to name the 'id' argument", txt)
	}
}

// TestCallToolHubUnreachable: when the hub can't be reached, the tool fails
// with a hint about the hub, not a cryptic dial error.
func TestCallToolHubUnreachable(t *testing.T) {
	// Port 1 is reliably unbindable/unconnectable for a test client.
	s := New("http://127.0.0.1:1", "", false, discardLogger())
	s.http.Timeout = 2 * time.Second
	m, isErr := callToolResult(t, s, "get_stats", nil)
	if !isErr {
		t.Fatal("get_stats against a dead hub should return isError:true")
	}
	if txt := resultText(t, m); !strings.Contains(txt, "cannot reach hub") {
		t.Errorf("unreachable text = %q, want it to mention 'cannot reach hub'", txt)
	}
}

// TestCallToolHubError: a non-200 hub response surfaces the status in the tool
// error.
func TestCallToolHubError(t *testing.T) {
	s, ts := fakeHub(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}, "")
	defer ts.Close()
	m, isErr := callToolResult(t, s, "get_stats", nil)
	if !isErr {
		t.Fatal("a 401 from the hub should return isError:true")
	}
	if txt := resultText(t, m); !strings.Contains(txt, "401") {
		t.Errorf("hub-error text = %q, want it to mention the 401 status", txt)
	}
}

// TestCallToolHappyPath: a successful get_stats renders the hub JSON.
func TestCallToolHappyPath(t *testing.T) {
	s, ts := fakeHub(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(api.Stats{TotalEntries: 42, EntriesPerSec: 3.5})
	}, "")
	defer ts.Close()
	m, isErr := callToolResult(t, s, "get_stats", nil)
	if isErr {
		t.Fatalf("get_stats happy path returned an error: %s", resultText(t, m))
	}
	if txt := resultText(t, m); !strings.Contains(txt, "42") {
		t.Errorf("get_stats text = %q, want it to include totalEntries 42", txt)
	}
}

// TestHubTokenPropagated: a configured hub token is sent as a Bearer header on
// every hub request.
func TestHubTokenPropagated(t *testing.T) {
	var gotAuth string
	s, ts := fakeHub(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(api.Stats{})
	}, "sekret")
	defer ts.Close()
	if _, isErr := callToolResult(t, s, "get_stats", nil); isErr {
		t.Fatal("get_stats unexpectedly failed")
	}
	if gotAuth != "Bearer sekret" {
		t.Errorf("hub saw Authorization = %q, want %q", gotAuth, "Bearer sekret")
	}
}

// TestArgIntCoercion: numeric args arrive from JSON as float64, but an agent
// (or a lax client) may send an int, a json.Number, or a string; argInt must
// coerce the numeric forms and fall back to the default otherwise.
func TestArgIntCoercion(t *testing.T) {
	cases := []struct {
		name string
		val  any
		want int
	}{
		{"float64 (the JSON default)", float64(7), 7},
		{"int", 9, 9},
		{"json.Number", json.Number("5"), 5},
		{"string is not coerced", "12", 100},
		{"missing key", nil, 100},
	}
	for _, c := range cases {
		args := map[string]any{}
		if c.val != nil {
			args["limit"] = c.val
		}
		if got := argInt(args, "limit", 100); got != c.want {
			t.Errorf("%s: argInt = %d, want %d", c.name, got, c.want)
		}
	}
	if got := argString(map[string]any{"filter": "x"}, "filter"); got != "x" {
		t.Errorf("argString = %q, want %q", got, "x")
	}
	if got := argString(map[string]any{"filter": 5}, "filter"); got != "" {
		t.Errorf("argString of a non-string = %q, want empty", got)
	}
}

// TestHandleLineOutputDiscipline feeds a mixed sequence of lines through
// handleLine and asserts every byte written is a valid JSON-RPC response
// (stdout is the protocol channel — nothing else may appear), that requests
// each get exactly one response, that a notification and a blank line produce
// none, and that a malformed line gets the spec-mandated parse error (-32700,
// id null; -32600 when the JSON is valid but not a request object) instead of
// leaving the client hanging (MCP-6).
func TestHandleLineOutputDiscipline(t *testing.T) {
	s := New("http://hub.invalid", "", false, discardLogger())
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	write := func(resp rpcResponse) {
		if err := enc.Encode(resp); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()

	lines := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`, // request -> response
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,       // request -> response
		`{"jsonrpc":"2.0","method":"notifications/x"}`,   // notification (no id) -> nothing
		`{not valid json`, // malformed -> -32700, id null
		`[1,2,3]`,         // valid JSON, not a request object -> -32600, id null
		`   `,             // blank -> nothing
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, // request -> response
	}
	for _, l := range lines {
		s.handleLine(ctx, l, write)
	}

	dec := json.NewDecoder(&out)
	var responses int
	seenIDs := map[float64]bool{}
	var errCodes []float64
	for dec.More() {
		var resp struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *float64        `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   *struct {
				Code float64 `json:"code"`
			} `json:"error"`
		}
		if err := dec.Decode(&resp); err != nil {
			t.Fatalf("stdout contained a non-JSON-RPC line: %v", err)
		}
		responses++
		if resp.JSONRPC != "2.0" {
			t.Errorf("response jsonrpc = %q, want 2.0", resp.JSONRPC)
		}
		if resp.ID == nil {
			// Only the parse/invalid-request errors may carry a null id.
			if resp.Error == nil {
				t.Errorf("null-id response without an error object: %+v", resp)
			} else {
				errCodes = append(errCodes, resp.Error.Code)
			}
			continue
		}
		if resp.Result == nil && resp.Error == nil {
			t.Errorf("response for id %v has neither result nor error", *resp.ID)
		}
		seenIDs[*resp.ID] = true
	}
	if responses != 5 {
		t.Fatalf("wrote %d responses, want 5 (one per request + parse error + invalid request)", responses)
	}
	for _, id := range []float64{1, 2, 3} {
		if !seenIDs[id] {
			t.Errorf("no response for request id %v", id)
		}
	}
	if len(errCodes) != 2 || errCodes[0] != -32700 || errCodes[1] != -32600 {
		t.Errorf("null-id error codes = %v, want [-32700 -32600]", errCodes)
	}
}

// TestServeConcurrentCalls locks the MCP-6 concurrency fix: a slow tools/call
// (hub taking hundreds of ms) must not block a ping sent right after it — the
// ping's response must reach the wire first. Also exercises serve() end to end
// over in-memory streams: clean nil return on EOF, all responses written.
func TestServeConcurrentCalls(t *testing.T) {
	s, ts := fakeHub(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(api.Stats{})
	}, "")
	defer ts.Close()

	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_stats"}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n")
	var out bytes.Buffer
	if err := s.serve(context.Background(), in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}

	dec := json.NewDecoder(&out)
	var ids []float64
	for dec.More() {
		var resp struct {
			ID *float64 `json:"id"`
		}
		if err := dec.Decode(&resp); err != nil {
			t.Fatalf("bad response line: %v", err)
		}
		if resp.ID == nil {
			t.Fatal("response without id")
		}
		ids = append(ids, *resp.ID)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d responses, want 2", len(ids))
	}
	if ids[0] != 2 {
		t.Fatalf("first response id = %v, want 2 (ping must not wait behind the slow tools/call)", ids[0])
	}
}
