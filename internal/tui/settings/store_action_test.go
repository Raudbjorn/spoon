package settings

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
)

func TestStoreActionChecksWritableCache(t *testing.T) {
	_, err := runAction(ActionStoreCheck, &config.Config{}, filepath.Join(t.TempDir(), "config.json"), writableCache{err: errors.New("store is read-only")})
	if err == nil {
		t.Fatal("store action accepted an unwritable cache")
	}
}
