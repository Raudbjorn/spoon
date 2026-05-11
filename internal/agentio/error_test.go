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
	rem, ok := e["remediation"].(string)
	if !ok {
		t.Fatalf("remediation not a string: %T", e["remediation"])
	}
	if !strings.Contains(rem, "spn threads list --help") {
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
	if err := json.Unmarshal(b.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	e := env["error"]
	if e["retryable"] != true {
		t.Errorf("rate_limited should be retryable")
	}
	ra, ok := e["retry_after_seconds"].(float64)
	if !ok {
		t.Fatalf("retry_after_seconds not a number: %T", e["retry_after_seconds"])
	}
	if ra != 60 {
		t.Errorf("retry_after_seconds=%v", e["retry_after_seconds"])
	}
}

func TestError_WithDetails(t *testing.T) {
	var b bytes.Buffer
	NewError(CodeNotFound, "thread missing", "verify ID").
		WithDetails(map[string]any{"thread_id": "PRRT_xyz"}).
		Emit(&b)
	var env map[string]map[string]any
	if err := json.Unmarshal(b.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d, ok := env["error"]["details"].(map[string]any)
	if !ok {
		t.Fatalf("details not a map: %T", env["error"]["details"])
	}
	if d["thread_id"] != "PRRT_xyz" {
		t.Errorf("details.thread_id=%v", d["thread_id"])
	}
}

func TestError_Emit_upstreamError_retryable(t *testing.T) {
	var b bytes.Buffer
	exit := NewError(CodeUpstream, "graphql failed", "retry shortly").Emit(&b)
	if exit != 1 {
		t.Errorf("upstream_error exits 1, got %d", exit)
	}
	var env map[string]map[string]any
	if err := json.Unmarshal(b.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env["error"]["retryable"] != true {
		t.Errorf("upstream_error should default to retryable=true")
	}
}

func TestRemediation_authRequired(t *testing.T) {
	r := RemediationAuthRequired()
	if !strings.Contains(r, "gh auth login") {
		t.Errorf("RemediationAuthRequired should mention gh auth login: %q", r)
	}
}

func TestRemediation_policyBodyRequired(t *testing.T) {
	r := RemediationPolicyBodyRequired("owner/repo#42", "PRRT_xyz")
	if !strings.Contains(r, "owner/repo#42") || !strings.Contains(r, "PRRT_xyz") {
		t.Errorf("placeholders not substituted: %q", r)
	}
}

func TestRemediation_authScope(t *testing.T) {
	r := RemediationAuthScope("repo")
	if !strings.Contains(r, "gh auth refresh -s repo") {
		t.Errorf("scope placeholder not substituted: %q", r)
	}
}

func TestRemediation_badInput_noArgs(t *testing.T) {
	r := RemediationBadInput("", "")
	if !strings.Contains(r, "spn --help") {
		t.Errorf("expected top-level help, got %q", r)
	}
}

func TestRemediation_badInput_nounOnly(t *testing.T) {
	r := RemediationBadInput("threads", "")
	if !strings.Contains(r, "spn threads --help") {
		t.Errorf("expected noun-level help, got %q", r)
	}
}

func TestRemediation_badInput_nounVerb(t *testing.T) {
	r := RemediationBadInput("threads", "resolve")
	if !strings.Contains(r, "spn threads resolve --help") {
		t.Errorf("expected verb-level help, got %q", r)
	}
}

func TestRemediation_rateLimited(t *testing.T) {
	r := RemediationRateLimited("2026-05-10T12:00:00Z", 30)
	if !strings.Contains(r, "2026-05-10T12:00:00Z") || !strings.Contains(r, "30s") {
		t.Errorf("placeholders not substituted: %q", r)
	}
}
