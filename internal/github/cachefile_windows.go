//go:build windows

package github

import "os"

// secureCacheFile reports whether path is a regular file. Windows does not
// expose POSIX uid/mode through FileInfo, so ownership is left to the ACLs on
// the enclosing per-user cache directory.
func secureCacheFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
