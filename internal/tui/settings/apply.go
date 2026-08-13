package settings

import (
	"fmt"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
)

// Clone makes an independent candidate, including token slices and optional
// booleans, before any validation-sensitive mutation reaches the live model.
func Clone(cfg *config.Config) (*config.Config, error) {
	if cfg == nil {
		return nil, fmt.Errorf("configuration layer is disabled")
	}
	return config.Clone(cfg)
}

// Candidate applies one field to an isolated copy and validates it. Callers
// assign it only after success, so failed edits cannot corrupt in-memory state.
func Candidate(field Field, cfg *config.Config, value string) (*config.Config, error) {
	if !field.Editable || field.Set == nil {
		return nil, fmt.Errorf("%s is read-only", field.Key)
	}
	candidate, err := Clone(cfg)
	if err != nil {
		return nil, err
	}
	if err := field.Set(candidate, value); err != nil {
		return nil, err
	}
	candidate.GitHub.Tokens = nonEmpty(candidate.GitHub.Tokens)
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	return candidate, nil
}

// Apply preserves the historical API but is transactional: cfg changes only
// after Candidate has validated successfully.
func Apply(field Field, cfg *config.Config, value string) error {
	candidate, err := Candidate(field, cfg, value)
	if err != nil {
		return err
	}
	*cfg = *candidate
	return nil
}

// Save validates a deep candidate, performs config's atomic 0600 publish, and
// updates the caller only if that publish succeeds.
func Save(path string, cfg *config.Config) error {
	candidate, err := Clone(cfg)
	if err != nil {
		return err
	}
	candidate.GitHub.Tokens = nonEmpty(candidate.GitHub.Tokens)
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := config.Save(path, candidate); err != nil {
		return err
	}
	*cfg = *candidate
	return nil
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
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

// RequiresConfirmationFor is value-aware. Billing applies only when the edit
// can newly activate Voyage; a harmless disable remains an ordinary edit.
func RequiresConfirmationFor(field Field, before, after *config.Config) bool {
	switch field.Consequence {
	case Reindex:
		return before.Embedder.Voyage.OutputDimension != after.Embedder.Voyage.OutputDimension
	case Billing:
		return (before.Embedder.Voyage.Disabled && !after.Embedder.Voyage.Disabled) ||
			(before.Embedder.Voyage.APIKeyFile == "" && after.Embedder.Voyage.APIKeyFile != "")
	case Hostwide:
		return true
	default:
		return false
	}
}

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
