# Using the MCP server

`k8shark mcp` runs a [Model Context Protocol](https://modelcontextprotocol.io)
server over stdio that exposes the hub's captured traffic to AI agents as
read-only tools. An agent can ask "what's failing right now", "what changed
since the deploy", or "show me the slow Postgres queries" and get answers
computed hub-side instead of paging through raw entries.

## Prerequisite: a reachable hub

The MCP server is a thin relay — it needs the hub's REST API reachable from
the machine the agent runs on:

- **In-cluster install**: `k8shark tap` (deploys and port-forwards) or
  `k8shark proxy` (port-forwards an existing install). Both leave the hub on
  `http://localhost:8898`, the MCP default.
- **Local dev, no cluster**: `make dev` runs a hub plus a demo worker on the
  same address.

## Registering the server

### Claude Code

```bash
claude mcp add k8shark -- k8shark mcp --hub http://localhost:8898
```

### Claude Desktop, Cursor, and other `.mcp.json` clients

`k8shark mcp --print-config` prints a ready-to-paste block (it includes the
token when one is configured):

```bash
k8shark mcp --print-config
```

```json
{
  "mcpServers": {
    "k8shark": {
      "command": "k8shark",
      "args": ["mcp", "--hub", "http://localhost:8898"]
    }
  }
}
```

Paste it into the client's MCP config — `.mcp.json` at the project root for
Cursor/Claude Code, `claude_desktop_config.json` for Claude Desktop.

## Authentication

When the hub has an API token (`hub.apiToken` in the chart — `k8shark tap`
generates one by default), pass it with `--hub-token` or the
`K8SHARK_API_TOKEN` environment variable:

```json
{
  "mcpServers": {
    "k8shark": {
      "command": "k8shark",
      "args": ["mcp", "--hub", "http://localhost:8898"],
      "env": { "K8SHARK_API_TOKEN": "<token>" }
    }
  }
}
```

`k8shark tap` prints the generated token at install time; it also lives in the
`k8shark-api-token` Secret of the release namespace.

## Tools

| Tool | Answers |
|------|---------|
| `get_stats` | Current totals: entries/sec, per-protocol and per-status counts, workers. |
| `get_traffic_summary` | Aggregates per workload/namespace/field: volume, error rate, p50/p95/max latency. |
| `get_timeline` | Bucketed entries/errors/warnings over a time window. |
| `diff_traffic` | What changed between two time windows (volume, error-rate, p95 deltas per group). |
| `find_error_clusters` | Error/warning entries grouped by signature, with example entry IDs. |
| `list_entries` | Filtered entries (IFL filter, since/until, sort, `before_seq` pagination). |
| `get_entry` | One entry's full detail: headers, bodies, timings, L4 metadata. |
| `get_workers` | Per-node capture status: ring drops, TLS capture, paused state. |
| `list_filter_fields` | The IFL field catalog (call before writing non-trivial filters). |
| `list_namespaces`, `list_workloads` | Observed namespaces/workloads to scope filters. |
| `start_pcap` | Placeholder, only registered with `--allow-capture`. |

All tools except `start_pcap` are annotated `readOnlyHint: true`, so clients
that understand annotations can auto-approve them. Tool output is capped at
100 KB with an explicit truncation notice; `list_entries` pages with
`before_seq` when a page is full.

## Notes

- stdout carries the JSON-RPC protocol; all logs go to stderr. If you wrap the
  command, don't write to its stdout.
- The `initialize` result includes `instructions` steering agents toward the
  cheap aggregate tools first — no extra prompting needed.
