package main

import "testing"

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
