package hub

import (
	"sort"
	"strings"
	"sync"

	"github.com/pablocolson/k8shark/pkg/api"
)

// FieldType identifies how a filterable field's values should be presented
// (and compared) in the IFL autocomplete UI.
type FieldType string

const (
	FieldTypeEnum     FieldType = "enum"     // small closed domain, == / !=
	FieldTypeNumber   FieldType = "number"   // numeric compares, unquoted values
	FieldTypeString   FieldType = "string"   // discrete-ish string, value list offered
	FieldTypeFreetext FieldType = "freetext" // high-cardinality/unbounded, no value list
)

// FieldValue is one observed (or statically known) value for a field, with
// its occurrence count. This is the hub-local JSON shape shared by
// /api/fields and /api/fields/{field}/values.
type FieldValue struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// FieldSpec describes one filterable field for the /api/fields catalog.
type FieldSpec struct {
	Name        string // canonical name, e.g. "dst.namespace"
	Type        FieldType
	Operators   []string // valid ops, in menu order
	TrackValues bool     // facet index records observed values for this field
	EnumValues  []string // static domain always offered (e.g. protocol, status)
}

// Operator sets by field type.
var (
	opsEnum   = []string{"==", "!="}
	opsString = []string{"==", "!=", "contains"}
	opsNumber = []string{"==", "!=", ">", "<", ">=", "<="}
	opsText   = []string{"==", "!=", "contains"}
)

