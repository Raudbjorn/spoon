package settings

import (
	"fmt"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
)

// Save removes cleared credentials before delegating validation, permission
// hardening, atomic replacement and schema stamping to config.Save.
func Save(path string, cfg *config.Config) error {
	cfg.GitHub.Tokens = nonEmpty(cfg.GitHub.Tokens)
	if err := cfg.Validate(); err != nil {
		return err
	}
	return config.Save(path, cfg)
}

func nonEmpty(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func Apply(field Field, cfg *config.Config, value string) error {
	if !field.Editable || field.Set == nil {
		return fmt.Errorf("%s is read-only", field.Key)
	}
	if err := field.Set(cfg, value); err != nil {
		return err
	}
	return cfg.Validate()
}

func RequiresConfirmation(field Field) bool { return field.Consequence != NoConsequence }

func ConsequenceMessage(field Field) string {
	switch field.Consequence {
	case Billing:
		return "This change can enable Voyage, an external service billed per token."
	case Reindex:
		return "This changes vector/index partitioning. Existing Voyage rows keep the old identity and documents become pending."
	case Hostwide:
		return "This saves the system configuration and affects every user on this machine."
	default:
		return ""
	}
}
