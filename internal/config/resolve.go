package config

// ValueSource identifies the layer that supplied an effective setting.
type ValueSource string

const (
	SourceFlag        ValueSource = "FLAG"
	SourceEnvironment ValueSource = "ENV"
	SourceFile        ValueSource = "FILE"
	SourceDefault     ValueSource = "DEFAULT"
)

// ResolvedString is the typed, inspectable result of Spoon's standard
// flag > environment > loaded file > built-in default precedence.
type ResolvedString struct {
	Value       string
	Source      ValueSource
	Environment string
	Inactive    bool
}

// ResolveString applies the common precedence without treating legitimate
// values such as "false" or "0" as absent. env is injected by callers that
// already own environment access, which keeps tests hermetic.
func ResolveString(fileValue string, filePresent bool, defaultValue, flagValue string, env map[string]string, envNames ...string) ResolvedString {
	if flagValue != "" {
		return ResolvedString{Value: flagValue, Source: SourceFlag, Inactive: filePresent}
	}
	for _, name := range envNames {
		if value, ok := env[name]; ok && value != "" {
			return ResolvedString{Value: value, Source: SourceEnvironment, Environment: name, Inactive: filePresent}
		}
	}
	if filePresent {
		return ResolvedString{Value: fileValue, Source: SourceFile}
	}
	return ResolvedString{Value: defaultValue, Source: SourceDefault}
}
