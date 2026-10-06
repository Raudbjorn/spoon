package authops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/svnbjrn/spoon/internal/config"
	"github.com/svnbjrn/spoon/internal/oauth"
)

const testOAuthToken = "gho_test_token_value"

func authBoot(t *testing.T, tokens ...string) config.BootstrapResult {
	t.Helper()
	cfg := &config.Config{}
	cfg.GitHub.Tokens = tokens
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return config.BootstrapResult{Config: cfg, Layer: config.LoadedLayer{State: config.LayerLoaded, Path: path}}
}

func stubOAuth(t *testing.T, tok oauth.Token, err error) {
	t.Helper()
	prevLogin, prevName := oauthLogin, fetchLoginName
	oauthLogin = func(_ context.Context, _ oauth.Config, _ time.Duration, announce func(string)) (oauth.Token, error) {
		announce("https://github.com/login/oauth/authorize?x=1")
		return tok, err
	}
	fetchLoginName = func(context.Context, string) (string, error) { return "octocat", nil }
	t.Cleanup(func() { oauthLogin, fetchLoginName = prevLogin, prevName })
}

func authEnv() map[string]string {
	return map[string]string{ClientIDEnv: "id", ClientSecretEnv: "shh"}
}

func errCode(t *testing.T, stderr *bytes.Buffer) string {
	t.Helper()
	var env struct {
		Error struct{ Code string } `json:"error"`
	}
	// stderr may carry info envelopes before the error; the error is the last value.
	raw := stderr.Bytes()
	if i := bytes.LastIndex(raw, []byte("{\n  \"error\"")); i >= 0 {
		raw = raw[i:]
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("stderr is not an error envelope: %q", stderr.String())
	}
	return env.Error.Code
}

func TestAuthLoginSavesTokenAndNeverPrintsIt(t *testing.T) {
	stubOAuth(t, oauth.Token{AccessToken: testOAuthToken, Scope: "public_repo"}, nil)
	boot := authBoot(t, "existing")
	var stdout, stderr bytes.Buffer
	if code := Run("spn", []string{"login"}, &stdout, &stderr, boot, authEnv()); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), testOAuthToken) {
		t.Error("access token appeared in output")
	}
	if !strings.Contains(stderr.String(), "oauth_authorize") {
		t.Errorf("no authorize info envelope on stderr: %q", stderr.String())
	}
	saved, err := config.Load(boot.Layer.Path)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got := saved.GitHub.Tokens; len(got) != 2 || got[0] != "existing" || got[1] != testOAuthToken {
		t.Errorf("saved tokens = %v, want existing token kept and new one appended", got)
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil || out["login"] != "octocat" || out["saved"] != true {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestAuthLoginRejects(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"no verb", nil, authEnv(), "bad_input"},
		{"unknown verb", []string{"logout"}, authEnv(), "bad_input"},
		{"unknown flag", []string{"login", "-scope", "x"}, authEnv(), "bad_input"},
		{"flag without value", []string{"login", "--scope"}, authEnv(), "bad_input"},
		{"bad timeout", []string{"login", "--timeout", "soon"}, authEnv(), "bad_input"},
		{"missing creds", []string{"login"}, map[string]string{}, "bad_input"},
		{"bad redirect", []string{"login"}, map[string]string{ClientIDEnv: "i", ClientSecretEnv: "s", RedirectEnv: "/auth"}, "bad_input"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubOAuth(t, oauth.Token{AccessToken: testOAuthToken}, nil)
			var stdout, stderr bytes.Buffer
			code := Run("spn", tc.args, &stdout, &stderr, authBoot(t), tc.env)
			if code != 2 || errCode(t, &stderr) != tc.want {
				t.Errorf("exit %d code %q, want exit 2 %q (stderr %q)", code, errCode(t, &stderr), tc.want, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout not clean: %q", stdout.String())
			}
		})
	}
}

func TestAuthLoginWithoutWritableConfigFailsBeforeAuthorizing(t *testing.T) {
	called := false
	stubOAuth(t, oauth.Token{}, nil)
	oauthLogin = func(context.Context, oauth.Config, time.Duration, func(string)) (oauth.Token, error) {
		called = true
		return oauth.Token{}, nil
	}
	var stdout, stderr bytes.Buffer
	boot := config.BootstrapResult{Layer: config.LoadedLayer{State: config.LayerDisabled}}
	if code := Run("spn", []string{"login"}, &stdout, &stderr, boot, authEnv()); code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	if called {
		t.Error("the browser flow ran although there is nowhere to store the token")
	}
}

func TestAuthLoginDeniedIsAuthRequired(t *testing.T) {
	stubOAuth(t, oauth.Token{}, errors.New("oauth: authorization denied: access_denied"))
	boot := authBoot(t)
	var stdout, stderr bytes.Buffer
	if code := Run("spn", []string{"login"}, &stdout, &stderr, boot, authEnv()); code == 0 {
		t.Fatal("denied login exited 0")
	}
	if c := errCode(t, &stderr); c != "auth_required" {
		t.Errorf("code = %q, want auth_required", c)
	}
	saved, _ := config.Load(boot.Layer.Path)
	if saved != nil && len(saved.GitHub.Tokens) != 0 {
		t.Errorf("a denied login stored tokens: %v", saved.GitHub.Tokens)
	}
}

func TestAuthLoginUnverifiedTokenIsNotStored(t *testing.T) {
	stubOAuth(t, oauth.Token{AccessToken: testOAuthToken}, nil)
	fetchLoginName = func(context.Context, string) (string, error) { return "", errors.New("GET /user returned HTTP 401") }
	boot := authBoot(t)
	var stdout, stderr bytes.Buffer
	if code := Run("spn", []string{"login"}, &stdout, &stderr, boot, authEnv()); code == 0 {
		t.Fatal("unverified token exited 0")
	}
	if strings.Contains(stderr.String(), testOAuthToken) {
		t.Error("token leaked into the error")
	}
	if saved, _ := config.Load(boot.Layer.Path); saved != nil && len(saved.GitHub.Tokens) != 0 {
		t.Errorf("unverified token was stored: %v", saved.GitHub.Tokens)
	}
}
