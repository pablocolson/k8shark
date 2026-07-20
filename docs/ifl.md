# IFL — the k8shark Filter Language

IFL is the small query language k8shark evaluates **hub-side** to filter the
live feed and the REST history. It powers the FilterBar autocomplete, the
`?filter=` REST/WebSocket parameter, and the MCP tools' `filter` argument.

This page is the field reference. The live catalog is served at
`GET /api/fields` (that's what the FilterBar autocompletes from); this doc
mirrors it. If you add a field, keep `internal/hub/filter.go` (`fieldGetter`)
and `internal/hub/facets.go` (the catalog) in sync — `TestFieldCatalogMatchesGetter`
enforces it — then regenerate this list.

## Grammar

```
expr    := or
or      := and ( "or" and )*
and     := not ( "and" not )*
not     := "not" not | primary
primary := "(" expr ")" | comparison | bareToken
comparison := field operator value
```

- **Booleans:** `and`, `or`, `not`, with parentheses. `and` binds tighter than `or`.
- **Bare token:** a value with no `field operator` around it is a full-text
  substring match across the entry (e.g. `checkout`, or `"GET /api"`).
- **Quoting:** string values may be quoted (`"kube-system"`) or bare when
  unambiguous (`dns`, `500`). Quote anything with spaces or special chars.
- **Unknown field names are a compile error**, never a silent match-nothing —
  so a typo fails loudly instead of hiding all traffic.
- The parser is depth- and length-bounded against pathological input.

## Operators

| Operator | Meaning | Applies to |
|----------|---------|-----------|
| `==` `!=` | equal / not equal | all fields |
| `>` `<` `>=` `<=` | numeric compare | number fields |
| `contains` | substring | string / freetext fields |
| `startswith` | prefix | string / freetext fields |
| `matches` | RE2 regex (pattern ≤ 256 bytes, no catastrophic backtracking) | string / freetext fields |
| `in ("a", "b", …)` | membership (list ≤ 64 values) | all fields |

Which operators a given field accepts depends on its **type**:

- **enum** — `== != in`
- **number** — `== != > < >= <= in`
- **string** — `== != contains matches startswith in`
- **freetext** — `== != contains matches startswith`

## Fields

### Core

| Field | Type |
|-------|------|
| `protocol` | enum (`http` `dns` `redis` `valkey` `postgres` `amqp` `ws` `tcp` `udp` `icmp`) |
| `status` | enum (`success` `warning` `error`) |
| `node` | string |
| `elapsedMs` | number |
| `namespace` | string (either endpoint's namespace) |
| `bytes` / `packets` | number |
| `flags` | string |
| `summary` | freetext |

### Endpoints

| Field | Type |
|-------|------|
| `src.name` `dst.name` | string (k8s pod/service name) |
| `src.namespace` `dst.namespace` | string |
| `src.workload` `dst.workload` | string (owning Deployment/StatefulSet/…) |
| `src.ip` `dst.ip` | string |
| `src.port` `dst.port` | number |

### HTTP

| Field | Type |
|-------|------|
| `http.method` | enum |
| `http.version` | enum |
| `response.status` | number |
| `request.host` | string |
| `request.path` | freetext |
| `request.body` `response.body` | freetext |
| `request.size` `response.size` | number |
| `response.contenttype` | string |
| `http.ttfbms` | number |
| `request.header.<name>` `response.header.<name>` | freetext (any captured header, resolved by prefix) |

### DNS

| Field | Type |
|-------|------|
| `dns.question` `dns.answer` | freetext |
| `dns.rcode` | enum |
| `dns.type` | enum |
| `dns.authoritative` `dns.recursionavailable` | enum |

### Redis / Valkey

| Field | Type |
|-------|------|
| `redis.command` | enum |
| `redis.reply` | freetext |
| `redis.db` | number |
| `redis.pipelinedepth` | number |

### Postgres

| Field | Type |
|-------|------|
| `postgres.query` | freetext |
| `postgres.error` | string |
| `postgres.statement` | string |
| `postgres.portal` | string |
| `postgres.txstatus` | enum |
| `postgres.rowcount` | number |

### AMQP

| Field | Type |
|-------|------|
| `amqp.class` `amqp.method` | enum |
| `amqp.exchange` `amqp.routingkey` `amqp.queue` | string |
| `amqp.deliverytag` | number |
| `amqp.correlationid` | freetext |
| `amqp.replyto` | string |

### WebSocket

| Field | Type |
|-------|------|
| `ws.opcode` | enum (`text` `binary` `close` `ping` `pong` `continuation`) |

### L4 (connection metadata)

| Field | Type |
|-------|------|
| `l4.ttl` `l4.window` `l4.mss` `l4.retransmits` | number |
| `l4.rttms` `l4.durationms` | number |
| `l4.clientbytes` `l4.serverbytes` `l4.clientpackets` `l4.serverpackets` | number |
| `l4.seqstart` `l4.ackstart` | number |
| `l4.srcmac` `l4.dstmac` | string |
| `l4.ipversion` | enum |
| `l4.ipflags` `l4.clienttcpflags` `l4.servertcpflags` | string |

### TLS

| Field | Type |
|-------|------|
| `tls.sni` | string |

## Examples

```
http.method == "POST" and response.status >= 500
protocol == "dns" or dst.namespace == "kube-system"
dst.namespace in ("prod", "staging") and elapsedMs > 500
request.path matches "^/api/v[0-9]+/" and not (src.name contains "canary")
redis.command == "SET" and redis.db == 3
postgres.query contains "UPDATE" and postgres.rowcount > 100
amqp.replyto startswith "amq." and amqp.correlationid contains "req-"
ws.opcode in ("text", "binary")
request.header.user-agent contains "curl"
"checkout"          # bare token = full-text match across the entry
```
