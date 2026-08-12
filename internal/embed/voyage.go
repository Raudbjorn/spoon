package embed

// voyage.go holds the shared HTTP transport for Voyage AI's embedding and
// reranking endpoints. It is the only part of internal/embed that talks to a
// network service; every other embedder in this package runs in-process.
//
// The API key is held in an unexported field and only ever set as a header.
// Nothing in this file interpolates it into an error, a log line, or a URL, and
// scrubSecret is applied to server-supplied text as a second line of defense —
// this package has no masking helper because the convention throughout spoon is
// avoidance rather than redaction.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// VoyageDefaultBaseURL is Voyage's v1 REST root. Overridable so tests can
	// point at an httptest server and so a user behind a gateway can redirect.
	VoyageDefaultBaseURL = "https://api.voyageai.com/v1"

	// VoyageDefaultEmbedModel is the code-retrieval embedding model;
	// VoyageDefaultRerankModel is the current generalist cross-encoder.
	VoyageDefaultEmbedModel  = "voyage-code-3"
	VoyageDefaultRerankModel = "rerank-2.5"

	// VoyageDefaultDimension is voyage-code-3's default output width. The model
	// also supports 256, 512 and 2048; the value is part of the vector's
	// identity, so it appears in ModelID (see voyageembed.go).
	VoyageDefaultDimension = 1024

	// voyageTimeout bounds a single request. Matches the GitHub client's
	// per-request budget (internal/github/client.go) — an embedding batch of a
	// few hundred documents is comfortably inside it.
	voyageTimeout = 60 * time.Second

	// voyageMaxRetries bounds retries of a single request on 429/5xx. Total
	// wall time is additionally capped by voyageMaxRetryWait per sleep, so a
	// long Retry-After cannot stall a run indefinitely — it aborts instead and
	// the caller degrades.
	voyageMaxRetries   = 3
	voyageRetryBase    = time.Second
	voyageMaxRetryWait = 30 * time.Second

	// voyageMaxErrBody bounds how much of an error response we read, matching
	// the bounded-read convention used by the forge clients.
	voyageMaxErrBody = 4 << 10
)

// voyageDimensions are the output widths voyage-code-3 and the voyage-3/4 lines
// accept. 0 means "unset" and resolves to VoyageDefaultDimension.
var voyageDimensions = map[int]bool{256: true, 512: true, 1024: true, 2048: true}

// VoyageConfig configures the Voyage embedder and reranker. HTTP and BaseURL
// exist so the whole surface is testable against httptest without a network or
// a key; there is no HTTP mocking library in this module.
type VoyageConfig struct {
	APIKey          string
	BaseURL         string
	EmbedModel      string
	RerankModel     string
	OutputDimension int
	HTTP            *http.Client

	// Cache, when non-nil, serves previously paid-for responses instead of
	// re-requesting them. See voyagecache.go. Nil disables caching, as does
	// SPOON_VOYAGE_NO_CACHE=1.
	Cache ResponseCache
}

// withDefaults fills unset fields and validates the rest. It is the single
// place a Voyage configuration is checked, so both constructors reject the same
// inputs identically.
func (c VoyageConfig) withDefaults() (VoyageConfig, error) {
	c.APIKey = strings.TrimSpace(c.APIKey)
	if c.APIKey == "" {
		return c, errors.New("voyage: no API key (set VOYAGE_AI_API_KEY)")
	}
	if c.BaseURL == "" {
		c.BaseURL = VoyageDefaultBaseURL
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	parsed, err := url.Parse(c.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return c, fmt.Errorf("voyage: base URL %q is not an absolute http(s) URL", c.BaseURL)
	}
	if c.EmbedModel == "" {
		c.EmbedModel = VoyageDefaultEmbedModel
	}
	if c.RerankModel == "" {
		c.RerankModel = VoyageDefaultRerankModel
	}
	if c.OutputDimension == 0 {
		c.OutputDimension = VoyageDefaultDimension
	}
	if !voyageDimensions[c.OutputDimension] {
		return c, fmt.Errorf("voyage: output dimension %d must be one of 256, 512, 1024, 2048", c.OutputDimension)
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: voyageTimeout}
	}
	// One place to honor the cache kill switch, so no construction path can
	// accidentally keep caching on when the user turned it off.
	if os.Getenv(VoyageNoCacheEnv) == "1" {
		c.Cache = nil
	}
	return c, nil
}

// VoyageErrorKind classifies a Voyage failure so callers can choose between
// failing loudly and degrading. The distinction matters: spoon fails hard only
// where the user named Voyage explicitly on that invocation, and degrades
// everywhere else.
type VoyageErrorKind int

const (
	// VoyageErrTransient covers dial errors, timeouts and 5xx — retried, then
	// surfaced. Always a degrade.
	VoyageErrTransient VoyageErrorKind = iota
	// VoyageErrAuth is 401/403: a missing, invalid or unauthorized key. The one
	// user-fixable kind.
	VoyageErrAuth
	// VoyageErrRateLimited is 429 after retries were exhausted.
	VoyageErrRateLimited
	// VoyageErrRequest is any other 4xx — a malformed request, i.e. a bug here
	// or an over-length input that truncation did not catch.
	VoyageErrRequest
)

// VoyageError is a classified Voyage API failure. Detail carries the server's
// own `detail` field when present, scrubbed of the API key.
type VoyageError struct {
	Kind   VoyageErrorKind
	Status int
	Op     string
	Detail string
	Err    error
}

