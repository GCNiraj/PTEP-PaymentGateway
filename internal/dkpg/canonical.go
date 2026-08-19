package dkpg

import (
	"bytes"
	"encoding/json"
	"sort"
)

// canonicalJSON serializes payload into deterministic JSON (sorted object keys).
// Why needed: DK signature generation must use stable canonical content.
// Called from: Client.signedPost before JWT signature creation.
func canonicalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}

	var data any
	if err := json.Unmarshal(b, &data); err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := writeCanonical(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// writeCanonical recursively writes canonical JSON for maps/arrays/scalars.
// Called from: canonicalJSON.
func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
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
			keyBytes, _ := json.Marshal(k)
			buf.Write(keyBytes)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	case []any:
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(b)
		return nil
	}
}
