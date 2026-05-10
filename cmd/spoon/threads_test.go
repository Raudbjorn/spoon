package main

import (
	"testing"
)

func TestParseThreadsFlags(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantErr bool
		check   func(t *testing.T, f threadsFlags)
	}{
		{
			name: "default mode (TUI)",
			args: []string{"owner/repo#42"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeTUI {
					t.Errorf("mode=%v want TUI", f.mode)
				}
			},
		},
		{
			name: "json",
			args: []string{"owner/repo#42", "--json"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeJSON {
					t.Errorf("mode=%v want JSON", f.mode)
				}
			},
		},
		{
			name: "next",
			args: []string{"owner/repo#42", "--next"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeNext {
					t.Errorf("mode=%v want Next", f.mode)
				}
			},
		},
		{
			name: "reply with body",
			args: []string{"owner/repo#42", "--reply", "PRRT_1", "--body", "hello"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeReply || f.targetID != "PRRT_1" || f.body != "hello" {
					t.Errorf("got %+v", f)
				}
			},
		},
		{
			name: "resolve",
			args: []string{"owner/repo#42", "--resolve", "PRRT_1", "--body", "fixed"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeResolve || f.targetID != "PRRT_1" || f.body != "fixed" {
					t.Errorf("got %+v", f)
				}
			},
		},
		{
			name: "resolve-all",
			args: []string{"owner/repo#42", "--resolve-all"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeResolveAll {
					t.Errorf("mode=%v", f.mode)
				}
			},
		},
		{
			name:    "json + next mutually exclusive",
			args:    []string{"owner/repo#42", "--json", "--next"},
			wantErr: true,
		},
		{
			name:    "reply needs target id",
			args:    []string{"owner/repo#42", "--reply"},
			wantErr: true,
		},
		{
			name:    "reply needs body",
			args:    []string{"owner/repo#42", "--reply", "PRRT_1"},
			wantErr: true,
		},
		{
			name:    "missing pr ref",
			args:    []string{"--json"},
			wantErr: true,
		},
		{
			name: "resolve without body (parser accepts; policy applies in dispatcher)",
			args: []string{"owner/repo#42", "--resolve", "PRRT_1"},
			check: func(t *testing.T, f threadsFlags) {
				if f.mode != modeResolve || f.targetID != "PRRT_1" || f.body != "" {
					t.Errorf("got %+v", f)
				}
			},
		},
		{
			name: "no-status sets the flag",
			args: []string{"owner/repo#42", "--no-status"},
			check: func(t *testing.T, f threadsFlags) {
				if !f.noStatus {
					t.Errorf("noStatus should be true")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseThreadsFlags(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			tc.check(t, f)
		})
	}
}


func TestParsePRRef(t *testing.T) {
	cases := []struct {
		in            string
		fallbackOwner string
		fallbackRepo  string
		wantOwner     string
		wantRepo      string
		wantNumber    int
		wantErr       bool
	}{
		{"owner/repo#42", "", "", "owner", "repo", 42, false},
		{"https://github.com/owner/repo/pull/42", "", "", "owner", "repo", 42, false},
		{"#42", "fallO", "fallR", "fallO", "fallR", 42, false},
		{"#42", "", "", "", "", 0, true}, // no fallback
		{"owner/repo", "", "", "", "", 0, true},
		{"https://gitlab.com/g/r/-/merge_requests/1", "", "", "", "", 0, true}, // gitlab rejected
		{"owner/repo#abc", "", "", "", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			o, r, n, err := parsePRRef(tc.in, tc.fallbackOwner, tc.fallbackRepo)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err mismatch: got %v, wantErr=%v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if o != tc.wantOwner || r != tc.wantRepo || n != tc.wantNumber {
				t.Errorf("got (%q,%q,%d), want (%q,%q,%d)", o, r, n, tc.wantOwner, tc.wantRepo, tc.wantNumber)
			}
		})
	}
}

