// Minimal shape for a node/endpoint that can be pivoted to via an IFL clause
// -- satisfied structurally by both ServiceMap's graph Node and an Entry's
// src/dst Endpoint.
interface Locatable {
  name?: string;
  ip: string;
}

// endpointClause pivots to all traffic touching this identity in either role
// (src or dst) -- preferring the k8s-resolved name, falling back to the raw
// IP when unresolved. Shared by ServiceMap's node click and EntryDetail's
// "filter on this source/destination" actions.
export function endpointClause(ep: Locatable): string {
  return ep.name ? `dst.name == "${ep.name}" or src.name == "${ep.name}"` : `dst.ip == "${ep.ip}" or src.ip == "${ep.ip}"`;
}

// conversationClause pins the filter to one exact src/dst pair (Wireshark's
// "Follow TCP Stream" equivalent): resolved names when both sides have one
// (more durable across a pod's ephemeral IP than pinning by IP), else the
// raw IP pair.
export function conversationClause(src: Locatable, dst: Locatable): string {
  if (src.name && dst.name) return `src.name == "${src.name}" and dst.name == "${dst.name}"`;
  return `src.ip == "${src.ip}" and dst.ip == "${dst.ip}"`;
}

// groupClause pivots the Top view's aggregated rows back to matching traffic,
// keyed on the group-by field. A namespace key is a plain name; a workload key
// is the hub's "namespace/workload" node label (see nodeLabel in graph.go), so
// it splits into a namespace-scoped workload match on either endpoint. When a
// workload key has no namespace prefix (the label fell back to a bare
// name/IP), it matches the workload field alone.
export function groupClause(groupBy: "workload" | "namespace", key: string): string {
  if (groupBy === "namespace") {
    return `src.namespace == "${key}" or dst.namespace == "${key}"`;
  }
  const slash = key.indexOf("/");
  if (slash < 0) {
    return `src.workload == "${key}" or dst.workload == "${key}"`;
  }
  const ns = key.slice(0, slash);
  const wl = key.slice(slash + 1);
  return (
    `(src.namespace == "${ns}" and src.workload == "${wl}") or ` +
    `(dst.namespace == "${ns}" and dst.workload == "${wl}")`
  );
}
