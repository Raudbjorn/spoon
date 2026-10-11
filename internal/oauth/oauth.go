// Package oauth runs GitHub's OAuth web flow for a command-line tool: a
// short-lived loopback HTTP listener receives the redirect, checks the state,
// and exchanges the code (with PKCE) for an access token.
//
// The listener exists only for the duration of one Login call and binds a
// loopback address, so the callback is never reachable from the network. A TLS
// reverse proxy owns the public redirect URI (see deploy/nginx-spoon.conf).
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// DefaultListenAddr is where the callback listener binds. The nginx vhost for
	// the redirect URI proxies to this address.
	DefaultListenAddr = "127.0.0.1:8790"
	// DefaultRedirectURI is the callback registered on the GitHub OAuth app.
	DefaultRedirectURI = "https://spoon.s8n.is/auth"

	authorizeURL = "https://github.com/login/oauth/authorize"
	tokenURL     = "https://github.com/login/oauth/access_token"

	// DefaultTimeout bounds how long Login waits for the user to finish in the browser.
	DefaultTimeout = 5 * time.Minute

	callbackPath        = "/auth"
	stateBytes          = 32
	verifierBytes       = 48
	maxTokenResponse    = 1 << 20 // bytes
	readHeaderTimeout   = 10 * time.Second
	shutdownGrace       = 3 * time.Second
	tokenExchangeBudget = 30 * time.Second
)

// Config describes one OAuth app and where to listen for its callback.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	Scope        string // space- or comma-separated; empty requests no scopes
	ListenAddr   string // default DefaultListenAddr

	// Overridable for tests; default to GitHub's endpoints and a bounded client.
	AuthorizeURL string
	TokenURL     string
	HTTPClient   *http.Client
}

// Token is the result of a successful exchange. It deliberately has no String
// or MarshalJSON method that would include AccessToken.
type Token struct {
	AccessToken string
	Scope       string
	TokenType   string
}

// Validate reports a configuration error before any socket is opened.
func (c Config) Validate() error {
	switch {
	case c.ClientID == "":
		return errors.New("oauth: client id is empty")
	case c.ClientSecret == "":
		return errors.New("oauth: client secret is empty")
	}
	ru, err := url.Parse(c.RedirectURI)
	if err != nil || ru.Scheme == "" || ru.Host == "" {
		return fmt.Errorf("oauth: redirect uri %q is not an absolute URL", c.RedirectURI)
	}
	if ru.Path != callbackPath {
		return fmt.Errorf("oauth: redirect uri path must be %s, got %q", callbackPath, ru.Path)
	}
	return nil
}

