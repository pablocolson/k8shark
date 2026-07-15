import type { ReactNode } from "react";
import { useEffect, useMemo, useState } from "react";
import type { Entry, L4Info, Payload, PGColumn, RawView } from "../types";

type TabId = "overview" | "request" | "response" | "headers" | "body" | "raw" | "l4";

export function EntryDetail({ entry, onClose }: { entry: Entry; onClose: () => void }) {
  const tabs = useMemo(() => visibleTabs(entry), [entry]);
  const [tab, setTab] = useState<TabId>("overview");

  // Reset to Overview when switching entries so we never land on a now-empty tab.
  useEffect(() => setTab("overview"), [entry.id]);
  const active = tabs.includes(tab) ? tab : "overview";

  return (
    <aside className="detail">
      <div className="detail-head">
        <span className={`proto-badge big st-${entry.status}`}>{entry.protocol}</span>
        <span className="detail-title mono">{entry.request.summary}</span>
        <button className="icon-btn" onClick={onClose} title="close" aria-label="close">
          ✕
        </button>
      </div>

      <div className="detail-meta">
        <Meta k="status" v={String(entry.statusCode || entry.status || "—")} />
        <Meta k="latency" v={`${entry.elapsedMs} ms`} />
        <Meta k="node" v={entry.node} />
        <Meta k="time" v={new Date(entry.timestamp).toLocaleString([], { hour12: false })} />
      </div>

      <div className="detail-flow">
        <EndpointCard title="source" ep={entry.src} />
        <div className="arrow">→</div>
        <EndpointCard title="destination" ep={entry.dst} />
      </div>

      <div className="tabs">
        {tabs.map((t) => (
          <button
            key={t}
            className={`tab${t === active ? " active" : ""}`}
            onClick={() => setTab(t)}
          >
            {t}
          </button>
        ))}
      </div>

      <div className="tab-body">
        {active === "overview" && <OverviewTab entry={entry} />}
        {active === "request" && <MessageTab p={entry.request} protocol={entry.protocol} side="request" />}
        {active === "response" && <MessageTab p={entry.response} protocol={entry.protocol} side="response" />}
        {active === "headers" && <HeadersTab entry={entry} />}
        {active === "body" && <BodyTab entry={entry} />}
        {active === "raw" && <RawTab entry={entry} />}
        {active === "l4" && entry.l4 && <L4Tab l4={entry.l4} />}
      </div>
    </aside>
  );
}

// --- tab visibility ---------------------------------------------------------

function visibleTabs(e: Entry): TabId[] {
  const tabs: TabId[] = ["overview", "request", "response"];
  const isHTTP = e.protocol === "http";
  if (isHTTP && (hasHeaders(e.request) || hasHeaders(e.response))) tabs.push("headers");
  if (e.request.body || e.response.body) tabs.push("body");
  if (e.request.raw || e.response.raw) tabs.push("raw");
  if (e.l4) tabs.push("l4");
  return tabs;
}

const hasHeaders = (p: Payload) => !!p.headers && Object.keys(p.headers).length > 0;

// --- Overview ---------------------------------------------------------------

function OverviewTab({ entry }: { entry: Entry }) {
  const rows: Row[] = [];
  const { protocol: proto, request: rq, response: rs } = entry;
  if (proto === "http") {
    push(rows, "method", rq.method);
    push(rows, "path", rq.path);
    push(rows, "host", rq.host);
    push(rows, "status", rs.statusCode);
    push(rows, "content-type", rs.contentType);
  } else if (proto === "dns") {
    push(rows, "question", rq.question);
    push(rows, "type", rq.dns?.questions?.[0]?.type);
    push(rows, "answer", rs.answer);
    push(rows, "rcode", rs.dns?.rcode);
  } else if (proto === "redis" || proto === "valkey") {
    push(rows, "command", rq.command);
    push(rows, "db", rq.redis?.dbIndex);
    push(rows, "reply", rs.redis?.reply);
    push(rows, "reply type", rs.redis?.replyType);
  } else if (proto === "postgres") {
    push(rows, "query", rq.query);
    push(rows, "statement", rq.postgres?.statementName);
    push(rows, "tag", rs.postgres?.tag);
    push(rows, "tx", rs.postgres?.txStatus);
    push(rows, "rows", rs.rowCount);
    push(rows, "error", rs.postgres?.error?.code);
  } else if (proto === "amqp") {
    push(rows, "class", rq.class);
    push(rows, "method", rq.method);
    push(rows, "exchange", rq.exchange);
    push(rows, "routing key", rq.routingKey);
    push(rows, "queue", rq.queue);
  } else {
    push(rows, "flags", rq.flags);
    push(rows, "packets", rq.packets);
    push(rows, "bytes", rq.bytes);
  }
  return (
    <Section title="overview">
      <KV rows={rows} />
    </Section>
  );
}

