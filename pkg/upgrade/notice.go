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

package upgrade

import (
	"sort"
	"sync"
)

// Notice is something about how this deployment upgrades that its
// operator should fix, shown on the Upgrade pane (GET /api/upgrade's
// notices). Core raises none of its own: an edition registers a source
// of them with RegisterNotice -- say, a unit file whose -upgrade-image
// flag names a repository the edition no longer publishes to, healed at
// start but worth removing. Its shape follows ui.InstallNotice.
type Notice struct {
	// Kind names the case, so a test or a client can tell notices apart.
	Kind string `json:"kind"`
	// Message is the notice itself.
	Message string `json:"message"`
	// Command is what fixes it, if one command does.
	Command string `json:"command,omitempty"`
	// DocsURL is where the fix is written up.
	DocsURL string `json:"docsUrl,omitempty"`
}

var (
	noticesMu      sync.Mutex
	noticeSources  = map[int]func() []Notice{}
	nextNoticeSlot int
)

// RegisterNotice adds a source of Upgrade pane notices, called on every
// GET /api/upgrade so it can answer from the deployment's state at the
// time. The returned func removes it again (for tests).
func RegisterNotice(source func() []Notice) (unregister func()) {
	noticesMu.Lock()
	defer noticesMu.Unlock()
	slot := nextNoticeSlot
	nextNoticeSlot++
	noticeSources[slot] = source
	return func() {
		noticesMu.Lock()
		defer noticesMu.Unlock()
		delete(noticeSources, slot)
	}
}

// Notices returns every registered source's notices, in the order the
// sources were registered, skipping any without a message.
func Notices() []Notice {
	noticesMu.Lock()
	slots := make([]int, 0, len(noticeSources))
	for slot := range noticeSources {
		slots = append(slots, slot)
	}
	sources := make([]func() []Notice, 0, len(slots))
	sort.Ints(slots)
	for _, slot := range slots {
		sources = append(sources, noticeSources[slot])
	}
	noticesMu.Unlock()

	var out []Notice
	for _, source := range sources {
		for _, n := range source() {
			if n.Message != "" {
				out = append(out, n)
			}
		}
	}
	return out
}
