# CLAUDE.md

Guidance for Claude Code (and any AI agent) working in this repository.

## What this is

**k8shark** — a from-scratch, Kubeshark-style network-observability tool for Kubernetes. A single Go binary
captures L7 traffic across a cluster (userspace **AF_PACKET**, plus optional **eBPF uprobes** for TLS-encrypted
traffic), reconstructs it into request/response **entries**, and streams them to a real-time React dashboard.
Module: `github.com/pablocolson/k8shark`.

> This is an **original MVP**, not a fork of Kubeshark. It reproduces Kubeshark's shape and workflow (worker
> DaemonSet → hub → live filtered dashboard) but is written from scratch — see the "Scope vs. Kubeshark"
> section in `README.md`. Do not copy code from the Kubeshark project; only files under this repo are
> authoritative for how this codebase works.

## Architecture

One binary, role chosen by sub-command (same image ships everywhere):

```
node ─▶ worker (DaemonSet) ──entries (WS)──▶ hub (Deployment) ──entries+stats (WS)──▶ front (React/nginx)
        AF_PACKET + eBPF TLS capture          in-mem ring buffer                       live table, detail,
        TCP reassembly                        REST + WebSocket                         service map, IFL bar
        L7 dissect → api.Entry                server-side IFL filter + k8s enrichment
```

- **worker** (`k8shark worker`): gopacket AF_PACKET capture → `tcpassembly` TCP reassembly → L7 dissectors →
  paired `api.Entry` → hub over WebSocket. Optional `--enable-tls` attaches eBPF uprobes to OpenSSL/boringssl
  (`internal/worker/ebpf/`) to also capture decrypted TLS traffic, fed through the same dissectors. Falls back
  to a synthetic **demo** feed only when `--demo` is passed explicitly (never silently, even if live capture
  fails — a broken capture should be loud, not disguised as traffic).
- **hub** (`k8shark hub`): bounded in-memory ring buffer, REST API, WebSocket fan-out to front clients,
  server-side IFL filtering, `/metrics` (Prometheus text), and best-effort k8s IP→pod/service name enrichment
  (`internal/hub/k8s.go`, no-op outside a cluster).
- **front** (`ui/`): Vite + React + TS, served by nginx in-cluster (nginx proxies `/api` and `/ws` to the hub).
- **CLI**: `tap` (helm-install + port-forward + open browser), `clean`, `proxy`, `console`, `hub`, `worker`,
  `mcp`, `version`.
- **mcp** (`k8shark mcp`): hand-rolled JSON-RPC-over-stdio MCP server (stdlib only, no SDK) that exposes the
  hub's data to AI agents (`get_stats`, `list_entries`, `get_entry`, `list_namespaces`, `list_workloads`,
  and a gated `start_pcap` placeholder).

## Repo layout

| Path | What |
|---|---|
| `cmd/k8shark/main.go` | entrypoint → `internal/cli` |
| `internal/cli/*.go` | cobra command tree (one file per command) |
| `internal/hub/{server,store,filter,k8s}.go` | hub server, ring buffer, IFL filter language, k8s enrichment |
| `internal/worker/worker.go` | capture loop + `route()` (packet → assembler / DNS / L4) |
| `internal/worker/pipeline.go` | protocol-agnostic req/resp pairing (`enqueueRequest`/`completeResponse`, `connKey`) |
| `internal/worker/dissect_*.go` | per-protocol dissectors (redis, postgres, amqp, l4) + `dissect_test.go` |
| `internal/worker/capture/` | AF_PACKET source (`afpacket_linux.go`, cgo/linux) + non-linux stub |
| `internal/worker/ebpf/` | eBPF TLS uprobe capture (CO-RE, Linux-only) — `--enable-tls` |
| `internal/worker/demo.go` | synthetic traffic generator (`--demo` only) |
| `internal/mcp/server.go` | MCP JSON-RPC server |
| `pkg/api/types.go` | **wire contract** shared by worker/hub/front (`Entry`, `Payload`, `Endpoint`, `Stats`, `Envelope`) |
| `ui/src/` | React front (`App.tsx`, `useHub.ts`, `types.ts`, `components/*.tsx`, `styles.css`) |
| `helm/k8shark/` | Helm chart (embedded into the binary via `helm/embed.go` for `k8shark tap`) |
| `deploy/` | standalone plain-manifest deploy example (no Helm) |
| `build/*.Dockerfile`, `Makefile` | container images + build/dev targets |

## Build, test, run

