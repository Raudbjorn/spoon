package github

import (
	"errors"
	"fmt"
	"testing"

	ghAPI "github.com/cli/go-gh/v2/pkg/api"
)

func TestIsTransientServerError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"typed 502", &ghAPI.HTTPError{StatusCode: 502}, true},
		{"typed 503", &ghAPI.HTTPError{StatusCode: 503}, true},
		{"typed 504", &ghAPI.HTTPError{StatusCode: 504}, true},
		{"typed 500 not retried", &ghAPI.HTTPError{StatusCode: 500}, false},
		{"typed 404 not retried", &ghAPI.HTTPError{StatusCode: 404}, false},
		{"typed 403 not retried", &ghAPI.HTTPError{StatusCode: 403}, false},
		{"wrapped 502", fmt.Errorf("GraphQL query: %w", &ghAPI.HTTPError{StatusCode: 502}), true},
		{"string 502", errors.New("HTTP 502: 502 Bad Gateway"), true},
		{"string 504", errors.New("HTTP 504: Gateway Timeout"), true},
		{"plain error", errors.New("connection refused"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientServerError(tt.err); got != tt.want {
				t.Errorf("isTransientServerError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
