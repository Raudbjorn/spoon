package config

import (
	"encoding/json"
	"maps"
	"strings"
)

// FieldPresent reports whether key was explicitly present in the loaded or
// saved JSON layer. It preserves false and zero as intentional file values.
func FieldPresent(c *Config, key string) bool { return c != nil && c.present[key] }

// RecordFieldValue marks a settings edit as explicit. Empty strings deliberately
// remove the leaf, while bool/numeric settings retain their zero values.
func RecordFieldValue(c *Config, key, value string) {
	if c == nil {
		return
	}
	if c.present == nil {
		c.present = map[string]bool{}
	}
	if c.raw == nil {
		c.raw = map[string]json.RawMessage{}
	}
	if value == "" {
		delete(c.present, key)
		delete(c.raw, key)
		return
	}
	c.present[key] = true
	if boolOrNumberField(key) {
		c.raw[key] = json.RawMessage(value)
		return
	}
	encoded, _ := json.Marshal(value)
	c.raw[key] = encoded
}

func boolOrNumberField(key string) bool {
	switch key {
	case "github.proxy.enabled", "embedder.voyage.disabled", "github.requestsPerMinute", "embedder.maxLength", "embedder.batchSize", "embedder.voyage.outputDimension":
		return true
	default:
		return false
	}
}

// Clone returns an independent Config while retaining source-presence metadata
// used by settings candidates before they are atomically published.
func Clone(c *Config) (*Config, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var clone Config
	if err := json.Unmarshal(data, &clone); err != nil {
		return nil, err
	}
	clone.present, clone.raw = copyPresence(c.present), copyRaw(c.raw)
	clone.secretRefs = maps.Clone(c.secretRefs)
	return &clone, nil
}

func copyPresence(in map[string]bool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
func copyRaw(in map[string]json.RawMessage) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

func leafMetadata(data []byte) (map[string]bool, map[string]json.RawMessage) {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil {
		return nil, nil
	}
	present, raw := map[string]bool{}, map[string]json.RawMessage{}
	var walk func(string, map[string]json.RawMessage)
	walk = func(prefix string, values map[string]json.RawMessage) {
		for key, value := range values {
			name := key
			if prefix != "" {
				name = prefix + "." + key
			}
			var nested map[string]json.RawMessage
			if json.Unmarshal(value, &nested) == nil && nested != nil {
				walk(name, nested)
				continue
			}
			present[name] = true
			raw[name] = append(json.RawMessage(nil), value...)
		}
	}
	walk("", root)
	return present, raw
}

// MarshalJSON preserves explicitly present zero values which Go's omitempty
// would otherwise erase. It always serializes the current typed value rather
// than replaying the loaded bytes, so ordinary direct mutations remain valid.
func (c Config) MarshalJSON() ([]byte, error) {
	type wire Config
	data, err := json.Marshal(wire(c))
	if err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	for key := range c.present {
		if !boolOrNumberField(key) || hasRawPath(root, strings.Split(key, ".")) {
			continue
		}
		raw, ok := c.currentZeroValue(key)
		if !ok {
			continue
		}
		setRawPath(root, strings.Split(key, "."), raw)
	}
	return json.Marshal(root)
}

func (c Config) currentZeroValue(key string) (json.RawMessage, bool) {
	var value any
	switch key {
	case "github.proxy.enabled":
		value = c.GitHub.Proxy.Enabled
	case "embedder.voyage.disabled":
		value = c.Embedder.Voyage.Disabled
	case "github.requestsPerMinute":
		value = c.GitHub.RequestsPerMinute
	case "embedder.maxLength":
		value = c.Embedder.MaxLength
	case "embedder.batchSize":
		value = c.Embedder.BatchSize
	case "embedder.voyage.outputDimension":
		value = c.Embedder.Voyage.OutputDimension
	default:
		return nil, false
	}
	raw, err := json.Marshal(value)
	return raw, err == nil
}

func hasRawPath(root map[string]json.RawMessage, parts []string) bool {
	if len(parts) == 0 {
		return false
	}
	value, ok := root[parts[0]]
	if !ok {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	var nested map[string]json.RawMessage
	return json.Unmarshal(value, &nested) == nil && hasRawPath(nested, parts[1:])
}
func setRawPath(root map[string]json.RawMessage, parts []string, value json.RawMessage) {
	if len(parts) == 0 {
		return
	}
	if len(parts) == 1 {
		root[parts[0]] = value
		return
	}
	var child map[string]json.RawMessage
	if existing := root[parts[0]]; existing != nil {
		_ = json.Unmarshal(existing, &child)
	}
	if child == nil {
		child = map[string]json.RawMessage{}
	}
	setRawPath(child, parts[1:], value)
	encoded, _ := json.Marshal(child)
	root[parts[0]] = encoded
}
