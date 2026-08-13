package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigContainsCredentialsForEveryCredentialField(t *testing.T) {
	cases := []string{
		`{"github":{"tokens":["token"]}}`,
		`{"github":{"proxy":{"apiKeyFile":"/tmp/key"}}}`,
		`{"embedder":{"voyage":{"apiKeyFile":"/tmp/key"}}}`,
	}
	for _, raw := range cases {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if !configContainsCredentials(path) {
			t.Fatalf("credential fixture not detected: %s", raw)
		}
	}
}
