package agentio

import (
	"encoding/json"
	"io"
)

// WriteJSON marshals v as JSON with 2-space indent and a trailing newline.
// Use this for single-value success output on stdout.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// WriteNDJSON marshals v as a single-line JSON object followed by '\n'.
// Use this for streaming output where each line is a complete value.
func WriteNDJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	return enc.Encode(v)
}

// WriteNull writes the literal "null\n". Use for spn threads next when no
// thread remains.
func WriteNull(w io.Writer) error {
	_, err := io.WriteString(w, "null\n")
	return err
}
