package claudeimport

import (
	"bytes"
	"encoding/json"
	"sort"
)

// GatewayState is the slice of the daemon's current state needed to
// compute import-time diffs. Callers populate it once per
// import-snapshot call and reuse it across rows.
//
// Entries is keyed by server name in the gateway. Each value is the
// JSON-marshalled ServerConfig. The diff compares this against the
// candidate row's bytes via canonical-form comparison.
type GatewayState struct {
	Entries map[string]json.RawMessage
}

// DriftFields lists the entry-config keys that differ between the
// candidate import row and the gateway's current entry of the same
// name. Empty list = no drift = safe to overwrite. Non-empty = local
// edits exist that an `overwrite` action would discard.
//
// The fields tracked are the union of object keys present in either
// side. The comparison is canonical-form: marshal-decode-marshal both
// sides into a map, then compare per-key bytes.
func DriftFields(candidate json.RawMessage, gateway GatewayState, name string) []string {
	gw, present := gateway.Entries[name]
	if !present {
		// No existing entry → no drift to report (this is an "add",
		// not a conflict).
		return nil
	}

	candObj, ok := decodeAsObject(candidate)
	if !ok {
		return nil
	}
	gwObj, ok := decodeAsObject(gw)
	if !ok {
		return nil
	}

	keySet := map[string]struct{}{}
	for k := range candObj {
		keySet[k] = struct{}{}
	}
	for k := range gwObj {
		keySet[k] = struct{}{}
	}

	var differing []string
	for k := range keySet {
		if !bytes.Equal(canonicalize(candObj[k]), canonicalize(gwObj[k])) {
			differing = append(differing, k)
		}
	}
	sort.Strings(differing)
	return differing
}

// decodeAsObject parses raw as a JSON object. Returns ok=false when
// raw is not an object (e.g. null, an array, a scalar) — callers treat
// that as "no drift to report" rather than an error.
func decodeAsObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	// json.Unmarshal of `null` into a map succeeds (sets the map
	// to nil) — explicitly reject so callers can distinguish.
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	out := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

// canonicalize produces a stable byte representation of v so that
// `{"a":1,"b":2}` compares equal to `{"b":2,"a":1}`. Used to suppress
// false-positive drift on rows whose only difference is field-order.
//
// Implementation: decode into interface{}, then re-marshal with
// json.Marshal which sorts map keys.
func canonicalize(v json.RawMessage) []byte {
	if len(v) == 0 {
		return nil
	}
	var generic any
	if err := json.Unmarshal(v, &generic); err != nil {
		return v
	}
	out, err := json.Marshal(generic)
	if err != nil {
		return v
	}
	return out
}
