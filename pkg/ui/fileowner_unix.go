// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

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
