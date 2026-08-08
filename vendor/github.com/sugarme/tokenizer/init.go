package tokenizer

import (
	"fmt"
	"os"
	"path/filepath"
)

var (
	CachedDir       string = "NOT_SETTING"
	tokenizerEnvKey string = "GO_TOKENIZER"
)

func init() {
	// default path: {$HOME}/.cache/tokenizer
	homeDir := os.Getenv("HOME")
	CachedDir = fmt.Sprintf("%s/.cache/tokenizer", homeDir)

	initEnv()

	// Patched for spoon: the upstream log.Printf("INFO: CachedDir=…") line is
	// removed — it pollutes stderr on every invocation, and spn's stderr is a
	// structured-output channel.
}

func initEnv() {
	val := os.Getenv(tokenizerEnvKey)
	if val != "" {
		CachedDir = val
	}

	if _, err := os.Stat(CachedDir); os.IsNotExist(err) {
		if err := os.MkdirAll(CachedDir, 0755); err != nil {
			// Patched for spoon: upstream log.Fatal here kills the whole
			// process at import time on hosts without a writable home
			// (system accounts, containers). Degrade to a temp dir instead —
			// the cache is a convenience, not a correctness requirement.
			CachedDir = filepath.Join(os.TempDir(), "tokenizer")
			_ = os.MkdirAll(CachedDir, 0755)
		}
	}
}
