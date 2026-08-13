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

// Resolve consumes the central runtime-effective configuration result. The
// Settings screen therefore reports exactly the defaults and override source
// that command startup will use rather than maintaining a parallel table.
func Resolve(field Field, cfg *config.Config, flags map[string]string) Resolved {
	env := map[string]string{}
	for _, setting := range config.Settings() {
		for _, name := range setting.Environment {
			env[name] = os.Getenv(name)
		}
	}
	// These influence derived runtime values but are not themselves settings.
	env["XDG_CACHE_HOME"] = os.Getenv("XDG_CACHE_HOME")
	resolved := config.ResolveEffectiveConfig(cfg, flags, env).Value(field.Key)
	if resolved.Source == "" {
		fileValue, filePresent := "", false
		if cfg != nil {
			fileValue, filePresent = field.Get(cfg), fieldPresent(field, cfg)
		}
		resolved = config.ResolveString(fileValue, filePresent, field.Default, flags[field.Key], env, field.Environment...)
	}
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
	if config.FieldPresent(cfg, field.Key) {
		return true
	}
	if field.Key == "github.proxy.whitelistPublicIp" {
		return cfg.GitHub.Proxy.WhitelistPublicIP != nil
	}
	value := field.Get(cfg)
	return value != "false" && value != "0" && strings.TrimSpace(value) != ""
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
