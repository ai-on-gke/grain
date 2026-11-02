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

// Package hostd is "grain hostd": a small process server that runs on the
// bare machine, as the user who installed grain, and starts only the
// programs that machine's own allowlist names, on behalf of a daemon that
// runs in the release container.
//
// It exists for the few things that cannot run inside the daemon's
// container -- an agent CLI that must run under the host's corp identity
// (Jetski, "agy"), or a stdio capability server that needs host-only
// tooling -- without giving the container a general way to run anything
// on the host. The shape is deliberately local and small:
//
//   - one unix socket, mode 0600, owned by the installing user and bind
//     mounted into the daemon's container; no TCP, no remote transport;
//   - one allowlist on the host (/etc/grain/hostd.yaml by default), which
//     the installer writes, the operator may edit, and the container never
//     sees; hostd re-reads it on every request;
//   - one connection per process: the daemon asks for an entry by name with
//     an argv, an environment and a working directory, each of which is
//     checked against the entry, and then streams the process's stdio over
//     the same connection until it exits.
//
// The one addition is grain's own: each capabilities entry hostd's build
// ships for its own servers (Server.Builtins) that the file lacks is
// offered as hostd's own root-owned binary, exactly as the installer would
// write it there. Anything else not on the allowlist, or not accepted by
// its entry, is refused and logged. Nothing here runs as root, and nothing here forwards the
// daemon's own environment: a child's environment is built from the entry.
// design/design/cli-run-location.design.md ("Host CLIs through grain hostd") is the
// design and its security argument.
package hostd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/safepath"
)

// DefaultAllowlistPath is where the installer writes the allowlist.
const DefaultAllowlistPath = "/etc/grain/hostd.yaml"

// DefaultSocketPath is the socket hostd listens on unless told otherwise:
// the systemd unit's RuntimeDirectory, bind mounted into the daemon's
// container at the same path.
const DefaultSocketPath = "/run/grain-hostd/hostd.sock"

// DefaultMaxDuration is the ceiling on a process's life when its entry
// names none. hostd enforces it itself, whatever the daemon asks for.
const DefaultMaxDuration = 24 * time.Hour

// Kind says what an allowlist entry is for.
type Kind string

const (
	// KindCLI is an agent CLI: the daemon supplies the argv it builds for
	// the run (prompt, flags, staged config paths).
	KindCLI Kind = "cli"
	// KindCapability is a stdio capability server: its command and args are
	// fixed by the entry, and the daemon only carries its stdio.
	KindCapability Kind = "capability"
)

// Allowlist is the parsed hostd.yaml.
//
//	clis:
//	  agy:
//	    path: /opt/agy/bin/agy
//	    paths_under: [/var/lib/grain]
//	    cwd_under: [/var/lib/grain]
//	    env: [HOME, GRAIN_*]
//	    pass_env: [HTTPS_PROXY, NO_PROXY]
//	    max_duration: 12h
//	capabilities:
//	  corp-search:
//	    command: /usr/local/bin/corp-search-mcp
//	    args: [--stdio]
//	    set_env: {CORP_SEARCH_TOKEN_FILE: /home/me/.corp-search-token}
type Allowlist struct {
	CLIs         map[string]*Entry `yaml:"clis"`
	Capabilities map[string]*Entry `yaml:"capabilities"`
	// DrainCap bounds how long a drain for an upgrade waits on processes
	// still running (Server.Drain); DefaultDrainCap when unset. At the cap
	// they are cancelled.
	DrainCap time.Duration `yaml:"drain_cap"`
}

// DefaultDrainCap is the allowlist's drain_cap when it sets none.
const DefaultDrainCap = time.Hour

// drainCap is a's DrainCap, or DefaultDrainCap.
func (a *Allowlist) drainCap() time.Duration {
	if a == nil || a.DrainCap <= 0 {
		return DefaultDrainCap
	}
	return a.DrainCap
}

