package config

import "encoding/json"

// FieldPresent reports whether key was explicitly present in the loaded or
// saved JSON layer. It preserves false and zero as intentional file values.
func FieldPresent(c *Config, key string) bool {
	return c != nil && c.present[key]
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
	if len(c.present) != 0 {
		clone.present = make(map[string]bool, len(c.present))
		for key, value := range c.present {
			clone.present[key] = value
		}
	}
	return &clone, nil
}

func leafPresence(data []byte) map[string]bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	present := make(map[string]bool)
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
		}
	}
	walk("", raw)
	return present
}
