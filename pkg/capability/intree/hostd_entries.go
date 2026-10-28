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

package intree

import (
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// init hands hostd this build's own entries, for MissingEntryHelp.
func init() { hostd.SetBuiltins(HostdBuiltins) }

// HostdBuiltins are the grain hostd capabilities entries this build of
// grain ships: host-git, and HostEntry(name) for every server marked Host
// -- core's, plus what the edition linked in registers (an edition's
// own servers). It is the one list scripts/setup.sh's entries are held
// to (tests/deploy) and `grain hostd -ensure-builtins` adds from, so an
// installer and an upgrade cannot disagree about what grain's own
// entries say.
func HostdBuiltins() []hostd.BuiltinEntry {
	out := []hostd.BuiltinEntry{{Name: gitproxy.HostGitEntry, Args: []string{"capability", gitproxy.HostGitEntry}}}
	for _, name := range Names() {
		b := builders[name]
		if b.Internal || !b.Host {
			continue
		}
		out = append(out, hostd.BuiltinEntry{
			Name: HostEntry(name),
			Args: []string{"capability", name},
			// A server whose entry passes a -listen (its data plane) takes
			// the daemon's, and a probe's ephemeral one.
			ExtraArgs: len(b.Args) > 0 || len(b.HostArgs) > 0,
		})
	}
	return out
}
