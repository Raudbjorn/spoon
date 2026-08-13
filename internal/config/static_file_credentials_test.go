package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStaticProxyCredentialPathRequiresPrivateConfig(t *testing.T) {
	dir := t.TempDir()
	static := filepath.Join(dir, "static.txt")
	if err := os.WriteFile(static, []byte("proxy"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	data := []byte(`{"github":{"proxy":{"staticFile":"` + static + `"}}}`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("static proxy credential path bypassed private-config gate")
	}
}