// Login runs the flow. announce receives the authorize URL as soon as the
// listener is up; the caller shows it to the user (opening a browser is the
// caller's business). It returns when a token is obtained, the user denies, the
// state check fails, or ctx / the timeout ends.
func (c Config) Login(ctx context.Context, timeout time.Duration, announce func(authURL string)) (Token, error) {
	if err := c.Validate(); err != nil {
		return Token{}, err
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	state, err := randomString(stateBytes)
	if err != nil {
		return Token{}, err
	}
	verifier, err := randomString(verifierBytes)
	if err != nil {
		return Token{}, err
	}
	addr := c.ListenAddr
	if addr == "" {
		addr = DefaultListenAddr
	}
	if err := requireLoopback(addr); err != nil {
		return Token{}, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return Token{}, fmt.Errorf("oauth: listen on %s: %w", addr, err)
	}

	authURL := c.authorizeURL(state, verifier)
	type outcome struct {
		tok Token
		err error
	}
	done := make(chan outcome, 1)
	var once sync.Once
	// claimed is set by the first valid callback so a duplicate cannot redeem
	// the same single-use code while the first exchange is in flight.
	var claimed atomic.Bool
	finish := func(tok Token, err error) { once.Do(func() { done <- outcome{tok, err} }) }

	mux := http.NewServeMux()
	// The app URL (https://spoon.s8n.is) starts the flow, so opening it in a
	// browser works as well as the printed link.
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, authURL, http.StatusFound)
	})
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			// Not our flow (stale tab, or a forged callback): do not end the login.
			writePage(w, http.StatusBadRequest, "Invalid state", "This callback does not belong to the running login.")
			return
		}
		if e := q.Get("error"); e != "" {
			writePage(w, http.StatusForbidden, "Authorization failed", e)
			finish(Token{}, fmt.Errorf("oauth: authorization denied: %s", sanitize(e)))
			return
		}
		code := q.Get("code")
		if code == "" {
			writePage(w, http.StatusBadRequest, "Missing code", "The callback carried no authorization code.")
			return
		}
		if !claimed.CompareAndSwap(false, true) {
			writePage(w, http.StatusConflict, "Already handled", "This login is already being completed; see the terminal.")
			return
		}
		ectx, cancel := context.WithTimeout(r.Context(), tokenExchangeBudget)
		defer cancel()
		tok, err := c.exchange(ectx, code, verifier)
		if err != nil {
			writePage(w, http.StatusBadGateway, "Token exchange failed", "See the terminal for details.")
			finish(Token{}, err)
			return
		}
		writePage(w, http.StatusOK, "spoon is authorized", "You can close this tab.")
		finish(tok, nil)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	if announce != nil {
		announce(authURL)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case o := <-done:
		return o.tok, o.err
	case err := <-serveErr:
		return Token{}, fmt.Errorf("oauth: callback listener stopped: %w", err)
	case <-timer.C:
		return Token{}, fmt.Errorf("oauth: no callback within %s", timeout)
	case <-ctx.Done():
		return Token{}, ctx.Err()
	}
}

func (c Config) authorizeURL(state, verifier string) string {
	base := c.AuthorizeURL
	if base == "" {
		base = authorizeURL
	}
	sum := sha256.Sum256([]byte(verifier))
	v := url.Values{}
	v.Set("client_id", c.ClientID)
	v.Set("redirect_uri", c.RedirectURI)
	v.Set("state", state)
	v.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	v.Set("code_challenge_method", "S256")
	if c.Scope != "" {
		v.Set("scope", c.Scope)
	}
	return base + "?" + v.Encode()
}

func (c Config) exchange(ctx context.Context, code, verifier string) (Token, error) {
	endpoint := c.TokenURL
	if endpoint == "" {
		endpoint = tokenURL
	}
	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", c.RedirectURI)
	form.Set("code_verifier", verifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: tokenExchangeBudget}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("oauth: token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponse))
	if err != nil {
		return Token{}, fmt.Errorf("oauth: read token response: %w", err)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
		ExpiresIn   int64  `json:"expires_in"`
		Refresh     string `json:"refresh_token"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Token{}, fmt.Errorf("oauth: token endpoint returned HTTP %d with an unreadable body", resp.StatusCode)
	}
	if out.Error != "" {
		return Token{}, fmt.Errorf("oauth: token endpoint refused the code: %s (%s)", sanitize(out.Error), sanitize(out.Description))
	}
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		return Token{}, fmt.Errorf("oauth: token endpoint returned HTTP %d without an access token", resp.StatusCode)
	}
	if out.Refresh != "" || out.ExpiresIn > 0 {
		// Only the access token is stored and there is no refresh path, so an
		// expiring token would silently stop working.
		return Token{}, errors.New("oauth: the app issued an expiring token (expires_in/refresh_token); disable token expiration on the OAuth app and do not request offline_access")
	}
	return Token{AccessToken: out.AccessToken, Scope: out.Scope, TokenType: out.TokenType}, nil
}

// requireLoopback refuses a callback listener that is reachable off-host.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("oauth: listen address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("oauth: listen address %q is not loopback", addr)
	}
	return nil
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauth: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sanitize keeps remote-supplied strings single-line and bounded before they
// reach an error message.
func sanitize(s string) string {
	const maxLen = 200
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if runes := []rune(s); len(runes) > maxLen {
		s = string(runes[:maxLen])
	}
	return s
}

func writePage(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>%s</title><h1>%s</h1><p>%s</p>\n",
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(sanitize(detail)))
}
