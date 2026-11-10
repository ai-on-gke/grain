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

package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ai-on-gke/grain/pkg/hubenv"
)

// renamedFlag is a flag's former name, still accepted after the flag was
// renamed: the Grain Registry became the Grain Hub, and -registry-url
// became -hub-url, but a systemd unit, a script or a k8s manifest written
// before that still says -registry-url. The old name is defined beside
// the new one, holds what it was given without applying it, and
// resolveRenamedFlags applies it after parsing -- unless the new name was
// given too, which wins whichever came first on the command line.
type renamedFlag struct {
	old, new string
	isBool   bool
	raw      string
	set      bool
}

func (f *renamedFlag) String() string {
	if f == nil {
		return ""
	}
	return f.raw
}

func (f *renamedFlag) Set(v string) error {
	f.raw, f.set = v, true
	return nil
}

func (f *renamedFlag) IsBoolFlag() bool { return f != nil && f.isBool }

// renamedFlags is every former name defined on one FlagSet.
type renamedFlags struct {
	fs    *flag.FlagSet
	flags []*renamedFlag
}

// formerHubFlagName is the name a flag renamed with the Grain Hub
// (formerly the Grain Registry) had before: "hub-url" was "registry-url",
// and "ui-hub-issuer" was "ui-registry-issuer".
func formerHubFlagName(name string) string {
	return strings.Replace(name, "hub", "registry", 1)
}

// defineRenamedFlags defines, on fs, the former "registry" name of every
// flag in names (each already defined under its new "hub" name), so
// either spelling parses. Call resolve after fs.Parse.
func defineRenamedFlags(fs *flag.FlagSet, names ...string) *renamedFlags {
	r := &renamedFlags{fs: fs}
	for _, name := range names {
		target := fs.Lookup(name)
		if target == nil {
			panic("defineRenamedFlags: no flag -" + name)
		}
		old := formerHubFlagName(name)
		f := &renamedFlag{old: old, new: name}
		if b, ok := target.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			f.isBool = true
		}
		fs.Var(f, old, "deprecated: renamed to -"+name)
		r.flags = append(r.flags, f)
	}
	return r
}

// resolve applies every former name given on the command line to its new
// one -- unless the new one was given as well -- and warns, once a
// process, that the old spelling is deprecated. A former name applied this
// way counts as the new flag having been passed (fs.Visit reports it), so
// code asking whether -hub-url was given needs no second check.
func (r *renamedFlags) resolve() error {
	if r == nil {
		return nil
	}
	given := map[string]bool{}
	r.fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	for _, f := range r.flags {
		if !f.set {
			continue
		}
		hubenv.Warn("-"+f.old, "-"+f.new)
		if given[f.new] {
			continue
		}
		if err := r.fs.Set(f.new, f.raw); err != nil {
			return err
		}
	}
	return nil
}

// hubDaemonFlagNames are the daemon's own Grain Hub flags, each also
// accepted under its pre-rename -registry-* name.
var hubDaemonFlagNames = []string{
	"hub-url", "hub-entry", "hub-auth-mode", "hub-oidc-audience", "hub-token-file",
	"hub-display-name", "hub-purpose", "hub-host-kind", "hub-listed", "hub-connect",
}

// hubSubcommandFlagNames are the `grain hub` subcommands' own flags that
// were renamed with it: `grain registry list -registry-url` still parses
// as `grain hub list -hub-url`.
var hubSubcommandFlagNames = []string{"hub-url", "hub-token"}

// defineHubSubcommandRenames defines the former name of each of
// hubSubcommandFlagNames fs has, for parseArgs: nil when it has none.
func defineHubSubcommandRenames(fs *flag.FlagSet) *renamedFlags {
	var names []string
	for _, name := range hubSubcommandFlagNames {
		if fs.Lookup(name) != nil && fs.Lookup(formerHubFlagName(name)) == nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	return defineRenamedFlags(fs, names...)
}

// deprecatedCommandAliases are the hiddenCommandAliases that say so on
// stderr when used.
var deprecatedCommandAliases = map[string]bool{"registry": true}

// warnDeprecatedCommand says on stderr that the command word typed, raw,
// is a deprecated spelling of cmd.
func warnDeprecatedCommand(stderr io.Writer, raw, cmd string) {
	if deprecatedCommandAliases[raw] {
		fmt.Fprintf(stderr, "warning: 'grain %s' is deprecated and will be removed in a future release; use 'grain %s' (the Grain Registry is now the Grain Hub)\n", raw, cmd)
	}
}
