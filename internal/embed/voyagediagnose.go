package embed

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/svnbjrn/spoon/internal/config"
)

// VoyageKeyDiagnosis explains how -- or whether -- a Voyage credential resolved.
//
// It exists because the resolution path is deliberately forgiving in three
// different places, and all three failures were reported to the user with the
// same four words. An unset path, a path naming a file that is not there, and a
// host that simply has no key each yield ("", nil): "not configured" was
// therefore indistinguishable from "configured, but pointing at nothing". That
// is the difference between "set this up" and "you have a typo", and the user
// cannot act without knowing which they have.
type VoyageKeyDiagnosis struct {
	// HasKey reports whether a credential resolved at all.
	HasKey bool
	// Source names where it came from, or would have: an environment variable
	// name or "embedder.voyage.apiKeyFile". Empty when nothing is configured.
	Source string
	// Path is the credential file consulted, if any. Kept separate from Source
	// so callers can render it distinctly.
	Path string
	// Summary is one line fit to show a user. It never contains key material.
	//
	// It is deliberately a phrase rather than a sentence beginning "Voyage ...",
	// so a caller can compose it after its own verdict without the two stuttering.
	Summary string
	// Label is the shortest honest description of where the credential came
	// from. Callers pair it with an error rather than the full Summary, which
	// for permission failures is the error text and would otherwise print twice.
	Label string
	// Err is a hard configuration error, such as loose file permissions. These
	// are worth surfacing even though the resolver treats most of this path as
	// best-effort.
	Err error
}

// DiagnoseVoyageKey resolves the Voyage credential the same way the embedder
// does, but reports how it got there instead of only what it got. It performs no
// network I/O and does not open a store.
func DiagnoseVoyageKey(effective config.EffectiveConfig, disable bool, env map[string]string) VoyageKeyDiagnosis {
	disabled, err := strconv.ParseBool(effective.Voyage.Disabled.Value)
	if err != nil {
		return VoyageKeyDiagnosis{
			Summary: fmt.Sprintf("embedder.voyage.disabled is %q, which is not a boolean", effective.Voyage.Disabled.Value),
			Err:     fmt.Errorf("embedder.voyage.disabled: %w", err),
		}
	}
	if disable || disabled {
		why := "embedder.voyage.disabled"
		if effective.Voyage.Disabled.Environment != "" {
			why = effective.Voyage.Disabled.Environment
		} else if disable {
			why = "--no-voyage"
		}
		return VoyageKeyDiagnosis{Summary: "disabled by " + why, Label: "disabled by " + why}
	}

	path := strings.TrimSpace(effective.Voyage.APIKeyFile.Value)

	// The environment wins over the file, so report it first when it is what
	// will actually be used.
	for _, name := range []string{VoyageAPIKeyEnv, VoyageAPIKeyEnvAlt} {
		if strings.TrimSpace(env[name]) != "" {
			return VoyageKeyDiagnosis{
				HasKey: true, Source: name, Path: path,
				Summary: "key resolved from " + name,
				Label:   "key from " + name,
			}
		}
	}

	if path == "" {
		return VoyageKeyDiagnosis{
			Summary: fmt.Sprintf(
				"no key: set embedder.voyage.apiKeyFile to a 0600 file, or export %s", VoyageAPIKeyEnv),
			Label: "no key configured",
		}
	}

	// Distinguish the states config.ReadCredentialFile deliberately collapses
	// into ("", nil). A missing file is not an error there -- a stale config
	// entry should not fail every command -- but it is exactly what the user
	// needs told.
	fromFile := VoyageKeyDiagnosis{Source: keyFileSource, Path: path, Label: "key from " + keyFileSource}

	switch info, statErr := os.Stat(path); {
	case errors.Is(statErr, os.ErrNotExist):
		fromFile.Label = keyFileSource + " names a missing file"
		fromFile.Summary = fmt.Sprintf("embedder.voyage.apiKeyFile is %s, which does not exist", path)
		return fromFile
	case statErr != nil:
		fromFile.Err = statErr
		fromFile.Summary = fmt.Sprintf("embedder.voyage.apiKeyFile %s cannot be read: %v", path, statErr)
		return fromFile
	case !info.Mode().IsRegular():
		fromFile.Summary = fmt.Sprintf("embedder.voyage.apiKeyFile %s is not a regular file", path)
		return fromFile
	}

	key, err := config.ReadCredentialFile(path, keyFileSource)
	if err != nil {
		// The permission check lives in ReadCredentialFile and its message
		// already names the chmod to run, so it is passed through rather than
		// reworded. Callers pair Label with Err instead of Summary here, or the
		// same sentence prints twice.
		fromFile.Err = err
		fromFile.Summary = err.Error()
		return fromFile
	}
	if key == "" {
		fromFile.Summary = fmt.Sprintf("embedder.voyage.apiKeyFile %s is empty", path)
		return fromFile
	}
	fromFile.HasKey = true
	fromFile.Summary = fmt.Sprintf("key resolved from embedder.voyage.apiKeyFile %s", path)
	return fromFile
}

const keyFileSource = "embedder.voyage.apiKeyFile"
