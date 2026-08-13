package keymap

import "testing"

func TestRegistryHasNoDuplicateKeysWithinScope(t *testing.T) {
	seen := map[Scope]map[string]Action{}
	for _, binding := range Registry {
		if seen[binding.Scope] == nil {
			seen[binding.Scope] = map[string]Action{}
		}
		for _, key := range binding.Keys {
			if previous, ok := seen[binding.Scope][key]; ok {
				t.Fatalf("scope %q binds %q to both %q and %q", binding.Scope, key, previous, binding.Action)
			}
			seen[binding.Scope][key] = binding.Action
		}
	}
}

func TestLookupRetainsD1Moves(t *testing.T) {
	for _, test := range []struct {
		scope Scope
		key   string
		want  Action
	}{
		{MainTable, "t", ToggleTheme},
		{MainTable, "c", CycleTier},
		{MainTable, "d", OpenCompare},
		{MainDetail, "c", CycleTier},
		{MainDetail, "d", OpenCompare},
	} {
		if got, ok := Lookup(test.scope, test.key); !ok || got != test.want {
			t.Errorf("Lookup(%q, %q) = %q, %v; want %q, true", test.scope, test.key, got, ok, test.want)
		}
	}
}

func TestEveryBindingDispatches(t *testing.T) {
	for _, binding := range Registry {
		for _, key := range binding.Keys {
			if got := Dispatch(binding.Scope, key); got != binding.Action {
				t.Errorf("Dispatch(%q, %q) = %q, want %q", binding.Scope, key, got, binding.Action)
			}
		}
	}
}
