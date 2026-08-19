package github

import ghAPI "github.com/cli/go-gh/v2/pkg/api"

// defaultRESTVersion is the pinned GitHub REST API version sent on every outbound
// REST request. This freezes the wire contract so the response schema is stable
// and predictable across runs. 2022-11-28 is the current effective default.
const defaultRESTVersion = "2022-11-28"

// restVersionHeader is the HTTP header name for the API version.
const restVersionHeader = "X-GitHub-Api-Version"

// newVersionedRESTClient builds a ghAPI.RESTClient with the pinned API version
// header set, without mutating the caller's ghAPI.ClientOptions.Headers map.
// If the caller already has a Headers map, entries are preserved. The version
// header is added or overwritten. The caller's Transport, AuthToken, Host, and
// Timeout are preserved as supplied.
func newVersionedRESTClient(opts ghAPI.ClientOptions) (*ghAPI.RESTClient, error) {
	headers := make(map[string]string, len(opts.Headers)+1)
	for k, v := range opts.Headers {
		headers[k] = v
	}
	headers[restVersionHeader] = defaultRESTVersion
	opts.Headers = headers
	return ghAPI.NewRESTClient(opts)
}
