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
	"fmt"
	"slices"

	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/hostd"
)

// init hands hostd this build's own entries, for MissingEntryHelp.
func init() { hostd.SetBuiltins(HostdBuiltins) }

// hostdEntries are the entries RegisterHostdEntry added, in order.
var hostdEntries []hostd.BuiltinEntry

// RegisterHostdEntry adds b to HostdBuiltins: a grain hostd capabilities
// entry this build ships that is not a capability server's -- one that
// runs some other grain subcommand on the host (`<grain> <b.Args...>`),
// with its own Env, PassEnv or MaxDuration. An edition calls it from its
// own init(), as it calls Register, and `grain hostd -ensure-builtins`
// then adds the entry to an allowlist that lacks it on the next hostd
// start or upgrade, as it does grain's other entries; the edition's
// installer writes the same entry, held to it by
// hostd.CheckInstallerEntries.
//
// It panics on an entry with no name or args, or one whose name another
// registered entry has; HostdBuiltins panics on one whose name a
// capability server's entry has (host-git, grain-<name>), since an
// edition's servers may register after it.
func RegisterHostdEntry(b hostd.BuiltinEntry) {
	if b.Name == "" || len(b.Args) == 0 || b.MaxDuration < 0 {
		panic(fmt.Sprintf("intree.RegisterHostdEntry: %+v needs a name, args and a non-negative max duration", b))
	}
	for _, have := range hostdEntries {
		if have.Name == b.Name {
			panic(fmt.Sprintf("intree.RegisterHostdEntry: %q is registered twice", b.Name))
		}
	}
	b.Args, b.Env, b.PassEnv = slices.Clone(b.Args), slices.Clone(b.Env), slices.Clone(b.PassEnv)
	hostdEntries = append(hostdEntries, b)
}

// HostdBuiltins are the grain hostd capabilities entries this build of
// grain ships: host-git, HostEntry(name) for every server marked Host
// -- core's, plus what the edition linked in registers (an edition's
// own servers) -- and every entry RegisterHostdEntry added. It is the
// one list scripts/setup.sh's entries are held to (tests/deploy) and
// `grain hostd -ensure-builtins` adds from, so an installer and an
// upgrade cannot disagree about what grain's own entries say.
func HostdBuiltins() []hostd.BuiltinEntry {
	out := capabilityHostdEntries()
	for _, b := range hostdEntries {
		for _, c := range out {
			if c.Name == b.Name {
				panic(fmt.Sprintf("intree.RegisterHostdEntry: %q is a capability server's hostd entry", b.Name))
			}
		}
		out = append(out, b)
	}
	return out
}

// capabilityHostdEntries are HostdBuiltins' capability servers' entries.
func capabilityHostdEntries() []hostd.BuiltinEntry {
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

// SwapHostdEntriesForTest replaces what RegisterHostdEntry has added
// with entries, for a test standing in for an edition's registration;
// restore puts the build's own back.
func SwapHostdEntriesForTest(entries []hostd.BuiltinEntry) (restore func()) {
	saved := hostdEntries
	hostdEntries = slices.Clone(entries)
	return func() { hostdEntries = saved }
}
