package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigContainsCredentialsForEveryCredentialDescriptor(t *testing.T) {
	for _, descriptor := range CredentialDescriptors() {
		t.Run(descriptor.Key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			cfg := &Config{}
			descriptor.Set(cfg, "sentinel-"+descriptor.Key)
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if !configContainsCredentials(path) {
				t.Fatalf("credential descriptor %q was not detected", descriptor.Key)
			}
			descriptor.Set(cfg, "")
			if ContainsCredentials(cfg) {
				t.Fatalf("credential descriptor %q remained after clear", descriptor.Key)
			}
			data, err = json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if configContainsCredentials(path) {
				t.Fatalf("credential descriptor %q remained detected after clear", descriptor.Key)
			}
		})
	}
}