// Entry is one program hostd may start, and everything about a request
// for it that hostd will accept.
type Entry struct {
	// Path (clis) or Command (capabilities) is the absolute path of the
	// binary. The request never names a binary; it names the entry.
	Path    string `yaml:"path"`
	Command string `yaml:"command"`
	// Args are prepended to every invocation. For a capability they are
	// the whole argv.
	Args []string `yaml:"args"`
	// ExtraArgs says whether the daemon may append arguments of its own.
	// Unset means yes for a CLI (its argv is the run's) and no for a
	// capability.
	ExtraArgs *bool `yaml:"extra_args"`
	// PathsUnder are the directories every path-like argument the daemon
	// supplies must sit under: the run's working directory, staging dir and
	// CLI home, which exist at the same paths on the host and in the
	// container. A path-like argument when this is empty is refused, and
	// so is one that does not exist yet or is itself a symlink; the child
	// is given the path as it resolved, not as the daemon sent it.
	PathsUnder []string `yaml:"paths_under"`
	// CwdUnder are the directories the requested working directory must sit
	// under. Empty means the daemon may not choose one; the process starts
	// in "/".
	CwdUnder []string `yaml:"cwd_under"`
	// Env are the variable names the daemon may set, exactly or as a
	// "PREFIX_*" pattern. Any other name in a request refuses it, and so
	// does a name set_env or pass_env already supplies. PATH, HOME, the
	// other identity variables and LD_* are admitted only by their exact
	// name, never by a pattern; listing one hands the daemon what the child
	// runs, loads or reads its config from (design/design/cli-run-location.design.md).
	// A value that looks like a path must sit under PathsUnder.
	Env []string `yaml:"env"`
	// PassEnv are names copied from hostd's own environment -- the corp
	// proxy, a credential cache -- into this entry's processes only.
	PassEnv []string `yaml:"pass_env"`
	// SetEnv are fixed values set for this entry's processes only. This is
	// where an entry's credentials go, by file path rather than value.
	SetEnv map[string]string `yaml:"set_env"`
	// MaxDuration caps a process's life; DefaultMaxDuration when unset.
	MaxDuration time.Duration `yaml:"max_duration"`
}

// LoadAllowlist reads and validates the allowlist at path, after checking
// that nobody but owners can change it: it must be a regular file owned by
// one of owners and writable by nobody else. owners is root alone in
// production (the installer writes it with sudo), so a daemon running as
// the same user as hostd still cannot rewrite what hostd will run.
func LoadAllowlist(path string, owners []uint32) (*Allowlist, error) {
	if err := checkOwnedBy(path, owners); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseAllowlist(data)
}

// ParseAllowlist parses and validates an allowlist.
func ParseAllowlist(data []byte) (*Allowlist, error) {
	var a Allowlist
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	// An empty file is an empty allowlist, which refuses everything.
	if err := dec.Decode(&a); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("hostd allowlist: %w", err)
	}
	for name, e := range a.CLIs {
		if err := e.validate(KindCLI, name); err != nil {
			return nil, err
		}
	}
	for name, e := range a.Capabilities {
		if err := e.validate(KindCapability, name); err != nil {
			return nil, err
		}
	}
	return &a, nil
}

func (e *Entry) validate(kind Kind, name string) error {
	where := fmt.Sprintf("hostd allowlist: %s %q", kind, name)
	if e == nil {
		return fmt.Errorf("%s: empty entry", where)
	}
	if !validName(name) {
		return fmt.Errorf("%s: names are letters, digits, '-', '_' and '.'", where)
	}
	bin := e.binary(kind)
	switch kind {
	case KindCLI:
		if e.Command != "" {
			return fmt.Errorf("%s: a cli names its binary as path:, not command:", where)
		}
	case KindCapability:
		if e.Path != "" {
			return fmt.Errorf("%s: a capability names its binary as command:, not path:", where)
		}
	}
	if bin == "" || !filepath.IsAbs(bin) || filepath.Clean(bin) != bin {
		return fmt.Errorf("%s: the binary must be a clean absolute path, got %q", where, bin)
	}
	for _, list := range [][]string{e.PathsUnder, e.CwdUnder} {
		for _, d := range list {
			if !filepath.IsAbs(d) || filepath.Clean(d) != d || d == "/" {
				return fmt.Errorf("%s: %q must be a clean absolute directory other than /", where, d)
			}
		}
	}
	for _, n := range e.Env {
		if !validEnvPattern(n) {
			return fmt.Errorf("%s: env %q is not a variable name or NAME_* pattern", where, n)
		}
	}
	for _, n := range e.PassEnv {
		if !validEnvName(n) {
			return fmt.Errorf("%s: pass_env %q is not a variable name", where, n)
		}
	}
	for n := range e.SetEnv {
		if !validEnvName(n) {
			return fmt.Errorf("%s: set_env %q is not a variable name", where, n)
		}
	}
	if e.MaxDuration < 0 {
		return fmt.Errorf("%s: max_duration is negative", where)
	}
	return nil
}