// fieldCatalog is the authoritative list of everything the IFL autocomplete
// UI can offer, in the stable order /api/fields reports them. It mirrors the
// switch in fieldGetter (filter.go) but collapses aliases to a single
// canonical name. Value extraction reuses fieldGetter directly -- no
// duplicated accessors here.
var fieldCatalog = []FieldSpec{
	{Name: "protocol", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true,
		EnumValues: []string{"http", "dns", "redis", "valkey", "postgres", "tcp", "udp", "icmp", "amqp"}},
	{Name: "status", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true,
		EnumValues: []string{"success", "warning", "error"}},
	{Name: "node", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "elapsedMs", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "namespace", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "src.name", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "src.namespace", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "src.workload", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "dst.name", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "dst.namespace", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "dst.workload", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "src.ip", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "dst.ip", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "src.port", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "dst.port", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: true},
	{Name: "http.method", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true},
	{Name: "response.status", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: true},
	{Name: "request.host", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "request.path", Type: FieldTypeFreetext, Operators: opsText, TrackValues: false},
	{Name: "dns.question", Type: FieldTypeFreetext, Operators: opsText, TrackValues: false},
	{Name: "dns.answer", Type: FieldTypeFreetext, Operators: opsText, TrackValues: false},
	{Name: "redis.command", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true},
	{Name: "postgres.query", Type: FieldTypeFreetext, Operators: opsText, TrackValues: false},
	{Name: "request.body", Type: FieldTypeFreetext, Operators: opsText, TrackValues: false},
	{Name: "response.body", Type: FieldTypeFreetext, Operators: opsText, TrackValues: false},
	{Name: "bytes", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "packets", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "flags", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "summary", Type: FieldTypeFreetext, Operators: opsText, TrackValues: false},

	// Richer sub-object fields (WS3). Enum/low-cardinality fields are tracked;
	// freetext and high-cardinality numerics are not (see performance bounds).
	{Name: "http.version", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true},
	{Name: "response.contenttype", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "dns.rcode", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true},
	{Name: "dns.type", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true},
	{Name: "redis.db", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: true},
	{Name: "redis.reply", Type: FieldTypeFreetext, Operators: opsText, TrackValues: false},
	{Name: "postgres.error", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "postgres.statement", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "postgres.txstatus", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true,
		EnumValues: []string{"I", "T", "E"}},
	{Name: "l4.ttl", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: true},
	{Name: "l4.retransmits", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "l4.window", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "l4.mss", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: true},
	{Name: "l4.rttms", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "l4.durationms", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "l4.clientbytes", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "l4.serverbytes", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "tls.sni", Type: FieldTypeString, Operators: opsString, TrackValues: true},

	// AMQP (WS5).
	{Name: "amqp.class", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true,
		EnumValues: []string{"Connection", "Channel", "Exchange", "Queue", "Basic"}},
	{Name: "amqp.method", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true},
	{Name: "amqp.exchange", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "amqp.routingkey", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "amqp.queue", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "amqp.deliverytag", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},

	// Previously display-only fields, now filterable (roadmap: "champs backend
	// déjà calculés, invisibles côté UI").
	{Name: "redis.pipelinedepth", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: true},
	{Name: "postgres.portal", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "dns.authoritative", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true,
		EnumValues: []string{"true", "false"}},
	{Name: "dns.recursionavailable", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true,
		EnumValues: []string{"true", "false"}},
	{Name: "request.size", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "response.size", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "postgres.rowcount", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "http.ttfbms", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},

	// Remaining L4Info fields (previously view-only in the detail L4 tab).
	{Name: "l4.srcmac", Type: FieldTypeString, Operators: opsString, TrackValues: false},
	{Name: "l4.dstmac", Type: FieldTypeString, Operators: opsString, TrackValues: false},
	{Name: "l4.ipversion", Type: FieldTypeEnum, Operators: opsEnum, TrackValues: true,
		EnumValues: []string{"4", "6"}},
	{Name: "l4.ipflags", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "l4.clienttcpflags", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "l4.servertcpflags", Type: FieldTypeString, Operators: opsString, TrackValues: true},
	{Name: "l4.seqstart", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "l4.ackstart", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "l4.clientpackets", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
	{Name: "l4.serverpackets", Type: FieldTypeNumber, Operators: opsNumber, TrackValues: false},
}

// fieldSpecByName is the derived lookup table built once at init.
var fieldSpecByName map[string]FieldSpec

func init() {
	fieldSpecByName = make(map[string]FieldSpec, len(fieldCatalog))
	for _, spec := range fieldCatalog {
		fieldSpecByName[spec.Name] = spec
	}
}

const (
	// facetTrackCap bounds how many distinct values are tracked per field, so
	// memory stays flat regardless of cardinality (e.g. free-form request
	// hosts or IPs).
	facetTrackCap = 200
	// facetTopN is how many top values (by count) are surfaced per field in
	// the /api/fields bulk snapshot.
	facetTopN = 50
)

// fieldCounter holds observed value counts for a single tracked field. It has
// no mutex of its own -- callers hold the parent facetIndex's mu.
type fieldCounter struct {
	counts map[string]int64
}

// increment bumps v's count. If v is a brand new key and the counter is
// already at facetTrackCap distinct values, the single lowest-count entry is
// evicted first (linear scan -- fine at this scale; this is space-saving in
// spirit, not a strict guarantee). Caller holds facetIndex.mu.
func (fc *fieldCounter) increment(v string) {
	if _, exists := fc.counts[v]; !exists && len(fc.counts) >= facetTrackCap {
		var evictKey string
		found := false
		var evictCount int64
		for k, c := range fc.counts {
			if !found || c < evictCount {
				evictKey, evictCount, found = k, c, true
			}
		}
		if found {
			delete(fc.counts, evictKey)
		}
	}
	fc.counts[v]++
}

// facetIndex tracks observed values per field, for IFL autocomplete. It owns
// its own mutex, fully decoupled from store.mu.
type facetIndex struct {
	mu     sync.Mutex
	fields map[string]*fieldCounter // canonical field name -> counter (TrackValues fields only)
}

// newFacetIndex builds an index pre-populated with one counter per
// TrackValues==true entry in fieldCatalog.
func newFacetIndex() *facetIndex {
	f := &facetIndex{fields: make(map[string]*fieldCounter)}
	for _, spec := range fieldCatalog {
		if spec.TrackValues {
			f.fields[spec.Name] = &fieldCounter{counts: map[string]int64{}}
		}
	}
	return f
}

// observe records one entry's tracked field values. Reuses fieldGetter from
// filter.go for value extraction -- no duplicated field-accessor logic.
func (f *facetIndex) observe(e *api.Entry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name, fc := range f.fields {
		get := fieldGetter(name)
		if get == nil {
			continue // catalog/getter drift — never panic the ingest path
		}
		v := get(e)
		if v == "" {
			continue
		}
		fc.increment(v)
	}
}

// values returns field's observed values matching prefix (case-insensitive
// prefix match), sorted by count descending then value ascending as a
// tiebreak, truncated to limit. The second return is whether field is
// tracked at all (false for freetext/untracked/unknown fields).
func (f *facetIndex) values(field, prefix string, limit int) ([]FieldValue, bool) {
	f.mu.Lock()
	fc, ok := f.fields[field]
	if !ok {
		f.mu.Unlock()
		return nil, false
	}
	lowerPrefix := strings.ToLower(prefix)
	out := make([]FieldValue, 0, len(fc.counts))
	for v, c := range fc.counts {
		if prefix == "" || strings.HasPrefix(strings.ToLower(v), lowerPrefix) {
			out = append(out, FieldValue{Value: v, Count: c})
		}
	}
	f.mu.Unlock()

	sortFieldValues(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, true
}

// snapshot returns the top facetTopN values (count desc, value asc tiebreak)
// for every tracked field. Used to build the /api/fields bulk response.
func (f *facetIndex) snapshot() map[string][]FieldValue {
	f.mu.Lock()
	raw := make(map[string][]FieldValue, len(f.fields))
	for name, fc := range f.fields {
		vals := make([]FieldValue, 0, len(fc.counts))
		for v, c := range fc.counts {
			vals = append(vals, FieldValue{Value: v, Count: c})
		}
		raw[name] = vals
	}
	f.mu.Unlock()

	out := make(map[string][]FieldValue, len(raw))
	for name, vals := range raw {
		sortFieldValues(vals)
		if len(vals) > facetTopN {
			vals = vals[:facetTopN]
		}
		out[name] = vals
	}
	return out
}

// sortFieldValues sorts count descending, then value ascending as a tiebreak.
func sortFieldValues(vals []FieldValue) {
	sort.Slice(vals, func(i, j int) bool {
		if vals[i].Count != vals[j].Count {
			return vals[i].Count > vals[j].Count
		}
		return vals[i].Value < vals[j].Value
	})
}
