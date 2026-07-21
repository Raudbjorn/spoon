package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const proxyScrapeBase = "https://api.proxyscrape.com/v4/account"

var hostPortPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+:\d+$`)

type proxyCache struct {
	FetchedAt time.Time `json:"fetchedAt"`
	URLs      []string  `json:"urls"`
}

func bootstrapProxyPool(ctx context.Context, opts ProxyOptions) (*proxyPool, error) {
	if !opts.Enabled {
		return nil, nil
	}
	if opts.CacheTTL <= 0 {
		opts.CacheTTL = time.Hour
	}
	if urls := loadProxyCache(opts.CacheTTL); len(urls) > 0 {
		return newStaticProxyPool(urls), nil
	}
	key, _ := readTrimmedFile(opts.APIKeyFile)
	var urls []*url.URL
	if key != "" {
		urls = fetchProxyScrape(ctx, key, opts.WhitelistPublicIP)
	}
	if len(urls) == 0 {
		urls, _ = readProxyFile(opts.StaticFile)
	}
	if len(urls) == 0 {
		return nil, nil
	}
	_ = saveProxyCache(urls)
	return newStaticProxyPool(urls), nil
}

func readTrimmedFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func readProxyFile(path string) ([]*url.URL, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseProxyLines(string(data)), nil
}

func parseProxyLines(body string) []*url.URL {
	seen := map[string]bool{}
	var out []*url.URL
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.Contains(line, "://") {
			if !hostPortPattern.MatchString(line) {
				continue
			}
			line = "http://" + line
		}
		u, err := url.Parse(line)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		// A missing port is valid — http.ProxyURL uses the scheme's default
		// port. Only a genuinely malformed host (any other SplitHostPort error)
		// is skipped.
		if _, _, err := net.SplitHostPort(u.Host); err != nil && !strings.Contains(err.Error(), "missing port") {
			continue
		}
		if !seen[u.String()] {
			seen[u.String()] = true
			out = append(out, u)
		}
	}
	return out
}

func fetchProxyScrape(ctx context.Context, apiKey string, whitelist bool) []*url.URL {
	discovery := &http.Client{Transport: http.DefaultTransport, Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyScrapeBase+"/api-keys", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("api-token", apiKey)
	resp, err := discovery.Do(req)
	if err != nil {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if err != nil || resp.StatusCode/100 != 2 {
		return nil
	}
	var envelope struct {
		Data []struct {
			AllowedSubaccounts []string `json:"allowed_subaccounts"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Data) == 0 || len(envelope.Data[0].AllowedSubaccounts) == 0 {
		return nil
	}
	subaccount := url.PathEscape(envelope.Data[0].AllowedSubaccounts[0])
	if whitelist {
		whitelistProxyScrapeIP(ctx, discovery, apiKey, subaccount)
	}
	endpoint := proxyScrapeBase + "/" + subaccount + "/datacenter_shared/proxy-list?protocol=http&format=normal&status=online"
	fetchClient := &http.Client{Transport: http.DefaultTransport, Timeout: 15 * time.Second}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("api-token", apiKey)
	resp, err = fetchClient.Do(req)
	if err != nil {
		return nil
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	// Mirror the /api-keys status check above: on 401/403/500 the body is an
	// error page, not a proxy list, and must not reach parseProxyLines.
	if err != nil || resp.StatusCode/100 != 2 {
		return nil
	}
	return parseProxyLines(string(body))
}

func whitelistProxyScrapeIP(ctx context.Context, client *http.Client, apiKey, subaccount string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	resp.Body.Close()
	ip := strings.TrimSpace(string(body))
	if err != nil || net.ParseIP(ip) == nil {
		return
	}
	endpoint := proxyScrapeBase + "/" + subaccount + "/datacenter_shared/whitelist?type=add&ip%5B%5D=" + url.QueryEscape(ip)
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return
	}
	req.Header.Set("api-token", apiKey)
	resp, err = client.Do(req)
	if err == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
	}
}

func proxyCachePath() (string, error) {
	root := os.Getenv("XDG_CACHE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".cache")
	}
	return filepath.Join(root, "spoon", "proxies.json"), nil
}

func loadProxyCache(ttl time.Duration) []*url.URL {
	path, err := proxyCachePath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cached proxyCache
	if json.Unmarshal(data, &cached) != nil || time.Since(cached.FetchedAt) > ttl {
		return nil
	}
	return parseProxyLines(strings.Join(cached.URLs, "\n"))
}

func saveProxyCache(urls []*url.URL) error {
	path, err := proxyCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	values := make([]string, 0, len(urls))
	for _, u := range urls {
		values = append(values, u.String())
	}
	data, err := json.Marshal(proxyCache{FetchedAt: time.Now().UTC(), URLs: values})
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return fmt.Errorf("write proxy cache: %w", err)
	}
	// os.Rename cannot overwrite an existing destination on Windows.
	_ = os.Remove(path)
	return os.Rename(path+".tmp", path)
}
