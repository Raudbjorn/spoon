package settings

import (
	"os"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
)

type Source string

const (
	FlagSource        Source = "FLAG"
	EnvironmentSource Source = "ENV"
	FileSource        Source = "FILE"
	DefaultSource     Source = "DEFAULT"
)

type Resolved struct {
	Value    string
	Source   Source
	Detail   string
	Inactive string
}

// Resolve applies the documented flags > environment > config > default
// precedence without duplicating configuration parsing in the renderer.
func Resolve(field Field, cfg *config.Config, flags map[string]string) Resolved {
	if value, ok := flags[field.Key]; ok && strings.TrimSpace(value) != "" {
		return Resolved{Value: value, Source: FlagSource}
	}
	for _, name := range field.Environment {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			row := Resolved{Value: value, Source: EnvironmentSource, Detail: name + "=" + value}
			if cfg != nil && strings.TrimSpace(field.Get(cfg)) != "" {
				row.Inactive = "saved to config.json; the environment override wins until it is unset."
			}
			return row
		}
	}
	if cfg != nil {
		if value := field.Get(cfg); strings.TrimSpace(value) != "" && value != "0" && value != "false" {
			return Resolved{Value: value, Source: FileSource}
		}
	}
	return Resolved{Value: field.Default, Source: DefaultSource}
}

func FieldByMust(key string) Field {
	field, ok := FieldByKey(key)
	if !ok {
		panic("unknown settings key " + key)
	}
	return field
}

func EnvironmentValue(name string) string {
	value := os.Getenv(name)
	if Environment[name] {
		if value == "" {
			return "unset"
		}
		return "set"
	}
	if value == "" {
		return "unset"
	}
	return value
}
