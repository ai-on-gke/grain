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

package hostd

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// DefaultGrainCommand is where the installer puts the grain binary on the
// host (scripts/setup.sh's HOST_GRAIN_BIN): the command every built-in
// entry runs.
const DefaultGrainCommand = "/usr/local/bin/grain"

// ProxyVars are the corp proxy variables a built-in entry passes from
// hostd's environment (scripts/setup.sh's HOSTD_PROXY_VARS).
var ProxyVars = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"}

// BuiltinEntry is one capabilities entry grain itself ships in the
// allowlist, run as `<grain> <args...>`: one of its own capability
// servers, or another grain subcommand an edition registers
// (intree.RegisterHostdEntry). The list a build carries
// (intree.HostdBuiltins: core, plus what its edition registers) is the
// one source of truth both scripts/setup.sh's heredocs (a test holds them
// equal; CheckInstallerEntries) and EnsureBuiltins write from.
type BuiltinEntry struct {
	Name string
	Args []string
	// ExtraArgs entries take the daemon's -listen, and admit
	// GRAIN_CAPABILITY_PROBE so a probe binds an ephemeral port.
	ExtraArgs bool
	// Env are further variable names the daemon may set, after
	// GRAIN_CAPABILITY_PROBE when ExtraArgs is set.
	Env []string
	// PassEnv, when set, replaces ProxyVars as what the entry passes
	// from hostd's environment.
	PassEnv []string
	// MaxDuration, when set, is the entry's max_duration: the cap on
	// one process's life (DefaultMaxDuration when unset).
	MaxDuration time.Duration
}

// env is the entry's env: list.
func (b BuiltinEntry) env() []string {
	var out []string
	if b.ExtraArgs {
		out = append(out, "GRAIN_CAPABILITY_PROBE")
	}
	return append(out, b.Env...)
}

// passEnv is the entry's pass_env: list.
func (b BuiltinEntry) passEnv() []string {
	if b.PassEnv != nil {
		return slices.Clone(b.PassEnv)
	}
	return slices.Clone(ProxyVars)
}

// Entry is b as an allowlist entry running command.
func (b BuiltinEntry) Entry(command string) *Entry {
	e := &Entry{Command: command, Args: slices.Clone(b.Args), Env: b.env(), PassEnv: b.passEnv(), MaxDuration: b.MaxDuration}
	if b.ExtraArgs {
		yes := true
		e.ExtraArgs = &yes
	}
	return e
}

// formatDuration is d as an allowlist writes it: 1m rather than 1m0s.
func formatDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// YAML is b's entry as the installer writes it: the name at indent, its
// fields at indent+step.
func (b BuiltinEntry) YAML(command, indent, step string) string {
	var s strings.Builder
	in := indent + step
	fmt.Fprintf(&s, "%s%s:\n", indent, b.Name)
	fmt.Fprintf(&s, "%scommand: %s\n", in, command)
	fmt.Fprintf(&s, "%sargs: [%s]\n", in, strings.Join(b.Args, ", "))
	if b.ExtraArgs {
		fmt.Fprintf(&s, "%sextra_args: true\n", in)
	}
	if env := b.env(); len(env) > 0 {
		fmt.Fprintf(&s, "%senv: [%s]\n", in, strings.Join(env, ", "))
	}
	fmt.Fprintf(&s, "%spass_env: [%s]\n", in, strings.Join(b.passEnv(), ", "))
	if b.MaxDuration > 0 {
		fmt.Fprintf(&s, "%smax_duration: %s\n", in, formatDuration(b.MaxDuration))
	}
	return s.String()
}

// EnsureResult is what EnsureBuiltins did to an allowlist.
type EnsureResult struct {
	// Added are the entries it wrote, by name.
	Added []string
	// Differs are entries the operator has that differ from grain's
	// default, one line each saying how. They are left as they are.
	Differs []string
	// Manual, when set, is the text an operator has to add under
	// capabilities: by hand: the file's shape is one EnsureBuiltins does
	// not edit (capabilities written as a flow mapping), and nothing was
	// written.
	Manual string
}

