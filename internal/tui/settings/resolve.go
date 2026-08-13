package settings

import (
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

// Resolve consumes a freshly captured process environment for direct Settings
// compatibility callers.
func Resolve(field Field, cfg *config.Config, flags map[string]string) Resolved {
	return ResolveWithEnvironment(field, cfg, flags, config.EnvironmentSnapshot())
}

func ResolveWithEnvironment(field Field, cfg *config.Config, flags, env map[string]string) Resolved {
	return ResolveFromEffective(field, config.ResolveEffectiveConfig(cfg, flags, env))
}

// ResolveFromEffective projects the command-owned typed runtime result into a
// Settings row without rereading config or environment.
func ResolveFromEffective(field Field, effective config.EffectiveConfig) Resolved {
	resolved := effective.Value(field.Key)
	if resolved.Source == "" {
		setting, ok := config.SettingByKey(field.Key)
		if !ok {
			panic("settings field has no effective descriptor: " + field.Key)
		}
		resolved = config.ResolvedString{Value: setting.Default, Source: config.SourceDefault}
	}
	row := Resolved{Value: resolved.Value, Source: resolved.Source}
	if resolved.Environment != "" {
		value := resolved.Value
		if field.IsCredential() || Environment[resolved.Environment] {
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

func EnvironmentValue(name string, env map[string]string) string {
	value := env[name]
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
