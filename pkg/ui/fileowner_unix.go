//go:build unix

package ui

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// FileOwner is the account that owns path, for a `sudo -u` that names
// it. A file this process cannot even stat -- one inside a directory it
// may not search -- is named by its directory's owner instead; "grain",
// the account scripts/setup.sh creates, when neither can be told.
// google/scripts/cloudtop-install.sh runs grain as whoever installed it, so the
// owner is the only account name a hint can trust.
func FileOwner(path string) string {
	for _, p := range []string{path, filepath.Dir(path)} {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			break
		}
		if u, err := user.LookupId(strconv.FormatUint(uint64(st.Uid), 10)); err == nil {
			return u.Username
		}
		break
	}
	return "grain"
}
