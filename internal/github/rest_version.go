package github

import (
	"fmt"
	"net/http"
	"strings"

	ghauth "github.com/cli/go-gh/v2/pkg/auth"
	gogithub "github.com/google/go-github/v90/github"
)

// defaultRESTVersion is the wire value sent to the GitHub REST API.
const defaultRESTVersion = "2022-11-28"

// StoredAPIVersion is what is persisted to the database. It is prefixed with
// "github/" to prevent SQLite/go-libsql date affinity from coercing
// "2022-11-28" into "2022-11-28T00:00:00Z" at insert time.
const StoredAPIVersion = "github/2022-11-28"

// restVersionHeader is the HTTP header name for the API version.
const restVersionHeader = "X-GitHub-Api-Version"

// diffAcceptHeader requests GitHub's unified-diff compare representation
// instead of JSON. Unlike the JSON compare response, it is not subject to
// forge.CompareFilesCap -- see FetchCompareDiff in compare_diff.go, the only
// consumer of the per-request Accept override built from it.
const diffAcceptHeader = "application/vnd.github.v3.diff"

// restUserAgent identifies spoon to GitHub; the API rejects requests without one.
const restUserAgent = "spoon"

// newRESTClient builds the go-github client for one GitHub identity. The pinned
// API version is sent on every request (go-github defaults to the same value;
// doGetSelect also passes it explicitly so a library bump cannot move it). An
// empty token builds an anonymous client.
//
// Rate-limit pacing is owned by the backend pool and limiter, so go-github's own
// pre-flight check is disabled: it would short-circuit a request the pool wants
// to route to a different identity. Redirects to another host are refused, since
// go-github attaches the Authorization header at the transport and would
// otherwise replay it on every hop.
func newRESTClient(token string, transport http.RoundTripper) (*gogithub.Client, error) {
	return newRESTClientForHost(defaultHost, token, transport)
}

// newRESTClientForHost is newRESTClient bound to one GitHub host, so a token
// issued for an enterprise installation is never sent to api.github.com.
func newRESTClientForHost(host, token string, transport http.RoundTripper) (*gogithub.Client, error) {
	httpClient := &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRESTRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRESTRedirects)
			}
			// Same host is not enough: an https->http hop keeps Authorization
			// on the same host and would send the credential in plaintext.
			if req.URL.Host != via[0].URL.Host || req.URL.Scheme != via[0].URL.Scheme {
				return fmt.Errorf("refusing redirect from %s://%s to %s://%s", via[0].URL.Scheme, via[0].URL.Host, req.URL.Scheme, req.URL.Host)
			}
			return nil
		},
	}
	opts := []gogithub.ClientOptionsFunc{
		gogithub.WithHTTPClient(httpClient),
		gogithub.WithUserAgent(restUserAgent),
		gogithub.WithDisableRateLimitCheck(),
	}
	if transport != nil {
		opts = append(opts, gogithub.WithTransport(transport))
	}
	if base := restBaseURL(host); base != "" {
		opts = append(opts, gogithub.WithEnterpriseURLs(base, base))
	}
	if token != "" {
		opts = append(opts, gogithub.WithAuthToken(token))
	}
	return gogithub.NewClient(opts...)
}

// restBaseURL returns the REST root for host, or "" for github.com, whose
// root is go-github's default. It mirrors newGraphQLClient's host mapping.
func restBaseURL(host string) string {
	if strings.EqualFold(host, "garage.github.com") {
		return "https://garage.github.com/api/v3/"
	}
	host = ghauth.NormalizeHostname(host)
	switch {
	case host == "" || host == defaultHost:
		return ""
	case ghauth.IsEnterprise(host):
		return "https://" + host + "/api/v3/"
	case host == "github.localhost":
		return "http://api.github.localhost/"
	}
	return "https://api." + host + "/"
}

// maxRESTRedirects bounds redirect chains on REST requests (net/http's default).
const maxRESTRedirects = 10

// restRequestOptions returns the per-request options every REST call carries:
// the pinned API version, plus an Accept override when accept is non-empty.
func restRequestOptions(accept string) []gogithub.RequestOption {
	opts := []gogithub.RequestOption{gogithub.WithVersion(defaultRESTVersion)}
	if accept != "" {
		opts = append(opts, func(req *http.Request) { req.Header.Set("Accept", accept) })
	}
	return opts
}
