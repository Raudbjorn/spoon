//go:build !windows

package github

import (
	"os"
	"syscall"
)

// secureCacheFile reports whether path is a regular file owned by this user
// with no group- or other-write bit. It gates inputs that steer network
// traffic, where a locally writable file is a traffic-interception primitive.
func secureCacheFile(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if info.Mode().Perm()&0o022 != 0 {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return !ok || int(st.Uid) == os.Getuid()
}
