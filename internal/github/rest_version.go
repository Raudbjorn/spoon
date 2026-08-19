package github

import (
	"net/http"
)

// defaultRESTVersion is the pinned GitHub REST API version sent on every outbound
// REST request. This freezes the wire contract so the response schema is stable
// and predictable across runs. 2022-11-28 is the current effective default.
const defaultRESTVersion = "2022-11-28"

// restVersionHeader is the HTTP header name for the API version.
const restVersionHeader = "X-GitHub-Api-Version"

// versionInjectingTransport wraps base and injects the pinned REST version header
// on every outbound request. go-gh does not set this header by default, so we
// add it here to make the wire contract explicit and testable.
type versionInjectingTransport struct {
	base        http.RoundTripper
	apiVersion  string
}

func newVersionInjectingTransport(base http.RoundTripper, apiVersion string) *versionInjectingTransport {
	return &versionInjectingTransport{base: base, apiVersion: apiVersion}
}
func (t *versionInjectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(restVersionHeader, t.apiVersion)
	return t.base.RoundTrip(req)
}