func (e *VoyageError) Error() string {
	switch {
	case e.Status == 0 && e.Err != nil:
		return fmt.Sprintf("voyage %s: %v", e.Op, e.Err)
	case e.Detail != "":
		return fmt.Sprintf("voyage %s: HTTP %d: %s", e.Op, e.Status, e.Detail)
	default:
		return fmt.Sprintf("voyage %s: HTTP %d", e.Op, e.Status)
	}
}

func (e *VoyageError) Unwrap() error { return e.Err }

// IsVoyageAuthError reports whether err is a Voyage authentication failure —
// the one Voyage error class a user can fix, so the only one that justifies a
// hard exit on an explicitly-Voyage invocation.
func IsVoyageAuthError(err error) bool {
	var ve *VoyageError
	return errors.As(err, &ve) && ve.Kind == VoyageErrAuth
}

// IsVoyageRateLimited reports whether err is a 429 that survived retries.
func IsVoyageRateLimited(err error) bool {
	var ve *VoyageError
	return errors.As(err, &ve) && ve.Kind == VoyageErrRateLimited
}

// scrubSecret removes secret from s. The API key should never reach an error
// string by construction; this guarantees it even if a server echoes the
// credential back in an error body.
func scrubSecret(s, secret string) string {
	if secret == "" || !strings.Contains(s, secret) {
		return s
	}
	return strings.ReplaceAll(s, secret, "[REDACTED]")
}

type voyageClient struct{ cfg VoyageConfig }

// post sends body as JSON to path (relative to the configured base URL) and
// decodes the response into dst. 429 and 5xx are retried up to
// voyageMaxRetries; every other non-200 is classified and returned.
func (c *voyageClient) post(ctx context.Context, path, op string, body, dst any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return &VoyageError{Kind: VoyageErrRequest, Op: op, Err: fmt.Errorf("encode request: %w", err)}
	}
	endpoint := c.cfg.BaseURL + path

	var last error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		retryable, wait, told, err := c.attempt(ctx, endpoint, op, payload, dst)
		if err == nil {
			return nil
		}
		last = err
		if !retryable || attempt >= voyageMaxRetries {
			return last
		}
		if !told {
			// No usable Retry-After: back off exponentially instead of guessing.
			wait = min(voyageRetryBase*(1<<attempt), voyageMaxRetryWait)
		}
		// A Retry-After longer than the cap is not worth waiting out inside a
		// fork listing; abort and let the caller degrade with a warning.
		if wait > voyageMaxRetryWait {
			return last
		}
		if err := voyageSleep(ctx, wait); err != nil {
			return err
		}
	}
}

// attempt performs one request. It reports whether the failure is worth retrying
// and, when the server said so via Retry-After, how long to wait — told
// distinguishes "the server asked for zero delay" from "the server said nothing",
// which the caller answers with exponential backoff.
func (c *voyageClient) attempt(ctx context.Context, endpoint, op string, payload []byte, dst any) (retryable bool, wait time.Duration, told bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return false, 0, false, &VoyageError{Kind: VoyageErrRequest, Op: op, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		// Do wraps the context error on cancellation; propagate that verbatim
		// so callers can distinguish a canceled run from a provider outage.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, 0, false, ctxErr
		}
		return true, 0, false, &VoyageError{Kind: VoyageErrTransient, Op: op,
			Err: errors.New(scrubSecret(err.Error(), c.cfg.APIKey))}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		if dst == nil {
			return false, 0, false, nil
		}
		if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
			return false, 0, false, &VoyageError{Kind: VoyageErrTransient, Status: resp.StatusCode, Op: op,
				Err: fmt.Errorf("decode response: %w", err)}
		}
		return false, 0, false, nil
	}

	detail := voyageErrDetail(resp.Body, c.cfg.APIKey)
	wait, told = retryAfter(resp.Header.Get("Retry-After"))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return true, wait, told,
			&VoyageError{Kind: VoyageErrRateLimited, Status: resp.StatusCode, Op: op, Detail: detail}
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return false, 0, false, &VoyageError{Kind: VoyageErrAuth, Status: resp.StatusCode, Op: op, Detail: detail}
	case resp.StatusCode >= 500:
		return true, wait, told,
			&VoyageError{Kind: VoyageErrTransient, Status: resp.StatusCode, Op: op, Detail: detail}
	default:
		return false, 0, false, &VoyageError{Kind: VoyageErrRequest, Status: resp.StatusCode, Op: op, Detail: detail}
	}
}

// voyageErrDetail extracts Voyage's `detail` error field, falling back to a
// bounded snippet of whatever the body actually was (a gateway HTML page, say).
func voyageErrDetail(body io.Reader, secret string) string {
	raw, _ := io.ReadAll(io.LimitReader(body, voyageMaxErrBody))
	var parsed struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(raw, &parsed) == nil && parsed.Detail != "" {
		return scrubSecret(strings.TrimSpace(parsed.Detail), secret)
	}
	return scrubSecret(strings.TrimSpace(string(raw)), secret)
}

// retryAfter parses the delay-seconds form of Retry-After, reporting whether the
// header carried a usable value. The HTTP-date form is treated as absent, so the
// caller backs off exponentially rather than retrying immediately.
func retryAfter(header string) (time.Duration, bool) {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds < 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

// voyageSleep waits for d or until ctx is done, whichever comes first.
func voyageSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