// differences says how have differs from want, the fields grain's own
// entry sets; "" when it does not.
func differences(have, want *Entry) string {
	var out []string
	if have.Command != want.Command {
		out = append(out, fmt.Sprintf("command %q (default %q)", have.Command, want.Command))
	}
	if !slices.Equal(have.Args, want.Args) {
		out = append(out, fmt.Sprintf("args %v (default %v)", have.Args, want.Args))
	}
	if (have.ExtraArgs != nil && *have.ExtraArgs) != (want.ExtraArgs != nil && *want.ExtraArgs) {
		out = append(out, fmt.Sprintf("extra_args %v (default %v)", have.ExtraArgs != nil && *have.ExtraArgs, want.ExtraArgs != nil && *want.ExtraArgs))
	}
	if !slices.Equal(have.Env, want.Env) {
		out = append(out, fmt.Sprintf("env %v (default %v)", have.Env, want.Env))
	}
	if !slices.Equal(have.PassEnv, want.PassEnv) {
		out = append(out, fmt.Sprintf("pass_env %v (default %v)", have.PassEnv, want.PassEnv))
	}
	if have.maxDuration() != want.maxDuration() {
		out = append(out, fmt.Sprintf("max_duration %v (default %v)", have.maxDuration(), want.maxDuration()))
	}
	return strings.Join(out, ", ")
}

// builtins is the build's own entries (intree.HostdBuiltins, which sets
// it): what MissingEntryHelp knows grain adds by itself.
var builtins func() []BuiltinEntry

// SetBuiltins names the function listing this build's own entries.
func SetBuiltins(f func() []BuiltinEntry) { builtins = f }

// Builtin is this build's own entry called name, if it is one.
func Builtin(name string) (BuiltinEntry, bool) {
	if builtins == nil {
		return BuiltinEntry{}, false
	}
	for _, b := range builtins() {
		if b.Name == name {
			return b, true
		}
	}
	return BuiltinEntry{}, false
}

// EnsureCommand is what an operator runs on the host to add grain's own
// entries now rather than at hostd's next start.
const EnsureCommand = "sudo systemctl start grain-hostd-allowlist.service"

// MissingEntryHelp says what happens about a capabilities entry name that
// hostd's allowlist lacks: grain adds its own (EnsureBuiltins, at hostd's
// next start or upgrade), and the text to add by hand; any other is the
// operator's to add.
func MissingEntryHelp(name string) string {
	b, ok := Builtin(name)
	if !ok {
		return fmt.Sprintf("add an entry %q under capabilities: in %s on the host, or run the server in the container", name, DefaultAllowlistPath)
	}
	return fmt.Sprintf("%q is grain's own entry, which grain adds to the allowlist by itself the next time grain hostd starts or is upgraded "+
		"(grain-hostd-allowlist.service, written by this release's setup.sh). To add it now, run %s on the host -- hostd re-reads the allowlist, "+
		"so nothing restarts and no run is touched; on an install whose setup.sh predates that service, re-run setup.sh once. "+
		"Or add it by hand under capabilities: in %s:\n%s", name, EnsureCommand, DefaultAllowlistPath, b.YAML(DefaultGrainCommand, "  ", "  "))
}

// withBuiltins gives a each entry of builtins it has no capabilities
// entry of that name for, running command, and says which it gave. An
// entry the file has is never replaced, however it differs: the
// operator's entry is the one hostd runs.
func (a *Allowlist) withBuiltins(builtins []BuiltinEntry, command string) []string {
	var added []string
	for _, b := range builtins {
		if _, ok := a.Capabilities[b.Name]; ok {
			continue
		}
		if a.Capabilities == nil {
			a.Capabilities = map[string]*Entry{}
		}
		a.Capabilities[b.Name] = b.Entry(command)
		added = append(added, b.Name)
	}
	return added
}

// CheckInstallerEntries holds an installer's allowlist text (what
// scripts/setup.sh, or an edition's installer, writes) to builtins, the
// list the binary's -ensure-builtins adds from: each entry must be there,
// parse to exactly b.Entry(command), and be written as b.YAML writes it,
// so what an operator is told to copy is the same whichever wrote it. It
// returns one line per disagreement; none when they agree. core's
// tests/deploy runs it against scripts/setup.sh, and an edition that
// registers its own entries (intree.RegisterHostdEntry) runs it against
// its own installer.
func CheckInstallerEntries(content, command string, builtins []BuiltinEntry) []string {
	a, err := ParseAllowlist([]byte(content))
	if err != nil {
		return []string{fmt.Sprintf("hostd refuses the installer's allowlist: %v", err)}
	}
	var out []string
	for _, b := range builtins {
		have, want := a.Capabilities[b.Name], b.Entry(command)
		switch {
		case have == nil:
			out = append(out, fmt.Sprintf("%s: the installer writes no entry; the binary adds %+v", b.Name, want))
			continue
		case !reflect.DeepEqual(have, want):
			out = append(out, fmt.Sprintf("%s: the installer writes %+v, the binary %+v", b.Name, have, want))
		}
		if text := b.YAML(command, "  ", "  "); !strings.Contains(content, text) {
			out = append(out, fmt.Sprintf("%s: the installer's text differs from the binary's, which is:\n%s", b.Name, text))
		}
	}
	return out
}