func (e *Entry) binary(kind Kind) string {
	if kind == KindCapability {
		return e.Command
	}
	return e.Path
}

func (e *Entry) extraArgs(kind Kind) bool {
	if e.ExtraArgs != nil {
		return *e.ExtraArgs
	}
	return kind == KindCLI
}

func (e *Entry) maxDuration() time.Duration {
	if e.MaxDuration > 0 {
		return e.MaxDuration
	}
	return DefaultMaxDuration
}

// Lookup returns the entry kind/name names, or nil.
func (a *Allowlist) Lookup(kind Kind, name string) *Entry {
	if a == nil {
		return nil
	}
	switch kind {
	case KindCLI:
		return a.CLIs[name]
	case KindCapability:
		return a.Capabilities[name]
	}
	return nil
}

// Offer is one allowlist entry as the daemon is told about it: enough for
// Settings to say what can run on the host, and nothing about how.
type Offer struct {
	Kind Kind   `json:"kind"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// Offers lists every entry, sorted.
func (a *Allowlist) Offers() []Offer {
	var out []Offer
	if a == nil {
		return out
	}
	for n, e := range a.CLIs {
		out = append(out, Offer{Kind: KindCLI, Name: n, Path: e.Path})
	}
	for n, e := range a.Capabilities {
		out = append(out, Offer{Kind: KindCapability, Name: n, Path: e.Command})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Spec is a request that has passed its entry's checks: exactly what hostd
// will exec.
type Spec struct {
	Path        string
	Args        []string
	Env         []string
	Dir         string
	MaxDuration time.Duration
}

// RefusedError is a request the allowlist does not accept. Its message is
// what hostd logs and what the daemon is told.
type RefusedError struct {
	Reason string
	// Draining is a draining hostd's refusal of a new start: the hostd
	// replacing it will take the start (IsDraining).
	Draining bool
}

// IsDraining reports whether err is a draining hostd's refusal.
func IsDraining(err error) bool {
	var r *RefusedError
	return errors.As(err, &r) && r.Draining
}

func (e *RefusedError) Error() string { return "hostd refused: " + e.Reason }

func refused(format string, args ...any) error {
	return &RefusedError{Reason: fmt.Sprintf(format, args...)}
}

// Check resolves a start request against the allowlist. hostEnv is hostd's
// own environment (os.Environ()), from which only PATH, the identity
// variables and the entry's pass_env are taken.
func (a *Allowlist) Check(req *Request, hostEnv []string) (*Spec, error) {
	kind := req.Kind
	e := a.Lookup(kind, req.Name)
	if e == nil {
		return nil, refused("%s %q is not on this host's allowlist", kind, req.Name)
	}
	where := fmt.Sprintf("%s %q", kind, req.Name)
	if len(req.Args) > 0 && !e.extraArgs(kind) {
		return nil, refused("%s takes no arguments from the daemon", where)
	}
	// The child is given the paths that were checked, resolved through
	// every symlink, not the ones the daemon sent: a name the daemon could
	// point somewhere else after the check is not passed on.
	args := append([]string(nil), e.Args...)
	for i, arg := range req.Args {
		checked, err := checkArg(arg, e.PathsUnder)
		if err != nil {
			return nil, refused("%s: argument %d: %v", where, i, err)
		}
		args = append(args, checked)
	}
	dir := "/"
	if req.Dir != "" {
		resolved, err := checkPathUnder(req.Dir, e.CwdUnder)
		if err != nil {
			return nil, refused("%s: working directory: %v", where, err)
		}
		dir = resolved
	}
	env, err := e.environment(req.Kind, req.Env, hostEnv)
	if err != nil {
		return nil, refused("%s: %v", where, err)
	}
	return &Spec{
		Path:        e.binary(kind),
		Args:        args,
		Env:         env,
		Dir:         dir,
		MaxDuration: e.maxDuration(),
	}, nil
}

// identityEnv are taken from hostd's own environment for every entry:
// where to find binaries and who the process is, none of which is a
// credential.
var identityEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "TZ"}

// ProbeEnv is the variable the daemon sets to "1" when it starts a stdio
// capability server only to probe it (Settings' Test); protocol.ProbeEnv
// is this name. Every capability entry accepts it without listing it in
// env: it changes nothing but whether the server mints, and an allowlist
// written before probes existed would otherwise refuse every Test.
const ProbeEnv = "GRAIN_CAPABILITY_PROBE"

func (e *Entry) environment(kind Kind, requested, hostEnv []string) ([]string, error) {
	vals := map[string]string{}
	var order []string
	set := func(k, v string) {
		if _, ok := vals[k]; !ok {
			order = append(order, k)
		}
		vals[k] = v
	}
	host := map[string]string{}
	for _, kv := range hostEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			host[k] = v
		}
	}
	for _, k := range append(append([]string(nil), identityEnv...), e.PassEnv...) {
		if v, ok := host[k]; ok {
			set(k, v)
		}
	}
	keys := make([]string, 0, len(e.SetEnv))
	for k := range e.SetEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		set(k, e.SetEnv[k])
	}
	for _, kv := range requested {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !validEnvName(k) {
			return nil, fmt.Errorf("environment entry %q is not NAME=value", redactEnv(kv))
		}
		if strings.ContainsRune(v, 0) {
			return nil, fmt.Errorf("environment variable %s carries a NUL byte", k)
		}
		if !e.acceptsEnv(kind, k) {
			return nil, fmt.Errorf("environment variable %s is not one this entry accepts", k)
		}
		if e.providesEnv(k) {
			return nil, fmt.Errorf("environment variable %s is this entry's own (set_env or pass_env), which the daemon may not override", k)
		}
		checked, err := checkEnvValue(v, e.PathsUnder)
		if err != nil {
			return nil, fmt.Errorf("environment variable %s: %v", k, err)
		}
		set(k, checked)
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vals[k])
	}
	return out, nil
}

// redactEnv keeps a refused variable's name and drops its value, which may
// be a credential, out of hostd's log.
func redactEnv(kv string) string {
	if k, _, ok := strings.Cut(kv, "="); ok {
		return k + "=…"
	}
	if len(kv) > 64 {
		return kv[:64] + "…"
	}
	return kv
}

// acceptsEnv reports whether the daemon may set name. hostd's identity
// variables and the dynamic loader's LD_* change what the child is and
// what it loads, so only an exact entry in env admits one: a NAME_*
// pattern never does. agy's per-run HOME is the case that needs this.
// A capability entry also accepts ProbeEnv.
func (e *Entry) acceptsEnv(kind Kind, name string) bool {
	if kind == KindCapability && name == ProbeEnv {
		return true
	}
	if slices.Contains(identityEnv, name) || strings.HasPrefix(name, "LD_") {
		return slices.Contains(e.Env, name)
	}
	return matchesAny(name, e.Env)
}

// providesEnv reports whether the entry itself supplies name, through
// set_env or pass_env: a value the operator chose is not the daemon's to
// replace.
func (e *Entry) providesEnv(name string) bool {
	_, set := e.SetEnv[name]
	return set || slices.Contains(e.PassEnv, name)
}

// checkEnvValue applies an entry's paths_under to a value that looks like
// a path ("/x", "./x", "../x", "~x", "." or "..") or a list of them
// ("/a:/b"), and refuses a ".." component anywhere in one. Other values --
// a flag, a key, a URL -- pass. It returns the value to set: v with each
// path in it replaced by what it resolves to.
func checkEnvValue(v string, roots []string) (string, error) {
	if !(v == "." || v == ".." || strings.HasPrefix(v, "/") || strings.HasPrefix(v, "./") ||
		strings.HasPrefix(v, "../") || strings.HasPrefix(v, "~")) {
		if hasDotDot(v) && strings.ContainsRune(v, '/') {
			return "", errors.New("the value climbs out with ..")
		}
		return v, nil
	}
	// The value itself stays out of the refusal: it may be a credential.
	parts := strings.Split(v, ":")
	for i, p := range parts {
		checked, err := checkPathPart(p, roots)
		if err != nil {
			return "", errors.New("the value is a path that is not an existing file or directory under this entry's paths_under")
		}
		parts[i] = checked
	}
	return strings.Join(parts, ":"), nil
}

func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if prefix, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		} else if p == name {
			return true
		}
	}
	return false
}

// checkArg refuses a NUL byte anywhere, a ".." path component anywhere,
// and any part of the argument that is path-like (see pathLike) and is not
// a clean absolute path under one of roots. The parts are the argument
// itself, or, for "--flag=value", "name=value" and a short option with its
// value attached ("-o/etc/passwd", "-o=x"), the name and the value apart.
// Neither agent CLI grain drives takes its prompt in argv (both read it
// from stdin), so no free text has to pass here. It returns the argument
// to pass: arg with each path part replaced by what it resolves to.
func checkArg(arg string, roots []string) (string, error) {
	if strings.ContainsRune(arg, 0) {
		return "", errors.New("carries a NUL byte")
	}
	if hasDotDot(arg) {
		return "", fmt.Errorf("%q climbs out with ..", arg)
	}
	parts, sep := argParts(arg)
	for i, part := range parts {
		checked, err := checkPathPart(part, roots)
		if err != nil {
			return "", err
		}
		parts[i] = checked
	}
	return strings.Join(parts, sep), nil
}

// argParts splits arg into what a CLI might read as a path: an option's
// name and its attached value, or the argument whole. Joining the parts
// with sep gives arg back.
func argParts(arg string) (parts []string, sep string) {
	if !strings.HasPrefix(arg, "--") && strings.HasPrefix(arg, "-") && len(arg) > 2 {
		// "-o/etc/passwd" and "-o=/etc/passwd" are both -o's value.
		if v, ok := strings.CutPrefix(arg[2:], "="); ok {
			return []string{arg[:2], v}, "="
		}
		return []string{arg[:2], arg[2:]}, ""
	}
	if k, v, ok := strings.Cut(arg, "="); ok {
		return []string{k, v}, "="
	}
	return []string{arg}, ""
}

// checkPathPart refuses a "~" or "~user" form outright, and a path-like s
// that is not a clean absolute path under one of roots, and returns s, or
// what s resolves to when it is a path.
func checkPathPart(s string, roots []string) (string, error) {
	if strings.HasPrefix(s, "~") {
		return "", fmt.Errorf("%q names a home directory with ~", s)
	}
	if pathLike(s) {
		return checkPathUnder(s, roots)
	}
	return s, nil
}

// pathLike is anything a program could open as a path other than a bare
// file name in its working directory: "." and "..", and anything with a
// "/" in it, wherever the "/" sits.
func pathLike(s string) bool {
	return s == "." || s == ".." || strings.ContainsRune(s, '/')
}

// hasDotDot reports whether s has a ".." path component wherever it sits:
// "sub/../../etc", "--x=..", "a:../b".
func hasDotDot(s string) bool {
	return slices.Contains(strings.FieldsFunc(s, func(r rune) bool {
		return r == '/' || r == '=' || r == ':' || r == ','
	}), "..")
}

// checkPathUnder refuses p unless it is absolute, has no ".." component,
// exists, is not itself a symlink (unless it is one of roots, which the
// operator named), and resolves inside one of roots, and returns what it
// resolves to. Callers pass on the resolved path, never p.
//
// A path that does not exist yet is refused: safepath.Within accepts the
// missing tail as it stands, and the daemon, which writes under roots,
// could create it as a symlink to anywhere after the check. A final
// component that is a link is refused for the same reason: the daemon
// planted it, and nothing the child should be handed needs one.
func checkPathUnder(p string, roots []string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q is not an absolute path", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", fmt.Errorf("%q climbs out with ..", p)
		}
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("%q: this entry accepts no paths", p)
	}
	if fi, err := os.Lstat(p); err != nil {
		return "", fmt.Errorf("%q does not exist", p)
	} else if fi.Mode()&os.ModeSymlink != 0 && !slices.Contains(roots, filepath.Clean(p)) {
		return "", fmt.Errorf("%q is a symlink", p)
	}
	// Within resolves symlinks on both sides, so a link planted under a
	// root that points out of it does not pass.
	for _, r := range roots {
		resolved, err := safepath.Within(r, p)
		if err != nil {
			continue
		}
		// Within lets components that do not exist through unresolved;
		// the Lstat above saw p, so this catches one removed since.
		if _, err := os.Lstat(resolved); err != nil {
			return "", fmt.Errorf("%q does not exist", p)
		}
		return resolved, nil
	}
	return "", fmt.Errorf("%q is not under %s", p, strings.Join(roots, ", "))
}

func validName(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func validEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func validEnvPattern(s string) bool {
	if p, ok := strings.CutSuffix(s, "*"); ok {
		return validEnvName(p)
	}
	return validEnvName(s)
}
