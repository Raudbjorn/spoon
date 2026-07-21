package embed

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The vendored fastembed-go downloader is unsafe: it calls http.Get with no
// timeout or context and extracts the archive with
// filepath.Join(target, header.Name) and no containment check, so a crafted
// entry named ../../../.bashrc escapes the cache directory. spoon invokes it by
// default on every fresh install.
//
// retrieveModel short-circuits when cacheDir/<model> already exists, so
// populating that directory ourselves means the vulnerable path never runs. We
// download and extract with the checks the upstream lacks.
const (
	fastEmbedModelName  = "fast-bge-small-en-v1.5"
	fastEmbedArchiveURL = "https://storage.googleapis.com/qdrant-fastembed/" + fastEmbedModelName + ".tar.gz"

	// Caps bound a hostile or corrupt archive: a decompression bomb cannot
	// exhaust the disk, and an endless entry stream cannot spin forever.
	fastEmbedMaxArchiveBytes = 1 << 30 // 1 GiB decompressed
	fastEmbedMaxEntryBytes   = 1 << 30
	fastEmbedMaxEntries      = 4096
	fastEmbedDownloadTimeout = 15 * time.Minute
)

// fastEmbedArchiveSHA256 pins the archive digest. TLS already covers a passive
// network attacker; this additionally covers a compromised bucket or a
// trusted-CA MITM. Set SPOON_FASTEMBED_SHA256 to enable verification — when
// empty the download still gets containment, type and size enforcement, which
// is what closes the traversal hole.
var fastEmbedArchiveSHA256 = strings.TrimSpace(os.Getenv("SPOON_FASTEMBED_SHA256"))

// provisionFastEmbedModel ensures cacheDir/<model> exists, downloading and
// extracting it safely if absent. It is a no-op when the model is already
// cached, including when a previous run used the upstream downloader.
func provisionFastEmbedModel(ctx context.Context, cacheDir string) error {
	dest := filepath.Join(cacheDir, fastEmbedModelName)
	if _, err := os.Stat(dest); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		// A permission or I/O error is not "absent" — surface it rather than
		// falling through to a full download that would then fail at rename.
		return fmt.Errorf("inspect fastembed cache %s: %w", dest, err)
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return fmt.Errorf("create fastembed cache dir: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, fastEmbedDownloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fastEmbedArchiveURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download fastembed model: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("download fastembed model: %s", resp.Status)
	}

	// Stage in a sibling directory so a partial or rejected extraction never
	// leaves something that looks like a valid cache entry.
	staging, err := os.MkdirTemp(cacheDir, ".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	body := io.Reader(io.LimitReader(resp.Body, fastEmbedMaxArchiveBytes))
	var digest []byte
	if fastEmbedArchiveSHA256 != "" {
		// Buffer to disk rather than memory so verification precedes extraction
		// without holding the whole archive resident.
		tmp, err := os.CreateTemp(staging, "archive-*.tar.gz")
		if err != nil {
			return err
		}
		defer tmp.Close()
		sum := sha256.New()
		if _, err := io.Copy(io.MultiWriter(tmp, sum), body); err != nil {
			return fmt.Errorf("download fastembed model: %w", err)
		}
		digest = sum.Sum(nil)
		if got := hex.EncodeToString(digest); !strings.EqualFold(got, fastEmbedArchiveSHA256) {
			return fmt.Errorf("fastembed archive digest mismatch: got %s, want %s", got, fastEmbedArchiveSHA256)
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		body = tmp
	}

	if err := safeExtractTarGz(body, staging); err != nil {
		return err
	}

	extracted := filepath.Join(staging, fastEmbedModelName)
	if _, err := os.Stat(extracted); err != nil {
		return fmt.Errorf("fastembed archive did not contain %s: %w", fastEmbedModelName, err)
	}
	if err := os.Rename(extracted, dest); err != nil {
		// Another process may have won the race; its copy is equally valid.
		if _, statErr := os.Stat(dest); statErr == nil {
			return nil
		}
		return fmt.Errorf("install fastembed model: %w", err)
	}
	return nil
}

// safeExtractTarGz extracts a gzipped tar into target, rejecting anything that
// would write outside it or that is not a plain file or directory.
func safeExtractTarGz(r io.Reader, target string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("read fastembed archive: %w", err)
	}
	defer gz.Close()

	root, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	tr := tar.NewReader(io.LimitReader(gz, fastEmbedMaxArchiveBytes))
	var (
		entries int
		written int64
	)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read fastembed archive: %w", err)
		}
		entries++
		if entries > fastEmbedMaxEntries {
			return fmt.Errorf("fastembed archive has more than %d entries", fastEmbedMaxEntries)
		}

		// Symlinks, hardlinks, devices and fifos are never legitimate here, and
		// a symlink is a second route out of the directory even when the entry
		// name itself is contained.
		switch header.Typeflag {
		case tar.TypeDir, tar.TypeReg:
		default:
			return fmt.Errorf("fastembed archive entry %q has disallowed type %d", header.Name, header.Typeflag)
		}

		path, err := containedPath(root, header.Name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			remaining := fastEmbedMaxArchiveBytes - written
			if remaining <= 0 {
				return fmt.Errorf("fastembed archive exceeds %d bytes", int64(fastEmbedMaxArchiveBytes))
			}
			if remaining > fastEmbedMaxEntryBytes {
				remaining = fastEmbedMaxEntryBytes
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			n, err := io.Copy(f, io.LimitReader(tr, remaining+1))
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if n > remaining {
				return fmt.Errorf("fastembed archive entry %q exceeds the size cap", header.Name)
			}
			written += n
		}
	}
}

// containedPath resolves name against root and fails if the result would land
// outside it, rejecting absolute paths and ../ traversal alike.
func containedPath(root, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("fastembed archive entry has an empty name")
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", fmt.Errorf("fastembed archive entry %q is an absolute path", name)
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("fastembed archive entry %q escapes the cache directory", name)
	}
	path := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("fastembed archive entry %q is not resolvable: %w", name, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("fastembed archive entry %q escapes the cache directory", name)
	}
	return path, nil
}

// discardFastEmbedCache removes the cached model directory so the next
// provision re-fetches it. Guarded so a misconfigured CacheDir cannot turn
// into a recursive delete of something unrelated: only the model directory
// beneath it is removed, never the cache root itself.
func discardFastEmbedCache(cacheDir string) error {
	if strings.TrimSpace(cacheDir) == "" {
		return fmt.Errorf("refusing to discard an empty fastembed cache dir")
	}
	dest := filepath.Join(cacheDir, fastEmbedModelName)
	if filepath.Clean(dest) == filepath.Clean(cacheDir) {
		return fmt.Errorf("refusing to discard the fastembed cache root %q", cacheDir)
	}
	if err := os.RemoveAll(dest); err != nil {
		return fmt.Errorf("discard fastembed cache %s: %w", dest, err)
	}
	return nil
}
