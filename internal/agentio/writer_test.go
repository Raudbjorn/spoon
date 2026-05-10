package agentio

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteJSON_object(t *testing.T) {
	var b bytes.Buffer
	if err := WriteJSON(&b, map[string]int{"n": 1}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	got := b.String()
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("expected trailing newline, got %q", got)
	}
	if !strings.Contains(got, `"n": 1`) && !strings.Contains(got, `"n":1`) {
		t.Errorf("missing key, got %q", got)
	}
}

func TestWriteNDJSON_multipleObjects(t *testing.T) {
	var b bytes.Buffer
	_ = WriteNDJSON(&b, map[string]string{"id": "a"})
	_ = WriteNDJSON(&b, map[string]string{"id": "b"})
	lines := strings.Split(strings.TrimSuffix(b.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), b.String())
	}
}

func TestWriteNull(t *testing.T) {
	var b bytes.Buffer
	if err := WriteNull(&b); err != nil {
		t.Fatalf("WriteNull: %v", err)
	}
	if b.String() != "null\n" {
		t.Errorf("want %q, got %q", "null\n", b.String())
	}
}