// --- Request / Response -----------------------------------------------------

function MessageTab({
  p,
  protocol,
  side,
}: {
  p: Payload;
  protocol: string;
  side: "request" | "response";
}) {
  const rows: Row[] = [];
  if (protocol === "http") {
    if (side === "request") {
      push(rows, "method", p.method);
      push(rows, "path", p.path);
      push(rows, "host", p.host);
      push(rows, "version", p.http?.version);
    } else {
      push(rows, "status", p.statusCode);
      push(rows, "version", p.http?.version);
    }
    push(rows, "content-type", p.contentType);
    push(rows, "size", p.size ? `${p.size} B` : undefined);
  } else if (protocol === "postgres") {
    if (side === "request") push(rows, "query", p.query);
    else push(rows, "tag", p.postgres?.tag);
  } else if (protocol === "redis" || protocol === "valkey") {
    if (side === "request") push(rows, "command", p.command);
    else {
      push(rows, "reply", p.redis?.reply);
      push(rows, "reply type", p.redis?.replyType);
    }
  } else if (protocol === "amqp" && side === "request") {
    push(rows, "class", p.class);
    push(rows, "method", p.method);
    push(rows, "exchange", p.exchange);
    push(rows, "routing key", p.routingKey);
    push(rows, "queue", p.queue);
    push(rows, "delivery tag", p.deliveryTag);
  }

  return (
    <Section title={side}>
      <KV rows={rows} />

      {p.http?.query && Object.keys(p.http.query).length > 0 && (
        <MapBlock title="query params" m={p.http.query} />
      )}
      {p.redis?.args && p.redis.args.length > 0 && <ListBlock title="args" items={p.redis.args} />}
      {p.redis?.attributes && Object.keys(p.redis.attributes).length > 0 && (
        <MapBlock title="attributes" m={p.redis.attributes} />
      )}
      {p.postgres?.params && p.postgres.params.length > 0 && (
        <ListBlock title="bind params" items={p.postgres.params} />
      )}
      {p.postgres?.columns && p.postgres.columns.length > 0 && (
        <ColumnsTable cols={p.postgres.columns} />
      )}
      {p.postgres?.error && <PGErrorBlock err={p.postgres.error} />}
      {p.dns?.questions && p.dns.questions.length > 0 && (
        <RecordTable title="questions" recs={p.dns.questions.map((q) => ({ name: q.name, type: q.type, data: q.class || "" }))} />
      )}
      {p.dns?.answers && p.dns.answers.length > 0 && <RecordTable title="answers" recs={p.dns.answers} />}
      {p.dns?.authority && p.dns.authority.length > 0 && (
        <RecordTable title="authority" recs={p.dns.authority} />
      )}
      {p.dns?.additional && p.dns.additional.length > 0 && (
        <RecordTable title="additional" recs={p.dns.additional} />
      )}
    </Section>
  );
}

// --- Headers ----------------------------------------------------------------

function HeadersTab({ entry }: { entry: Entry }) {
  return (
    <Section title="headers">
      {hasHeaders(entry.request) && (
        <MapBlock title="request" m={entry.request.headers!} />
      )}
      {hasHeaders(entry.response) && (
        <MapBlock title="response" m={entry.response.headers!} />
      )}
    </Section>
  );
}

// --- Body -------------------------------------------------------------------

function BodyTab({ entry }: { entry: Entry }) {
  return (
    <Section title="body">
      {entry.request.body && <BodyBlock title="request" p={entry.request} />}
      {entry.response.body && <BodyBlock title="response" p={entry.response} />}
    </Section>
  );
}

function BodyBlock({ title, p }: { title: string; p: Payload }) {
  return (
    <>
      <div className="subhead">
        {title}
        {p.truncated && <span className="chip trunc">truncated</span>}
      </div>
      <pre className="body mono">{p.body}</pre>
    </>
  );
}

// --- Raw --------------------------------------------------------------------

function RawTab({ entry }: { entry: Entry }) {
  return (
    <Section title="raw">
      {entry.request.raw && <RawBlock title="request" raw={entry.request.raw} />}
      {entry.response.raw && <RawBlock title="response" raw={entry.response.raw} />}
    </Section>
  );
}

function RawBlock({ title, raw }: { title: string; raw: RawView }) {
  return (
    <>
      <div className="subhead">
        {title} · first {raw.bytes ?? 0} B of stream
        {raw.truncated && <span className="chip trunc">truncated</span>}
      </div>
      <pre className="hex">{raw.hex}</pre>
    </>
  );
}

// --- L4 ---------------------------------------------------------------------

