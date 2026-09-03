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

// diffAcceptHeader requests GitHub's unified-diff compare representation
// instead of JSON. Unlike the JSON compare response, it is not subject to
// forge.CompareFilesCap -- see FetchCompareDiff in compare_diff.go, the only
// consumer of the REST client this header builds.
const diffAcceptHeader = "application/vnd.github.v3.diff"

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

// diffClientOptions returns opts with Headers replaced by a single Accept:
// diffAcceptHeader entry, for building the RestDiff sibling of a Rest client
// beside it (same AuthToken, Host, Transport, Timeout -- same identity and
// budget, different representation). None of the three backend construction
// sites in client.go set Headers on the options they pass in here, so there
// is nothing to preserve; if that ever changes, this must start merging
// instead of replacing.
func diffClientOptions(opts ghAPI.ClientOptions) ghAPI.ClientOptions {
	opts.Headers = map[string]string{"Accept": diffAcceptHeader}
	return opts
}
