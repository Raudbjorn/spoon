package settings

import (
	"os"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
)

type Source = config.ValueSource

const (
	FlagSource        = config.SourceFlag
	EnvironmentSource = config.SourceEnvironment
	FileSource        = config.SourceFile
	DefaultSource     = config.SourceDefault
)

type Resolved struct {
	Value    string
	Source   Source
	Detail   string
	Inactive string
}

// Resolve delegates precedence to config.ResolveString, the same typed layer
// runtime consumers use. Field-specific presence avoids treating false and 0
// as absent merely because they are useful values.
func Resolve(field Field, cfg *config.Config, flags map[string]string) Resolved {
	fileValue, filePresent := "", false
	if cfg != nil {
		fileValue = field.Get(cfg)
		filePresent = fieldPresent(field, cfg)
	}
	env := make(map[string]string, len(field.Environment))
	for _, name := range field.Environment {
		env[name] = os.Getenv(name)
	}
	flag := ""
	if flags != nil {
		flag = flags[field.Key]
	}
	resolved := config.ResolveString(fileValue, filePresent, field.Default, flag, env, field.Environment...)
	row := Resolved{Value: resolved.Value, Source: resolved.Source}
	if resolved.Environment != "" {
		value := resolved.Value
		if field.Secret || Environment[resolved.Environment] {
			value = "set"
		}
		row.Detail = resolved.Environment + "=" + value
	}
	if resolved.Inactive {
		row.Inactive = "saved to config.json; a higher-priority override is active until removed."
	}
	return row
}

func fieldPresent(field Field, cfg *config.Config) bool {
	if field.Key == "github.proxy.whitelistPublicIp" {
		return cfg.GitHub.Proxy.WhitelistPublicIP != nil
	}
	value := field.Get(cfg)
	if value == "false" || value == "0" {
		return false
	}
	return strings.TrimSpace(value) != ""
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
