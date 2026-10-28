//go:build unix

package gitproxy

import (
	"os"
	"syscall"
)

// ownedByUs reports whether fi belongs to the uid this process runs as.
func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
