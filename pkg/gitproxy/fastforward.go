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

package gitproxy

import (
	"bufio"
	"errors"
	"io"
)

// PushShowsFastForward reads a git-receive-pack request body from its
// start and reports whether the commits its packfile carries show newTip
// descending from oldTip: the proof of a fast-forward that needs nothing
// from the remote, which an ordinary push always carries, since its pack
// holds every commit from the new tip back to the remote's. A push whose
// pack does not reach oldTip -- a forced update, or one with no pack at
// all because the remote already has newTip -- is not shown to be one.
// Without GitProxy's fallback to the remote's own history this is
// stricter than refuseRefUpdates, and refuses some pushes that are in
// fact fast-forwards; it never admits one that is not.
func PushShowsFastForward(body io.Reader, newTip, oldTip string) (bool, error) {
	r := bufio.NewReaderSize(body, 64<<10)
	if err := skipToPack(r); err != nil {
		if errors.Is(err, errNoPack) {
			return false, nil
		}
		return false, err
	}
	commits, err := readPackStream(r, len(newTip)/2)
	if err != nil {
		return false, err
	}
	return commits.descends(newTip, oldTip), nil
}
