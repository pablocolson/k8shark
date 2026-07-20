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
