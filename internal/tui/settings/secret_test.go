package settings

import "testing"

func TestSecretInputNeverRendersValueOrAllowsYank(t *testing.T) {
	const secret = "task-nine-secret-sentinel"
	input := NewSecretInput()
	input.Set(secret)
	if got := input.Render(); got == "" || contains(got, secret) {
		t.Fatalf("secret leaked in %q", got)
	}
	if input.Yank() == nil {
		t.Fatal("secret yank should be refused")
	}
	input.Clear()
	if input.Value() != "" || input.Render() != "" {
		t.Fatalf("clear did not remove secret")
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || len(s) > len(sub) && (s[:len(sub)] == sub || s[len(s)-len(sub):] == sub))
}
