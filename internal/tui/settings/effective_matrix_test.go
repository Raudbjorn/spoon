package settings

import (
	"reflect"
	"strings"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestEveryEditableFieldUsesCentralEffectiveDescriptorMatrix(t *testing.T) {
	for _, field := range Registry {
		if !field.Editable {
			continue
		}
		t.Run(field.Key, func(t *testing.T) {
			descriptor, ok := config.SettingByKey(field.Key)
			if !ok {
				t.Fatalf("editable field %q has no central effective descriptor", field.Key)
			}
			if field.Default != descriptor.Default || !reflect.DeepEqual(field.Environment, descriptor.Environment) {
				t.Fatalf("field metadata drifted from central descriptor: field=%+v descriptor=%+v", field, descriptor)
			}

			file := &config.Config{}
			fileValue := matrixValue(field.Key)
			if err := field.Set(file, fileValue); err != nil {
				t.Fatal(err)
			}
			config.RecordFieldValue(file, field.Key, fileValue)
			fileValue = field.Get(file)

			fileRow := ResolveFromEffective(field, config.ResolveEffectiveConfig(file, nil, nil))
			if fileRow.Source != FileSource || fileRow.Value != fileValue || fileRow.Inactive != "" {
				t.Fatalf("file resolution = %+v, want file value %q", fileRow, fileValue)
			}

			defaultEffective := config.ResolveEffectiveConfig(&config.Config{}, nil, nil)
			defaultRow := ResolveFromEffective(field, defaultEffective)
			if defaultRow.Source != DefaultSource || defaultRow.Value != defaultEffective.Value(field.Key).Value {
				t.Fatalf("default resolution = %+v, effective value = %q", defaultRow, defaultEffective.Value(field.Key).Value)
			}

			if len(descriptor.Environment) != 0 {
				envName := descriptor.Environment[0]
				envValue := matrixEnvironmentValue(field.Key)
				envRow := ResolveFromEffective(field, config.ResolveEffectiveConfig(file, nil, map[string]string{envName: envValue}))
				if envRow.Source != EnvironmentSource || envRow.Inactive == "" {
					t.Fatalf("environment resolution = %+v", envRow)
				}
			}

			flagRow := ResolveFromEffective(field, config.ResolveEffectiveConfig(file, map[string]string{field.Key: "flag-source"}, nil))
			if flagRow.Source != FlagSource || flagRow.Value != "flag-source" || flagRow.Inactive == "" {
				t.Fatalf("flag resolution = %+v", flagRow)
			}
		})
	}
}

func matrixValue(key string) string {
	switch key {
	case "github.requestsPerMinute":
		return "321"
	case "github.proxy.enabled", "github.proxy.whitelistPublicIp", "embedder.voyage.disabled":
		return "true"
	case "embedder.maxLength":
		return "513"
	case "embedder.batchSize":
		return "33"
	case "embedder.voyage.outputDimension":
		return "512"
	case "ui.theme":
		return "amber"
	case "ui.color":
		return "ansi16"
	case "ui.glyphs":
		return "ascii"
	case "embedder.backend":
		return "fastembed"
	case "forge.provider":
		return "gitlab"
	case "github.proxy.cacheTtl":
		return "2m"
	}
	return "file-" + strings.ReplaceAll(key, ".", "-")
}

func matrixEnvironmentValue(key string) string {
	switch key {
	case "embedder.voyage.disabled":
		return "1"
	case "embedder.voyage.outputDimension":
		return "512"
	case "ui.color":
		return "ansi16"
	}
	return "env-source"
}
