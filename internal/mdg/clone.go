package mdg

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxArchiveBytes     = 256 << 20
	maxSourceBytes      = 1 << 30
	maxSourceFileBytes  = 100 << 20
	maxSourceEntries    = 100_000
	maxArchiveRedirects = 10
	sourceTimeout       = 5 * time.Minute
)

// FetchSource downloads a GitHub source archive or shallow-clones GitLab.
// GitHub's archiveLink is supplied by the caller's authenticated Spoon client.
// dest must be an empty directory; callers own cleanup, including on failure.
func FetchSource(ctx context.Context, provider, owner, repo, ref, dest string,
	archiveLink func(context.Context, string, string, string) (*url.URL, error),
) error {
	ctx, cancel := context.WithTimeout(ctx, sourceTimeout)
	defer cancel()
	switch provider {
	case "github":
		if archiveLink == nil {
			return fmt.Errorf("GitHub archive client unavailable")
		}
		link, err := archiveLink(ctx, owner, repo, ref)
		if err != nil {
			return fmt.Errorf("resolve source archive: %w", err)
		}
		return downloadSource(ctx, link, dest, http.DefaultTransport)
	case "gitlab":
		cloneURL := fmt.Sprintf("https://gitlab.com/%s/%s.git", owner, repo)
		cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--filter=blob:none", cloneURL, dest)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git clone %s/%s: %w", owner, repo, err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported source provider: %s", provider)
	}
}

func validArchiveURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.Host == "codeload.github.com" && u.User == nil
}

func downloadSource(ctx context.Context, link *url.URL, dest string, transport http.RoundTripper) error {
	if !validArchiveURL(link) {
		return fmt.Errorf("invalid GitHub archive download URL")
	}
	// A separate unauthenticated client prevents the API token being forwarded
	// to the signed download URL, including on redirects.
	client := &http.Client{Transport: transport, Timeout: sourceTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxArchiveRedirects || !validArchiveURL(req.URL) {
				return fmt.Errorf("invalid archive redirect")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.String(), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		// Signed archive URLs contain credentials; do not include them in errors.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("download source archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download source archive: HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxArchiveBytes {
		return fmt.Errorf("source archive exceeds %d bytes", maxArchiveBytes)
	}
	return extractSource(ctx, resp.Body, dest)
}

func extractSource(ctx context.Context, body io.Reader, dest string) error {
	compressed := &io.LimitedReader{R: body, N: maxArchiveBytes + 1}
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return fmt.Errorf("open source archive: %w", err)
	}
	defer gz.Close()
	expanded := &io.LimitedReader{R: gz, N: maxSourceBytes + 1}
	tr := tar.NewReader(expanded)
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	prefix := ""
	for entries := 0; ; entries++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read source archive: %w", err)
		}
		if entries >= maxSourceEntries {
			return fmt.Errorf("source archive exceeds %d entries", maxSourceEntries)
		}
		name := strings.TrimSuffix(header.Name, "/")
		if !filepath.IsLocal(name) || path.Clean(name) != name || strings.Contains(name, "\\") {
			return fmt.Errorf("unsafe archive path %q", header.Name)
		}
		top, relative, _ := strings.Cut(name, "/")
		if prefix == "" {
			prefix = top
		}
		if top != prefix {
			return fmt.Errorf("source archive has multiple roots")
		}
		if relative == "" {
			if header.Typeflag != tar.TypeDir {
				return fmt.Errorf("source archive root is not a directory")
			}
			continue
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(relative, 0o700); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// ponytail: skip symlinks; add in-root resolution if MDG needs linked sources.
			continue
		case tar.TypeReg:
			if header.Size < 0 || header.Size > maxSourceFileBytes || header.Size >= expanded.N {
				return fmt.Errorf("source file %q exceeds archive size limits", header.Name)
			}
			if err := root.MkdirAll(path.Dir(relative), 0o700); err != nil {
				return err
			}
			f, err := root.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(f, tr, header.Size)
			closeErr := f.Close()
			if copyErr != nil {
				return fmt.Errorf("extract source file: %w", copyErr)
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported archive entry type %d", header.Typeflag)
		}
	}
	// Drain through gzip to verify its checksum and enforce the expanded limit,
	// including tar padding and trailing data.
	if _, err := io.Copy(io.Discard, expanded); err != nil {
		return fmt.Errorf("finish source archive: %w", err)
	}
	if compressed.N <= 0 || expanded.N <= 0 {
		return fmt.Errorf("source archive exceeds size limits")
	}
	if prefix == "" {
		return fmt.Errorf("empty source archive")
	}
	return nil
}
