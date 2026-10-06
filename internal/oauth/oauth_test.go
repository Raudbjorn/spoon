package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

type tokenServer struct {
	*httptest.Server
	form chan url.Values
}

func newTokenServer(t *testing.T, reply any, status int) *tokenServer {
	t.Helper()
	ts := &tokenServer{form: make(chan url.Values, 1)}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ts.form <- r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func testConfig(t *testing.T, tokenSrv *tokenServer) (Config, string) {
	addr := freeAddr(t)
	return Config{
		ClientID: "id", ClientSecret: "shh", Scope: "public_repo",
		RedirectURI: "https://spoon.example/auth", ListenAddr: addr,
		AuthorizeURL: "https://gh.example/authorize", TokenURL: tokenSrv.URL,
	}, addr
}

// run starts Login and returns once the listener is announced.
func run(t *testing.T, c Config, timeout time.Duration) (authURL string, result <-chan error, tok *Token) {
	t.Helper()
	announced := make(chan string, 1)
	res := make(chan error, 1)
	tok = new(Token)
	go func() {
		got, err := c.Login(context.Background(), timeout, func(u string) { announced <- u })
		*tok = got
		res <- err
	}()
	select {
	case authURL = <-announced:
	case err := <-res:
		t.Fatalf("Login returned before announcing: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Login never announced")
	}
	return authURL, res, tok
}

func get(t *testing.T, rawURL string) int {
	t.Helper()
	resp, err := http.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestLoginExchangesCodeWithPKCE(t *testing.T) {
	ts := newTokenServer(t, map[string]string{"access_token": "gho_x", "token_type": "bearer", "scope": "public_repo"}, 200)
	c, addr := testConfig(t, ts)
	authURL, res, tok := run(t, c, 10*time.Second)

	u, _ := url.Parse(authURL)
	q := u.Query()
	if q.Get("client_id") != "id" || q.Get("redirect_uri") != c.RedirectURI || q.Get("scope") != "public_repo" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("authorize URL params = %v", q)
	}
	if got := get(t, "http://"+addr+"/auth?code=abc&state="+url.QueryEscape(q.Get("state"))); got != 200 {
		t.Fatalf("callback status = %d, want 200", got)
	}
	if err := <-res; err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tok.AccessToken != "gho_x" || tok.Scope != "public_repo" {
		t.Errorf("token = %+v", *tok)
	}
	form := <-ts.form
	sum := sha256.Sum256([]byte(form.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Error("code_verifier does not match the announced code_challenge")
	}
	if form.Get("code") != "abc" || form.Get("client_secret") != "shh" || form.Get("redirect_uri") != c.RedirectURI {
		t.Errorf("exchange form = %v", form)
	}
	if strings.Contains(authURL, "shh") {
		t.Error("authorize URL leaks the client secret")
	}
}

func TestLoginIgnoresWrongStateThenSucceeds(t *testing.T) {
	ts := newTokenServer(t, map[string]string{"access_token": "gho_x"}, 200)
	c, addr := testConfig(t, ts)
	authURL, res, _ := run(t, c, 10*time.Second)
	u, _ := url.Parse(authURL)

	if got := get(t, "http://"+addr+"/auth?code=evil&state=nope"); got != http.StatusBadRequest {
		t.Fatalf("forged callback status = %d, want 400", got)
	}
	select {
	case err := <-res:
		t.Fatalf("a forged callback ended the login: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if len(ts.form) != 0 {
		t.Error("token endpoint was called for a forged callback")
	}
	get(t, "http://"+addr+"/auth?code=ok&state="+url.QueryEscape(u.Query().Get("state")))
	if err := <-res; err != nil {
		t.Fatalf("Login: %v", err)
	}
}

func TestLoginReportsDenial(t *testing.T) {
	ts := newTokenServer(t, nil, 200)
	c, addr := testConfig(t, ts)
	authURL, res, _ := run(t, c, 10*time.Second)
	u, _ := url.Parse(authURL)
	get(t, "http://"+addr+"/auth?error=access_denied&state="+url.QueryEscape(u.Query().Get("state")))
	if err := <-res; err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v, want access_denied", err)
	}
}

func TestLoginTokenEndpointError(t *testing.T) {
	ts := newTokenServer(t, map[string]string{"error": "bad_verification_code", "error_description": "expired"}, 200)
	c, addr := testConfig(t, ts)
	authURL, res, _ := run(t, c, 10*time.Second)
	u, _ := url.Parse(authURL)
	if got := get(t, "http://"+addr+"/auth?code=old&state="+url.QueryEscape(u.Query().Get("state"))); got != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", got)
	}
	err := <-res
	if err == nil || !strings.Contains(err.Error(), "bad_verification_code") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "shh") {
		t.Error("error leaks the client secret")
	}
}

func TestLoginRootRedirectsToAuthorize(t *testing.T) {
	ts := newTokenServer(t, nil, 200)
	c, addr := testConfig(t, ts)
	authURL, _, _ := run(t, c, 300*time.Millisecond)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != authURL {
		t.Errorf("/ -> %d %q, want 302 to the authorize URL", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestLoginTimesOut(t *testing.T) {
	ts := newTokenServer(t, nil, 200)
	c, _ := testConfig(t, ts)
	_, res, _ := run(t, c, 50*time.Millisecond)
	if err := <-res; err == nil || !strings.Contains(err.Error(), "no callback") {
		t.Fatalf("err = %v, want timeout", err)
	}
}

func TestValidateAndLoopback(t *testing.T) {
	ok := Config{ClientID: "a", ClientSecret: "b", RedirectURI: DefaultRedirectURI}
	cases := map[string]Config{
		"no id":        {ClientSecret: "b", RedirectURI: DefaultRedirectURI},
		"no secret":    {ClientID: "a", RedirectURI: DefaultRedirectURI},
		"relative uri": {ClientID: "a", ClientSecret: "b", RedirectURI: "/auth"},
		"wrong path":   {ClientID: "a", ClientSecret: "b", RedirectURI: "https://x.example/callback"},
	}
	for name, c := range cases {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, c)
		}
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	ok.ListenAddr = "0.0.0.0:8790"
	if _, err := ok.Login(context.Background(), time.Second, nil); err == nil || !strings.Contains(err.Error(), "not loopback") {
		t.Errorf("non-loopback listen = %v, want refusal", err)
	}
}
