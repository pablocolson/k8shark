# Contributing to k8shark

Thanks for your interest. k8shark is a single Go binary (worker / hub / MCP /
CLI) plus a Vite + React front-end, deployed by an embedded Helm chart.
`CLAUDE.md` is the authoritative guide to the architecture and conventions —
read it before extending a dissector, the filter language, or the wire
contract.

## Development setup

```bash
make dev        # build the UI + run the hub (serving it) + a demo worker
                # -> http://localhost:8898, no cluster needed
```

- `make build` — native CLI binary (`bin/k8shark`)
- `make test` — Go unit tests (with the macOS `-linkmode=external` workaround)
- `make test-ui` — front-end vitest suite
- `make helm-lint` — lint the chart

## Before opening a PR

Run the same checks CI runs:

```bash
gofmt -l .              # must print nothing
go vet ./...
go test -race ./...     # on macOS: go test -race -ldflags="-linkmode=external" ./...
make helm-lint
cd ui && npm ci && npm test && npm run build
```

## Conventions

- **Match the surrounding style.** Keep comments at the density of the file
  you're editing; use `log/slog` for logging (the `mcp` command must log only
  to stderr — stdout is its JSON-RPC channel).
- **`pkg/api` changes are additive**, `json:",omitempty"`, and nil-safe: an
  old entry must still deserialize.
- **Filter fields stay in sync.** Adding an IFL field means updating
  `fieldGetter` (`internal/hub/filter.go`) *and* the catalog
  (`internal/hub/facets.go`) — `TestFieldCatalogMatchesGetter` enforces it.
- **Adding a protocol dissector:** see the "Protocols & dispatch" section of
  `CLAUDE.md`. Add real-bytes unit tests in `internal/worker/dissect_test.go`.
- **Live capture is Linux-only + cgo.** Build the worker image via
  `build/k8shark.Dockerfile`; on macOS the worker only produces demo traffic
  (with `--demo`). See the gotchas section of `CLAUDE.md`.
- **Commits/PRs** branch off `main`. Keep the subject imperative and under
  ~50 chars; explain the *why* in the body when it isn't obvious.

## Reporting bugs / requesting features

Use the issue templates (bug report / feature request). For a bug, include
`k8shark version`, your Kubernetes distro/CNI, and whether eBPF TLS capture is
enabled.
