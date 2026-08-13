package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRawV1AndV2RoundTripAreLossless(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	raw := `{"version":1,"forge":{"provider":"gitlab","host":"forge.test"},"github":{"tokens":["one","two"],"requestsPerMinute":300,"proxy":{"enabled":true,"apiKeyFile":"` + key + `","staticFile":"` + key + `","whitelistPublicIp":false,"cacheTtl":"5m"}},"embedder":{"backend":"fastembed","model":"bge","cacheDir":"` + dir + `","maxLength":512,"batchSize":32,"voyage":{"apiKeyFile":"` + key + `","embedModel":"embed","rerankModel":"rerank","outputDimension":1024,"baseUrl":"https://example.test"}}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	v1, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, v1); err != nil {
		t.Fatal(err)
	}
	v2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Version != CurrentVersion {
		t.Fatalf("version = %d", v2.Version)
	}
	want := *v1
	want.Version = CurrentVersion
	got := *v2
	got.present = nil
	want.present = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("v1 migration changed fields\nwant %#v\ngot %#v", want, got)
	}
	if err := Save(path, v2); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got = *again
	got.present = nil
	want = *v2
	want.present = nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("v2 round trip changed fields")
	}
}
