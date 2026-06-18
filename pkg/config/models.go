package config

import (
	"bytes"
	"encoding/json"
	"slices"
)

// Models is an ordered map of model name → config. JSON object key order
// is preserved during unmarshal — Go's default map decoding drops order,
// which would lose the user's intent in settings.json and the server-side
// sort order returned by OpenRouter's /models endpoint.
//
// The zero value is a usable empty Models.
type Models struct {
	keys  []string
	store map[string]ModelConfig
}

// UnmarshalJSON parses a JSON object into Models, preserving key order.
// Source: encoding/json #27179 — Go map decoding loses object order.
func (m *Models) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))

	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return &json.UnmarshalTypeError{Value: "non-object", Type: nil}
	}

	m.keys = m.keys[:0]
	m.store = make(map[string]ModelConfig)

	for dec.More() {
		// Object key.
		nameTok, err := dec.Token()
		if err != nil {
			return err
		}
		name, ok := nameTok.(string)
		if !ok {
			return &json.UnmarshalTypeError{Value: "non-string key"}
		}

		// Object value — use RawMessage so we can detect duplicates
		// (preserve first occurrence order) without losing ModelConfig detail.
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		var mc ModelConfig
		if err := json.Unmarshal(raw, &mc); err != nil {
			return err
		}

		if _, exists := m.store[name]; !exists {
			m.keys = append(m.keys, name)
		}
		m.store[name] = mc
	}

	// Consume closing '}'.
	if _, err := dec.Token(); err != nil {
		return err
	}
	return nil
}

// MarshalJSON encodes Models back into a JSON object, preserving key order.
func (m Models) MarshalJSON() ([]byte, error) {
	if m.store == nil || len(m.keys) == 0 {
		return []byte("{}"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range m.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		vb, err := json.Marshal(m.store[k])
		if err != nil {
			return nil, err
		}
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Get returns the config for name and whether it existed.
func (m *Models) Get(name string) (ModelConfig, bool) {
	if m.store == nil {
		return ModelConfig{}, false
	}
	mc, ok := m.store[name]
	return mc, ok
}

// Has reports whether name exists.
func (m *Models) Has(name string) bool {
	_, ok := m.Get(name)
	return ok
}

// Set inserts or updates name. New names are appended to the key order.
func (m *Models) Set(name string, c ModelConfig) {
	if m.store == nil {
		m.store = make(map[string]ModelConfig)
	}
	if _, exists := m.store[name]; !exists {
		m.keys = append(m.keys, name)
	}
	m.store[name] = c
}

// NewModelsFromMap builds a Models from a Go map. Key order is unspecified
// (Go map iteration), so this is intended only for tests where order doesn't
// matter. Production callers should use Set() in order or rely on UnmarshalJSON.
func NewModelsFromMap(source map[string]ModelConfig) Models {
	var m Models
	for name, mc := range source {
		m.Set(name, mc)
	}
	return m
}

// Len returns the number of models.
func (m *Models) Len() int {
	return len(m.keys)
}

// Ordered returns the model names in their original (JSON or insertion) order.
func (m *Models) Ordered() []string {
	return slices.Clone(m.keys)
}
