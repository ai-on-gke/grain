//go:build !unix

package gitproxy

import "os"

// ownedByUs has no owner to compare off unix, so it never vouches for a
// directory.
func ownedByUs(os.FileInfo) bool { return false }
