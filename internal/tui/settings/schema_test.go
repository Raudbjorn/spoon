package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestRegistryReflectsEveryPersistedConfigLeaf(t *testing.T) {
	want := map[string]bool{}
	configLeaves(reflect.TypeOf(config.Config{}), "", want)
	got := map[string]bool{}
	for _, field := range Registry {
		if got[field.Key] {
			t.Fatalf("duplicate registry key %q", field.Key)
		}
		got[field.Key] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registry mismatch\ngot:  %v\nwant: %v", got, want)
	}
}

func TestCredentialRegistryMatchesConfigPermissionGate(t *testing.T) {
	want := map[string]bool{}
	for _, descriptor := range config.CredentialDescriptors() {
		want[descriptor.Key] = true
		field, ok := FieldByKey(descriptor.Key)
		if !ok || !field.IsCredential() {
			t.Fatalf("credential descriptor %q is not exposed as a credential field", descriptor.Key)
		}
	}
	got := map[string]bool{}
	for _, field := range Registry {
		if field.IsCredential() {
			got[field.Key] = true
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("credential registry mismatch: got %v want %v", got, want)
	}
}

func configLeaves(typ reflect.Type, prefix string, out map[string]bool) {
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		if field.Type.Kind() == reflect.Struct {
			configLeaves(field.Type, key, out)
			continue
		}
		out[key] = true
	}
}

func TestEveryDocumentedEnvironmentVariableIsExposedAndRuntimeCovered(t *testing.T) {
	documented := map[string]bool{}
	for _, name := range config.DocumentedEnvironment() {
		documented[name] = true
		if _, ok := Environment[name]; !ok {
			t.Errorf("missing environment entry %s", name)
		}
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	re := regexp.MustCompile(`os\.Getenv\("(SPOON_[A-Z0-9_]+)"\)`)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && (info.Name() == "vendor" || strings.HasPrefix(info.Name(), ".")) {
			return filepath.SkipDir
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range re.FindAllStringSubmatch(string(data), -1) {
			if !documented[match[1]] {
				t.Errorf("runtime %s is not documented/exposed", match[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
