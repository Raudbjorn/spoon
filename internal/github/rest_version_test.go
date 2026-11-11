package github

import (
	"net/http"
	"net/url"
	"testing"
)

func TestRESTBaseURLFollowsHost(t *testing.T) {
	cases := map[string]string{
		"github.com":        "",
		"":                  "",
		"ghe.example.com":   "https://ghe.example.com/api/v3/",
		"octo.ghe.com":      "https://api.octo.ghe.com/",
		"garage.github.com": "https://garage.github.com/api/v3/",
	}
	for host, want := range cases {
		if got := restBaseURL(host); got != want {
			t.Errorf("restBaseURL(%q) = %q, want %q", host, got, want)
		}
	}
	rc, err := newRESTClientForHost("ghe.example.com", "tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := rc.BaseURL(); got != "https://ghe.example.com/api/v3/" {
		t.Errorf("enterprise client base = %q", got)
	}
}

func TestRequireAPIOriginRejectsForeignAndDowngradedLinks(t *testing.T) {
	rc, err := newRESTClient("tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{rest: rc}
	for next, ok := range map[string]bool{
		"https://api.github.com/repos/o/r/forks?page=2": true,
		"/repos/o/r/forks?page=2":                       true,
		"https://evil.example/repos/o/r/forks?page=2":   false,
		"http://api.github.com/repos/o/r/forks?page=2":  false,
	} {
		if err := c.requireAPIOrigin(next); (err == nil) != ok {
			t.Errorf("requireAPIOrigin(%q) = %v, want ok=%v", next, err, ok)
		}
	}
}

func TestRESTClientRefusesSchemeDowngradeRedirect(t *testing.T) {
	rc, err := newRESTClient("tok", nil)
	if err != nil {
		t.Fatal(err)
	}
	via := []*http.Request{{URL: &url.URL{Scheme: "https", Host: "api.github.com"}}}
	next := &http.Request{URL: &url.URL{Scheme: "http", Host: "api.github.com"}}
	if err := rc.Client().CheckRedirect(next, via); err == nil {
		t.Fatal("https->http redirect on the same host was allowed")
	}
}
