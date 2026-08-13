package settings

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/setupcheck"
)

type unwritableActionStore struct{}

func (unwritableActionStore) Close() error { return nil }
func (unwritableActionStore) VoyageCacheWritable(context.Context) error {
	return errors.New("store is read-only")
}

func TestStoreActionChecksWritableCache(t *testing.T) {
	m := New(&config.Config{}, filepath.Join(t.TempDir(), "config.json"), nil).WithActionDeps(ActionDeps{
		StoreOpen: func() (setupcheck.Store, error) { return unwritableActionStore{}, nil },
	})
	_, err := runActionWithDeps(ActionStoreCheck, &m, m.deps)
	if err == nil {
		t.Fatal("store action accepted an unwritable cache")
	}
}
