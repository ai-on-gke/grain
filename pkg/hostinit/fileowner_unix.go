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

package hostinit

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// nonRootOwner returns the owning account's name (or "uid <n>") when fi
// is owned by a non-root Unix UID, and "" when owned by root (UID 0) or
// when the owner cannot be read.
func nonRootOwner(fi os.FileInfo) string {
	if fi == nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return nonRootUIDName(st.Uid)
}

func nonRootUIDName(uid uint32) string {
	if uid == 0 {
		return ""
	}
	if u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10)); err == nil && u.Username != "" {
		return u.Username
	}
	return fmt.Sprintf("uid %d", uid)
}
