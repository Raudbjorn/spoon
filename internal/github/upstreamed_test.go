package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// prResp builds a single associated-PR JSON object for the fake endpoint.
func prResp(number int, state, mergedAt, baseFullName string) map[string]any {
	var merged any // null unless provided
	if mergedAt != "" {
		merged = mergedAt
	}
	return map[string]any{
		"number":    number,
		"state":     state,
		"merged_at": merged,
		"base": map[string]any{
			"repo": map[string]any{"full_name": baseFullName},
		},
	}
}

func TestCheckUpstreamed(t *testing.T) {
	const upstream = "openvinotoolkit/openvino"

	tests := []struct {
		name    string
		body    []map[string]any
		status  int
		wantUp  bool
		wantPR  int
		wantErr bool
	}{
		{
			name:   "merged into upstream → upstreamed",
			body:   []map[string]any{prResp(19634, "closed", "2023-09-22T05:33:33Z", upstream)},
			wantUp: true, wantPR: 19634,
		},
		{
			name:   "closed but never merged → not upstreamed",
			body:   []map[string]any{prResp(20000, "closed", "", upstream)},
			wantUp: false,
		},
		{
			name:   "open PR → not upstreamed",
			body:   []map[string]any{prResp(21000, "open", "", upstream)},
			wantUp: false,
		},
		{
			name:   "merged into the fork itself, not upstream → not upstreamed",
			body:   []map[string]any{prResp(7, "closed", "2023-01-01T00:00:00Z", "someuser/openvino")},
			wantUp: false,
		},
		{
			name:   "no associated PRs → not upstreamed",
			body:   []map[string]any{},
			wantUp: false,
		},
		{
			name: "first PR unmerged, second merged upstream → upstreamed",
			body: []map[string]any{
				prResp(100, "closed", "", upstream),
				prResp(101, "closed", "2024-02-02T00:00:00Z", upstream),
			},
			wantUp: true, wantPR: 101,
		},
		{
			name:   "404 (deleted/private fork) → unknown, no error",
			status: http.StatusNotFound,
			wantUp: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, "/commits/") || !strings.HasSuffix(r.URL.Path, "/pulls") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if tc.status != 0 {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"message":"Not Found"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(tc.body)
			}))
			defer srv.Close()

			c := newTestClient(t, srv)
			got, err := c.CheckUpstreamed(context.Background(), "someuser", "openvino", "deadbeef", upstream)
			if tc.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got.Upstreamed != tc.wantUp {
				t.Errorf("Upstreamed = %v, want %v", got.Upstreamed, tc.wantUp)
			}
			if got.PRNumber != tc.wantPR {
				t.Errorf("PRNumber = %d, want %d", got.PRNumber, tc.wantPR)
			}
		})
	}
}

func TestCheckUpstreamed_EmptySHA_NoCall(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	got, err := c.CheckUpstreamed(context.Background(), "o", "r", "", "up/stream")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Upstreamed {
		t.Errorf("empty sha should be not-upstreamed")
	}
	if called {
		t.Errorf("empty sha should short-circuit without an API call")
	}
}