Use the Makefile. Key targets: `build`, `ui`, `dev`, `test`, `helm-lint`, `docker-build`, `docker-push`, `clean`.

```bash
make dev        # build UI + run hub (serving the UI) + a demo worker → http://localhost:8898  (no cluster needed)
make test       # go unit tests
make build      # native binary → bin/k8shark
make ui         # React build → ui/dist
```

- **`ui/dist` is served by the hub** via `k8shark hub --serve-ui ui/dist` (that's what `make dev` does), so you
  can exercise the whole app locally without Kubernetes.
- Verify runtime changes by driving the real flow (worker → hub → front / MCP) and observing — not only tests.

## ⚠️ Gotchas (read before building/running)

- **macOS:** the Go internal linker can omit `LC_UUID` on some toolchain/OS combinations, causing binaries to
  crash with `missing LC_UUID load command`. The Makefile already handles it: `build` uses
  `-ldflags="-linkmode=external"` **and** ad-hoc `codesign`. For tests, run
  `go test -ldflags="-linkmode=external" ./...` (the `test` target does this). None of this affects
  Linux/container builds.
- **Toolchain:** builds pin `GOTOOLCHAIN=local`. Don't bump the `go`/`toolchain` directive above what's
  installed unless you intend to pull a newer toolchain.
- **Live capture is Linux-only + cgo:** `gopacket/afpacket` uses cgo + kernel headers, so it **only compiles on
  Linux** (build the worker image via `docker build -f build/k8shark.Dockerfile`, not cross-compile from
  macOS). On macOS the worker auto-falls back to demo mode **only if `--demo` was passed**; otherwise it logs
  an error and stays idle. `go build ./...` on macOS excludes the afpacket file.
- **eBPF TLS capture is Linux-only, needs BTF + privileges:** `--enable-tls` requires `/sys/kernel/btf/vmlinux`
  and BPF/PERFMON/SYS_ADMIN/SYS_RESOURCE/SYS_PTRACE capabilities (see `helm/k8shark/values.yaml`
  `worker.tls.enabled`). It degrades gracefully — if it can't load/attach, the worker logs a warning and
  continues on AF_PACKET alone. Go's `crypto/tls` is not hooked yet (only OpenSSL/boringssl).

## Protocols & dispatch

Dissected today: **HTTP, DNS, Redis (RESP2/RESP3, incl. pub/sub), Postgres, AMQP 0-9-1, generic L4 (tcp/udp
flows + icmp)**. `route()` in `worker.go`: TCP → `tcpassembly` → `consumeStream()` dispatches by **server
port** (6379 Redis, 5432 Postgres, 5672 AMQP, else HTTP sniff); UDP → DNS handler or generic UDP flow; ICMP →
per-packet entry. Any TCP conn that produced an L7 entry is flagged (`markL7`) so it isn't also emitted as a
generic L4 flow. eBPF-decrypted TLS streams dispatch by content-sniff instead of port (see
`internal/worker/tls_pipeline.go`), since there's no port to key on once the traffic is already decrypted.

Adding a protocol: write `internal/worker/dissect_<proto>.go` with a `consume<Proto>` that calls
`enqueueRequest`/`completeResponse` (or emits standalone for async protocols); wire it in `consumeStream`; add
the `Protocol` const + any `Payload` fields in `pkg/api/types.go`; add filter fields in `internal/hub/filter.go`;
add a UI colour + `EntryDetail` case + demo traffic. Add real-bytes unit tests in `dissect_test.go`.

## IFL — the filter language

`internal/hub/filter.go` compiles an IFL expression to a predicate (recursive-descent parser, depth- and
length-bounded against pathological input). Fields resolved via `fieldGetter()` (e.g. `protocol`,
`http.method`, `response.status`, `src.name`, `dst.namespace`, `request.path`, `redis.command`,
`postgres.query`, `bytes`, `packets`, `flags`). Operators: `== != contains > < >= <=`; boolean `and`/`or`/`not`;
a bare token = full-text match. Filtering runs **hub-side** (REST `?filter=` and the front WebSocket, live via
a `MsgFilter` control frame). Keep `fieldGetter` and the front autocomplete (`internal/hub/facets.go`,
`/api/fields`) in sync when adding a field.

## Conventions

- Logging: `log/slog`. **The `mcp` command must log only to stderr** (stdout is the JSON-RPC channel).
- `pkg/api` changes are **additive**, `json:",omitempty"`, nil-safe — old entries must still deserialize.
- Match surrounding style; keep comments at the density of the file you're editing.
- Commit/push only when asked; branch off `main` for PRs.
