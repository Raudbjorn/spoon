package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	gogithub "github.com/google/go-github/v90/github"
)

func TestGraphQLClientHeaders(t *testing.T) {
	tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://api.github.com/graphql" || req.Method != http.MethodPost {
			t.Errorf("request = %s %s", req.Method, req.URL)
		}
		for name, want := range map[string]string{
			"Authorization": "Bearer test-token",
			"User-Agent":    restUserAgent,
			"Accept":        "application/vnd.github+json",
			"Content-Type":  "application/json",
		} {
			if got := req.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		if got := req.Header.Get(restVersionHeader); got != "" {
			t.Errorf("GraphQL must not send the REST version header, got %q", got)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{"viewer":{"login":"octo"}}}`)), Request: req}, nil
	})
	var query struct{ Viewer struct{ Login string } }
	if err := queryGraphQL(context.Background(), newGraphQLClient("test-token", defaultHost, tr), &query, nil, &query); err != nil {
		t.Fatal(err)
	}
	if query.Viewer.Login != "octo" {
		t.Errorf("viewer = %q, want octo", query.Viewer.Login)
	}
}

func TestGraphQLClientRefusesRedirects(t *testing.T) {
	for _, target := range []string{"https://other.example/graphql", "http://api.github.com/graphql", "https://api.github.com/elsewhere"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 {
					t.Errorf("redirect target received a request with Authorization=%t", req.Header.Get("Authorization") != "")
					return nil, errors.New("redirect followed")
				}
				return &http.Response{
					StatusCode: http.StatusTemporaryRedirect,
					Header:     http.Header{"Location": []string{target}},
					Body:       io.NopCloser(strings.NewReader("redirect")),
					Request:    req,
				}, nil
			})
			var query struct{ Viewer struct{ Login string } }
			err := queryGraphQL(context.Background(), newGraphQLClient("secret", defaultHost, tr), &query, nil, &query)
			status, headers, ok := httpFailure(err)
			if calls != 1 || !ok || status != http.StatusTemporaryRedirect || headers.Get("Location") != target {
				t.Fatalf("calls=%d status=%d headers=%v err=%v", calls, status, headers, err)
			}
		})
	}
}

func TestGraphQLClientCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		cancel()
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	var query struct{ Viewer struct{ Login string } }
	err := queryGraphQL(ctx, newGraphQLClient("test-token", defaultHost, tr), &query, nil, &query)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestGraphQLClientHost(t *testing.T) {
	for host, endpoint := range map[string]string{
		"github.com":        "https://api.github.com/graphql",
		"API.GITHUB.COM":    "https://api.github.com/graphql",
		"git.example.com":   "https://git.example.com/api/graphql",
		"tenant.ghe.com":    "https://api.tenant.ghe.com/graphql",
		"github.localhost":  "http://api.github.localhost/graphql",
		"garage.github.com": "https://garage.github.com/api/graphql",
	} {
		t.Run(host, func(t *testing.T) {
			tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != endpoint {
					t.Errorf("endpoint = %s, want %s", req.URL, endpoint)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{"viewer":{"login":"alice"}}}`)), Request: req}, nil
			})
			var query struct{ Viewer struct{ Login string } }
			if err := queryGraphQL(context.Background(), newGraphQLClient("test-token", host, tr), &query, nil, &query); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGraphQLClientPreservesHTTPError(t *testing.T) {
	tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Retry-After": []string{"12"}, "X-Github-Request-Id": []string{"test-request"}},
			Body:       io.NopCloser(strings.NewReader(`{"message":"temporarily unavailable"}`)),
			Request:    req,
		}, nil
	})
	var query struct{ Viewer struct{ Login string } }
	err := queryGraphQL(context.Background(), newGraphQLClient("test-token", defaultHost, tr), &query, nil, &query)
	var responseErr *gogithub.ErrorResponse
	if !errors.As(err, &responseErr) {
		t.Fatalf("error = %T %v, want *github.ErrorResponse", err, err)
	}
	status, headers, ok := httpFailure(err)
	if !ok || status != http.StatusServiceUnavailable || headers.Get("Retry-After") != "12" || headers.Get("X-GitHub-Request-Id") != "test-request" {
		t.Errorf("status=%d headers=%v ok=%v", status, headers, ok)
	}
	if responseErr.Message != `{"message":"temporarily unavailable"}` {
		t.Errorf("error body lost: %q", responseErr.Message)
	}
}

func TestGraphQLClientRejectsMalformedResponses(t *testing.T) {
	for _, body := range []string{
		`{"data":`,
		`{"data":{"viewer":{"login":123}}}`,
		`{"data":{"viewer":{"unexpected":"value"}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			tr := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})
			var query struct{ Viewer struct{ Login string } }
			if err := queryGraphQL(context.Background(), newGraphQLClient("test-token", defaultHost, tr), &query, nil, &query); err == nil {
				t.Fatal("malformed response accepted")
			}
		})
	}
}