function L4Tab({ l4 }: { l4: L4Info }) {
  const rows: Row[] = [];
  push(rows, "src mac", l4.srcMac);
  push(rows, "dst mac", l4.dstMac);
  push(rows, "ip version", l4.ipVersion);
  push(rows, "ttl", l4.ttl);
  push(rows, "ip flags", l4.ipFlags);
  push(rows, "client flags", l4.clientTcpFlags);
  push(rows, "server flags", l4.serverTcpFlags);
  push(rows, "seq start", l4.seqStart);
  push(rows, "ack start", l4.ackStart);
  push(rows, "window", l4.window);
  push(rows, "mss", l4.mss);
  push(rows, "rtt", l4.rttMs ? `${l4.rttMs} ms` : undefined);
  push(rows, "retransmits", l4.retransmits);
  push(rows, "duration", l4.durationMs ? `${l4.durationMs} ms` : undefined);
  push(rows, "client bytes", l4.clientBytes);
  push(rows, "server bytes", l4.serverBytes);
  push(rows, "client packets", l4.clientPackets);
  push(rows, "server packets", l4.serverPackets);
  if (l4.tls) {
    push(rows, "tls sni", l4.tls.sni);
    push(rows, "tls alpn", l4.tls.alpn);
    push(rows, "tls version", l4.tls.version);
    push(rows, "tls cipher", l4.tls.cipher);
  }
  return (
    <Section title="l4">
      <KV rows={rows} />
      {l4.headerHex && (
        <>
          <div className="subhead">header</div>
          <pre className="hex">{l4.headerHex}</pre>
        </>
      )}
    </Section>
  );
}

// --- shared building blocks --------------------------------------------------

type Row = [string, string];

function push(rows: Row[], k: string, v: string | number | undefined) {
  if (v === undefined || v === null || v === "" || v === 0) return;
  rows.push([k, String(v)]);
}

function KV({ rows }: { rows: Row[] }) {
  if (rows.length === 0) return <div className="empty-note">no data</div>;
  return (
    <table className="kv">
      <tbody>
        {rows.map(([k, v]) => (
          <tr key={k}>
            <td className="kv-k">{k}</td>
            <td className="kv-v mono">{v}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function MapBlock({ title, m }: { title: string; m: Record<string, string> }) {
  return (
    <>
      <div className="subhead">{title}</div>
      <table className="kv">
        <tbody>
          {Object.entries(m).map(([k, v]) => (
            <tr key={k}>
              <td className="kv-k">{k}</td>
              <td className="kv-v mono">{v}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}

function ListBlock({ title, items }: { title: string; items: string[] }) {
  return (
    <>
      <div className="subhead">{title}</div>
      <ol className="arglist mono">
        {items.map((it, i) => (
          <li key={i}>{it}</li>
        ))}
      </ol>
    </>
  );
}

function ColumnsTable({ cols }: { cols: PGColumn[] }) {
  return (
    <>
      <div className="subhead">columns</div>
      <table className="kv">
        <tbody>
          {cols.map((c, i) => (
            <tr key={i}>
              <td className="kv-k">{c.name}</td>
              <td className="kv-v mono">{c.type || (c.typeOid ? `oid ${c.typeOid}` : "")}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}

function RecordTable({ title, recs }: { title: string; recs: Array<{ name: string; type: string; ttl?: number; data: string }> }) {
  return (
    <>
      <div className="subhead">{title}</div>
      <table className="kv rec">
        <tbody>
          {recs.map((r, i) => (
            <tr key={i}>
              <td className="kv-k mono">{r.type}</td>
              <td className="kv-v mono">
                {r.name}
                {r.data ? ` → ${r.data}` : ""}
                {r.ttl ? ` (ttl ${r.ttl})` : ""}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}

function PGErrorBlock({ err }: { err: NonNullable<Payload["postgres"]>["error"] }) {
  if (!err) return null;
  const rows: Row[] = [];
  push(rows, "severity", err.severity);
  push(rows, "code", err.code);
  push(rows, "message", err.message);
  push(rows, "detail", err.detail);
  push(rows, "hint", err.hint);
  push(rows, "where", err.where);
  return (
    <>
      <div className="subhead">error</div>
      <KV rows={rows} />
    </>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="section">
      <div className="section-title">{title}</div>
      {children}
    </div>
  );
}

function EndpointCard({ title, ep }: { title: string; ep: Entry["src"] }) {
  return (
    <div className="ep-card">
      <div className="ep-title">{title}</div>
      <div className="ep-name mono">{ep.name || ep.ip}</div>
      <div className="ep-sub mono">
        {ep.namespace ? `${ep.namespace} · ` : ""}
        {ep.ip}:{ep.port}
      </div>
    </div>
  );
}

function Meta({ k, v }: { k: string; v: string }) {
  return (
    <div className="meta-item">
      <span className="meta-k">{k}</span>
      <span className="meta-v mono">{v}</span>
    </div>
  );
}
