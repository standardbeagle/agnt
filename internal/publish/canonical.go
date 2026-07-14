package publish

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// CanonicalJSON returns a deterministic byte encoding of v: object keys sorted,
// strings encoded without HTML escaping, no map-iteration nondeterminism. Two
// values that are semantically equal produce byte-identical output, so a sha256
// over these bytes is a stable digest (spec §"Deterministic canonical JSON +
// digest").
func CanonicalJSON(v any) ([]byte, error) {
	// Marshal through the typed value first (deterministic field order), then
	// re-decode into a generic tree with UseNumber so numbers keep their literal
	// form, then emit canonically. Routing everything through the typed struct
	// means a decode/encode round-trip normalizes to the same canonical bytes.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, tree); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Digest returns the hex sha256 of the canonical encoding of v. Stable across
// runs and across an encode/decode cycle.
func Digest(v any) (string, error) {
	b, err := CanonicalJSON(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case json.Number:
		buf.WriteString(t.String())
	case string:
		writeCanonicalString(buf, t)
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeCanonicalString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return errf("canonical: unsupported type %T", v)
	}
	return nil
}

// writeCanonicalString writes s as a JSON string with HTML escaping disabled so
// the output is stable and readable ('<', '>', '&' are not \u-escaped).
func writeCanonicalString(buf *bytes.Buffer, s string) {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	// Encode cannot fail for a string; ignore err. Trailing newline trimmed.
	_ = enc.Encode(s)
	buf.Write(bytes.TrimRight(tmp.Bytes(), "\n"))
}
