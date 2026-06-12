// Package models manages the local model repository for spoon's in-process
// OpenVINO features: a registry of default pre-converted models per feature
// and a pure-Go HuggingFace downloader. No external tools (ovms, optimum,
// huggingface-cli) are involved.
package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Feature identifies a model-backed capability.
type Feature string

const (
	FeatureEmbedder Feature = "embedder"
	FeatureReranker Feature = "reranker"
	FeatureLabeler  Feature = "labeler"
)

// Default pre-converted model repos (OpenVINO HuggingFace organization).
// All are public, no auth required.
var defaults = map[Feature]string{
	FeatureEmbedder: "OpenVINO/bge-base-en-v1.5-fp16-ov",
	FeatureReranker: "OpenVINO/bge-reranker-base-fp16-ov",
	FeatureLabeler:  "OpenVINO/Qwen2.5-1.5B-Instruct-int4-ov",
}

// approxSizeMB is shown in consent prompts. Rough download sizes.
var approxSizeMB = map[Feature]int{
	FeatureEmbedder: 440,
	FeatureReranker: 560,
	FeatureLabeler:  1100,
}

// DefaultRepo returns the default HF repo for a feature.
func DefaultRepo(f Feature) string { return defaults[f] }

// ApproxSizeMB returns the rough download size for a feature's default model.
func ApproxSizeMB(f Feature) int { return approxSizeMB[f] }

// BaseDir returns the local model repository root:
// $XDG_DATA_HOME/spoon/models, falling back to ~/.local/share/spoon/models.
func BaseDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "spoon", "models"), nil
}

// LocalDir returns the on-disk directory for a repo ("Org/name" →
// <base>/Org/name), matching the layout `ovms --pull` uses so the two can
// share a repository.
func LocalDir(repo string) (string, error) {
	base, err := BaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, filepath.FromSlash(repo)), nil
}

// IsDownloaded reports whether dir already contains a usable OpenVINO model.
func IsDownloaded(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "openvino_model.xml"))
	return err == nil
}

// hfHost is swappable for tests.
var hfHost = "https://huggingface.co"

// Progress receives download progress: the current file, bytes done and
// total bytes for that file (total may be 0 when unknown). May be nil.
type Progress func(file string, done, total int64)

var httpClient = &http.Client{Timeout: 30 * time.Minute}

// treeEntry is one record of HF's /api/models/<repo>/tree/main response.
type treeEntry struct {
	Type string `json:"type"` // "file" | "directory"
	Path string `json:"path"`
	Size int64  `json:"size"`
	LFS  *struct {
		Size int64 `json:"size"`
	} `json:"lfs"`
}

// skippedFiles are repo files irrelevant to inference; not downloading them
// keeps the transfer minimal.
var skippedPrefixes = []string{".git"}

func skipFile(path string) bool {
	for _, p := range skippedPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// Download fetches every file of an HF repo's main branch into destDir.
// Files that already exist with the expected size are skipped, so an
// interrupted download resumes file-by-file. Returns the total bytes
// fetched.
func Download(ctx context.Context, repo, destDir string, progress Progress) (int64, error) {
	entries, err := listRepo(ctx, repo)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return 0, err
	}
	var fetched int64
	for _, e := range entries {
		if e.Type != "file" || skipFile(e.Path) {
			continue
		}
		want := e.Size
		if e.LFS != nil && e.LFS.Size > 0 {
			want = e.LFS.Size
		}
		dest := filepath.Join(destDir, filepath.FromSlash(e.Path))
		if st, err := os.Stat(dest); err == nil && want > 0 && st.Size() == want {
			continue // already complete
		}
		n, err := fetchFile(ctx, repo, e.Path, dest, want, progress)
		if err != nil {
			return fetched, fmt.Errorf("download %s/%s: %w", repo, e.Path, err)
		}
		fetched += n
	}
	return fetched, nil
}

// listRepo returns the file entries of the repo's main branch (recursive).
func listRepo(ctx context.Context, repo string) ([]treeEntry, error) {
	u := fmt.Sprintf("%s/api/models/%s/tree/main?recursive=true", hfHost, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("list %s: HTTP %d: %s", repo, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var entries []treeEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("parse %s tree: %w", repo, err)
	}
	return entries, nil
}

// fetchFile downloads one repo file to dest atomically (tmp + rename).
func fetchFile(ctx context.Context, repo, path, dest string, total int64, progress Progress) (int64, error) {
	u := fmt.Sprintf("%s/%s/resolve/main/%s", hfHost, repo, url.PathEscape(path))
	// PathEscape escapes "/" too; repo files may sit in subdirs.
	u = strings.ReplaceAll(u, "%2F", "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if total == 0 && resp.ContentLength > 0 {
		total = resp.ContentLength
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".spoon-dl-*")
	if err != nil {
		return 0, err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	var done int64
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := tmp.Write(buf[:n]); werr != nil {
				return done, werr
			}
			done += int64(n)
			if progress != nil {
				progress(path, done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, rerr
		}
	}
	if err := tmp.Close(); err != nil {
		return done, err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return done, err
	}
	return done, nil
}

// Ensure returns the local directory for a feature's default model,
// downloading it first when absent. progress may be nil.
func Ensure(ctx context.Context, f Feature, progress Progress) (string, error) {
	repo := DefaultRepo(f)
	if repo == "" {
		return "", fmt.Errorf("no default model registered for feature %q", f)
	}
	dir, err := LocalDir(repo)
	if err != nil {
		return "", err
	}
	if IsDownloaded(dir) {
		return dir, nil
	}
	if _, err := Download(ctx, repo, dir, progress); err != nil {
		return "", err
	}
	if !IsDownloaded(dir) {
		return "", fmt.Errorf("download of %s completed but %s/openvino_model.xml is missing", repo, dir)
	}
	return dir, nil
}
