package config

import "testing"

func TestResolveStringPrecedencePreservesExplicitFalse(t *testing.T) {
	result := ResolveString("false", true, "true", "override", map[string]string{"SPOON_X": "env"}, "SPOON_X")
	if result.Value != "override" || result.Source != SourceFlag {
		t.Fatalf("flag result = %#v", result)
	}
	result = ResolveString("false", true, "true", "", map[string]string{}, "SPOON_X")
	if result.Value != "false" || result.Source != SourceFile {
		t.Fatalf("file result = %#v", result)
	}
}
