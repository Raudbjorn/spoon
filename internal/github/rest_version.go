package github

import ghAPI "github.com/cli/go-gh/v2/pkg/api"

// defaultRESTVersion is the wire value sent to the GitHub REST API.
const defaultRESTVersion = "2022-11-28"

// StoredAPIVersion is what is persisted to the database. It is prefixed with
// "github/" to prevent SQLite/go-libsql date affinity from coercing
// "2022-11-28" into "2022-11-28T00:00:00Z" at insert time.
const StoredAPIVersion = "github/2022-11-28"

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
