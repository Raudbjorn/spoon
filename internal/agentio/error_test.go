package agentio

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestError_Emit_basic(t *testing.T) {
	var b bytes.Buffer
	exit := NewError(CodeBadInput, "missing PR ref", "Run spn threads list --help").Emit(&b)
	if exit != 2 {
		t.Errorf("bad_input should exit 2, got %d", exit)
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(b.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON on stderr: %v\n%s", err, b.String())
	}
	e := env["error"]
	if e["code"] != "bad_input" {
		t.Errorf("code=%v", e["code"])
	}
	if e["retryable"] != false {
		t.Errorf("retryable should default to false for bad_input")
	}
	if !strings.Contains(e["remediation"].(string), "spn threads list --help") {
		t.Errorf("remediation lost: %v", e["remediation"])
	}
}

func TestError_Emit_rateLimited_retryable(t *testing.T) {
	var b bytes.Buffer
	exit := NewError(CodeRateLimited, "limit hit", "wait").WithRetryAfter(60).Emit(&b)
	if exit != 1 {
		t.Errorf("rate_limited exits 1, got %d", exit)
	}
	var env map[string]map[string]any
	_ = json.Unmarshal(b.Bytes(), &env)
	e := env["error"]
	if e["retryable"] != true {
		t.Errorf("rate_limited should be retryable")
	}
	if e["retry_after_seconds"].(float64) != 60 {
		t.Errorf("retry_after_seconds=%v", e["retry_after_seconds"])
	}
}

func TestError_WithDetails(t *testing.T) {
	var b bytes.Buffer
	NewError(CodeNotFound, "thread missing", "verify ID").
		WithDetails(map[string]any{"thread_id": "PRRT_xyz"}).
		Emit(&b)
	var env map[string]map[string]any
	_ = json.Unmarshal(b.Bytes(), &env)
	d := env["error"]["details"].(map[string]any)
	if d["thread_id"] != "PRRT_xyz" {
		t.Errorf("details.thread_id=%v", d["thread_id"])
	}
}
