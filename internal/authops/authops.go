// Package authops implements the "auth login" verb shared by the spn and spoon
// binaries: run the OAuth web flow and store the verified token in the config.
package authops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/svnbjrn/spoon/internal/agentio"
	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/oauth"
)

const (
	ClientIDEnv     = "SPOON_OAUTH_CLIENT_ID"
	ClientSecretEnv = "SPOON_OAUTH_CLIENT_SECRET"
	RedirectEnv     = "SPOON_OAUTH_REDIRECT_URI"
	ListenEnv       = "SPOON_OAUTH_LISTEN"

	// authDefaultScope is enough to read public repositories and resolve review
	// threads on them; private-repo access must be asked for with --scope.
	authDefaultScope = "public_repo"

	loginProbeTimeout = 15 * time.Second
	userEndpoint      = "https://api.github.com/user"
	maxUserResponse   = 1 << 20 // bytes
)

const authFlags = "auth login [--scope S] [--listen ADDR] [--timeout DUR]"

// Seams for tests; production uses the real flow and the real GitHub API.
var (
	oauthLogin = func(ctx context.Context, c oauth.Config, timeout time.Duration, announce func(string)) (oauth.Token, error) {
		return c.Login(ctx, timeout, announce)
	}
	fetchLoginName = githubLoginFor
)

// Run executes "auth <args>" for the binary named prog (used only in messages).
// It returns the process exit code. Output follows the spn contract: bare JSON
// on stdout, agentio envelopes on stderr, and the access token never printed.
func Run(prog string, args []string, stdout, stderr io.Writer, boot config.BootstrapResult, env map[string]string) int {
	authUsage := prog + " " + authFlags
	if len(args) > 0 && args[0] == "storage" {
		return runStorage(prog, args[1:], stdout, stderr, boot)
	}
	if len(args) == 0 || args[0] != "login" {
		return agentio.NewError(agentio.CodeBadInput, "missing or unknown verb (login, storage)", authUsage).Emit(stderr)
	}
	scope, listen, timeout := authDefaultScope, env[ListenEnv], oauth.DefaultTimeout
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		flag := rest[i]
		switch flag {
		case "--scope", "--listen", "--timeout":
			if i+1 >= len(rest) {
				return agentio.NewError(agentio.CodeBadInput, flag+" requires a value", authUsage).Emit(stderr)
			}
			i++
			switch flag {
			case "--scope":
				scope = rest[i]
			case "--listen":
				listen = rest[i]
			case "--timeout":
				d, err := time.ParseDuration(rest[i])
				if err != nil || d <= 0 {
					return agentio.NewError(agentio.CodeBadInput, "--timeout must be a positive duration such as 5m", authUsage).Emit(stderr)
				}
				timeout = d
			}
		default:
			return agentio.NewError(agentio.CodeBadInput, "unknown flag: "+flag, authUsage).Emit(stderr)
		}
	}

	// The token is persisted into the config file, so refuse up front if there is
	// nowhere to put it rather than after the user has authorized in the browser.
	if boot.Layer.State != config.LayerLoaded || boot.Config == nil || boot.Layer.Path == "" {
		return agentio.NewError(agentio.CodeBadInput, "no writable spoon config to store the token in",
			"Unset SPOON_NO_CONFIG and fix any config error, then retry.").Emit(stderr)
	}
	// A loaded layer is not necessarily writable (a readable /etc/spoon, say), so
	// probe publication now instead of after GitHub has issued a credential.
	if err := config.ProbeAtomicPublication(boot.Layer.Path); err != nil {
		return agentio.NewError(agentio.CodeBadInput, "cannot store the token: "+err.Error(),
			"Make the directory holding the config writable for this user, then retry.").Emit(stderr)
	}
	redirect := env[RedirectEnv]
	if redirect == "" {
		redirect = oauth.DefaultRedirectURI
	}
	cfg := oauth.Config{
		ClientID: env[ClientIDEnv], ClientSecret: env[ClientSecretEnv],
		RedirectURI: redirect, Scope: scope, ListenAddr: listen,
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return agentio.NewError(agentio.CodeBadInput, "OAuth app credentials are not set",
			"Export "+ClientIDEnv+" and "+ClientSecretEnv+" (never put them in the config file or command line).").Emit(stderr)
	}
	if err := cfg.Validate(); err != nil {
		return agentio.NewError(agentio.CodeBadInput, err.Error(), authUsage).Emit(stderr)
	}

	tok, err := oauthLogin(context.Background(), cfg, timeout, func(authURL string) {
		_ = agentio.WriteNDJSON(stderr, map[string]any{"info": map[string]any{
			"code":    "oauth_authorize",
			"message": "open this URL in a browser to authorize spoon",
			"details": map[string]any{"url": authURL, "redirect_uri": redirect, "timeout": timeout.String()},
		}})
	})
	if err != nil {
		return agentio.NewError(agentio.CodeAuthRequired, err.Error(), "Re-run `"+prog+" auth login` and approve the request in the browser before the timeout.").Emit(stderr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), loginProbeTimeout)
	defer cancel()
	login, err := fetchLoginName(ctx, tok.AccessToken)
	if err != nil {
		// Do not store a token that GitHub will not vouch for.
		return agentio.NewError(agentio.CodeUpstream, "token obtained but verification failed: "+err.Error(), agentio.RemediationUpstream()).Emit(stderr)
	}

	cfgCopy := *boot.Config
	// The dispatcher keeps the first token for each identity. A new login
	// (including --scope upgrades) must take precedence over older credentials.
	cfgCopy.GitHub.Tokens = prioritizeToken(cfgCopy.GitHub.Tokens, tok.AccessToken)
	if err := config.Save(boot.Layer.Path, &cfgCopy); err != nil {
		return agentio.NewError(agentio.CodeInternal, "authorized as "+login+" but saving the token failed: "+err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	emitInlineFallback(stderr, &cfgCopy)
	if err := agentio.WriteJSON(stdout, map[string]any{
		"login": login, "scope": tok.Scope, "saved": true, "config": boot.Layer.Path,
		"storage": config.TokenStorage(&cfgCopy),
	}); err != nil {
		return agentio.NewError(agentio.CodeInternal, err.Error(), agentio.RemediationInternal()).Emit(stderr)
	}
	return 0
}

// emitInlineFallback reports, as a structured envelope on the command's own
// stderr, that Save kept tokens in the config file because the keyring failed.
func emitInlineFallback(stderr io.Writer, c *config.Config) {
	cause := c.InlineFallback()
	if cause == nil {
		return
	}
	_ = agentio.WriteNDJSON(stderr, map[string]any{"warning": map[string]any{
		"code":        "keyring_unavailable",
		"message":     "GitHub tokens stay in the 0600 config file: the OS keyring could not be used: " + cause.Error(),
		"remediation": keyringRemediation,
	}})
}

func prioritizeToken(list []string, v string) []string {
	others := slices.DeleteFunc(slices.Clone(list), func(token string) bool { return token == v })
	return append([]string{v}, others...)
}

func githubLoginFor(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, userEndpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "spoon")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET /user returned HTTP %d", resp.StatusCode)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxUserResponse)).Decode(&u); err != nil || strings.TrimSpace(u.Login) == "" {
		return "", fmt.Errorf("GET /user returned no login")
	}
	return u.Login, nil
}
