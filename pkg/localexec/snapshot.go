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

// Package localexec is grain's local-scripts executor, ported from Silo
// v2's localexec (Silo design.md §12, "remote agent, narrow local
// reach"): each script a trusted declaration names becomes one MCP tool,
// local_<name>, that runs it on the node host with the agent's arguments
// and no shell in between.
//
// Snapshot records the scripts when a run starts and refuses any that
// another user could have written; every call re-hashes its script and
// refuses one that changed. Check is the same refusals as errors, plus
// best-effort warnings on scripts that turn an argument into code
// (Warnings). Executor serves the tools, with a timeout and an output cap
// on every call, per-script environment (credentials), and an Approver
// asked first for scripts marked approve: true.
//
// docs/local-scripts.md is the design and the declaration's reference.
package localexec

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

// Defaults of Silo design §12.3.
const (
	DefaultTimeout   = 5 * time.Minute
	DefaultOutputCap = 1 << 20
	// KillGrace is how long a timed-out script's process group has
	// between SIGTERM and SIGKILL.
	KillGrace = 5 * time.Second
	// describeLines is how far into a script # grain: lines are read.
	describeLines = 20
)

// Declaration is a local-scripts file: Silo's local: section, with
// per-script credentials beside each script rather than in a separate
// credentials section. Its folder is the directory holding the file, and
// every script must lie inside it.
type Declaration struct {
	Scripts Scripts `yaml:"scripts"`
	// Workdir is where every script runs, relative to the folder; the
	// folder itself by default.
	Workdir string `yaml:"workdir"`
	// Timeout bounds one call; DefaultTimeout when unset.
	Timeout Duration `yaml:"timeout"`
	// OutputCap bounds stdout and stderr each, in bytes;
	// DefaultOutputCap when unset.
	OutputCap int `yaml:"output_cap"`

	// Folder is the directory the declaration was read from.
	Folder string `yaml:"-"`
}

// Scripts says which scripts are served: every executable directly in
// Dir ("scripts" when unset, if the folder has one), and every entry of
// List.
type Scripts struct {
	Dir  string   `yaml:"dir"`
	List []Script `yaml:"list"`
}

// Script is one scripts.list entry.
type Script struct {
	Path        string `yaml:"path"`
	Description string `yaml:"description"`
	// Approve makes each call wait for a person's approval.
	Approve bool     `yaml:"approve"`
	Timeout Duration `yaml:"timeout"`
	// Env delivers credentials to this script alone: environment
	// variable name to the name of the secret holding its value.
	Env map[string]string `yaml:"env"`
}

// Duration is a time.Duration written as Go spells one ("90s", "5m").
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil || v <= 0 {
		return fmt.Errorf("line %d: %q is not a positive duration (such as 90s or 5m)", n.Line, n.Value)
	}
	*d = Duration(v)
	return nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Load reads the declaration at path. Unknown fields are refused.
func Load(path string) (*Declaration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var d Declaration
	if err := dec.Decode(&d); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("local scripts %s: %w", path, err)
	}
	abs, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	d.Folder = abs
	if d.OutputCap < 0 {
		return nil, fmt.Errorf("local scripts %s: output_cap must not be negative", path)
	}
	for _, s := range d.Scripts.List {
		if strings.TrimSpace(s.Path) == "" {
			return nil, fmt.Errorf("local scripts %s: a scripts.list entry has no path", path)
		}
		for name := range s.Env {
			if !envName.MatchString(name) {
				return nil, fmt.Errorf("local scripts %s: script %s: %q is not an environment variable name", path, s.Path, name)
			}
		}
	}
	return &d, nil
}

// Resolved is one script the declaration serves, before its snapshot.
type Resolved struct {
	// Name is the script's file name.
	Name string
	// Tool is the MCP tool that runs it: local_<name>, lowercased, with
	// anything outside [a-z0-9_] turned into _.
	Tool string
	// Path is its absolute path.
	Path string
	// Entry is its scripts.list entry, or nil when it came from the
	// directory only.
	Entry *Script
}

// ToolName is the tool a script named name is served as.
func ToolName(name string) string {
	var b strings.Builder
	b.WriteString("local_")
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Resolve lists the scripts d serves: every executable regular file
// directly in scripts.dir, then every scripts.list entry, a list entry
// replacing a directory script of the same path. Two scripts that would
// be served as the same tool are refused.
func (d *Declaration) Resolve() ([]Resolved, error) {
	byPath := map[string]int{}
	var out []Resolved
	add := func(path string, entry *Script) {
		if i, ok := byPath[path]; ok {
			if entry != nil {
				out[i].Entry = entry
			}
			return
		}
		name := filepath.Base(path)
		byPath[path] = len(out)
		out = append(out, Resolved{Name: name, Tool: ToolName(name), Path: path, Entry: entry})
	}
	dir, defaulted := d.Scripts.Dir, d.Scripts.Dir == ""
	if defaulted {
		dir = "scripts"
	}
	dir = d.abs(dir)
	entries, err := os.ReadDir(dir)
	if err != nil && !(defaulted && errors.Is(err, fs.ErrNotExist)) {
		return nil, fmt.Errorf("scripts.dir: %w", err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		add(filepath.Join(dir, e.Name()), nil)
	}
	for i := range d.Scripts.List {
		add(d.abs(d.Scripts.List[i].Path), &d.Scripts.List[i])
	}
	tools := map[string]string{}
	var errs []error
	for _, r := range out {
		if other, ok := tools[r.Tool]; ok {
			errs = append(errs, fmt.Errorf("scripts %s and %s would both be served as %s", other, r.Path, r.Tool))
		}
		tools[r.Tool] = r.Path
	}
	return out, errors.Join(errs...)
}

func (d *Declaration) abs(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(d.Folder, p)
}

// WorkdirPath is workdir resolved against the folder.
func (d *Declaration) WorkdirPath() string {
	if d.Workdir == "" {
		return d.Folder
	}
	return d.abs(d.Workdir)
}

// Cap is output_cap, or DefaultOutputCap.
func (d *Declaration) Cap() int {
	if d.OutputCap <= 0 {
		return DefaultOutputCap
	}
	return d.OutputCap
}

// Secrets is every secret the scripts' env entries name, sorted.
func (d *Declaration) Secrets() []string {
	seen := map[string]bool{}
	for _, s := range d.Scripts.List {
		for _, secret := range s.Env {
			seen[secret] = true
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Snapshotted is one snapshotted script.
type Snapshotted struct {
	// Name is the file name; Tool the MCP tool, local_<name>.
	Name, Tool string
	// Path is the script's real path, symlinks resolved.
	Path string
	Size int64
	Mode fs.FileMode
	// SHA256 is the hex digest of its content at snapshot.
	SHA256 string

	// Description is the tool's description, with the # grain-args:
	// usage line when there is one.
	Description string
	Approve     bool
	// Timeout bounds one call; an approval may take as long again.
	Timeout time.Duration
	// Env is the env entries of its scripts.list entry.
	Env map[string]string
}

// Snapshot checks and records the scripts d serves. Any refusal fails
// the whole snapshot: a script is never dropped silently (Silo design
// §12.2).
func (d *Declaration) Snapshot() ([]Snapshotted, error) {
	resolved, err := d.Resolve()
	if err != nil {
		return nil, err
	}
	var errs []error
	out := make([]Snapshotted, 0, len(resolved))
	for _, r := range resolved {
		s, err := d.snapshotOne(r)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, s)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *Declaration) snapshotOne(r Resolved) (Snapshotted, error) {
	if err := checkFile(d.Folder, r.Path); err != nil {
		return Snapshotted{}, fmt.Errorf("script %s: %w", r.Name, err)
	}
	real, _ := filepath.EvalSymlinks(r.Path)
	fi, sum, head, err := hashFile(real)
	if err != nil {
		return Snapshotted{}, fmt.Errorf("script %s: %w", r.Name, err)
	}
	s := Snapshotted{
		Name: r.Name, Tool: r.Tool, Path: real,
		Size: fi.Size(), Mode: fi.Mode(), SHA256: sum,
		Timeout: DefaultTimeout,
	}
	if d.Timeout > 0 {
		s.Timeout = time.Duration(d.Timeout)
	}
	desc := ""
	if e := r.Entry; e != nil {
		desc = strings.TrimSpace(e.Description)
		s.Approve = e.Approve
		s.Env = e.Env
		if e.Timeout > 0 {
			s.Timeout = time.Duration(e.Timeout)
		}
	}
	comment, usage := scriptComments(head)
	if desc == "" {
		desc = comment
	}
	if desc == "" {
		desc = fmt.Sprintf("Run %s on the grain node host.", r.Name)
	}
	if usage != "" {
		desc += "\nUsage: " + r.Name + " " + usage
	}
	if s.Approve {
		desc += "\nEach call waits for a person to approve it."
	}
	s.Description = desc
	return s, nil
}

// checkFile applies the refusals of Silo design §12.2 to a script's real
// path: it must lie in the folder, be a regular executable file owned by
// the user grain runs as, and neither it nor any directory between it
// and the folder may be writable by group or world.
func checkFile(folder, path string) error {
	root, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("it resolves to %s, outside the declaration's folder", real)
	}
	fi, err := os.Lstat(real)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", real)
	}
	if fi.Mode()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", real)
	}
	if err := checkOwner(real, fi); err != nil {
		return err
	}
	for p := real; ; p = filepath.Dir(p) {
		pi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if pi.Mode().Perm()&0o022 != 0 {
			what := "it"
			if p != real {
				what = "its directory " + p
			}
			return fmt.Errorf("%s is writable by group or others (mode %04o): only grain's own user may be able to change a script grain runs", what, pi.Mode().Perm())
		}
		if p == root || p == filepath.Dir(p) {
			return nil
		}
	}
}

// getuid is the uid grain runs as; tests replace it.
var getuid = os.Getuid

func checkOwner(path string, fi fs.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if uid := getuid(); int(st.Uid) != uid {
		return fmt.Errorf("%s is owned by uid %d, not grain's own user (uid %d)", path, st.Uid, uid)
	}
	return nil
}

// hashFile returns a file's info, the hex SHA-256 of its content, and
// its first bytes (for descriptions, up to 64 KiB).
func hashFile(path string) (fs.FileInfo, string, []byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, "", nil, err
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil {
		return nil, "", nil, err
	}
	h := sha256.New()
	head := &capped{n: 64 << 10}
	if _, err := io.Copy(io.MultiWriter(h, head), fh); err != nil {
		return nil, "", nil, err
	}
	return fi, hex.EncodeToString(h.Sum(nil)), head.b, nil
}

type capped struct {
	b []byte
	n int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.n - len(c.b); room > 0 {
		c.b = append(c.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// Verify re-hashes a script before a call and returns an error saying
// what changed since the snapshot.
func (s Snapshotted) Verify() error {
	fi, sum, _, err := hashFile(s.Path)
	if err != nil {
		return fmt.Errorf("script %s could not be re-read: %w", s.Name, err)
	}
	var changed []string
	if sum != s.SHA256 {
		changed = append(changed, "content")
	}
	if fi.Size() != s.Size {
		changed = append(changed, "size")
	}
	if fi.Mode() != s.Mode {
		changed = append(changed, "mode")
	}
	if len(changed) > 0 {
		return fmt.Errorf("script %s has changed since this run started (%s): grain runs only what it snapshotted; the next run serves the new version",
			s.Name, strings.Join(changed, ", "))
	}
	return nil
}

// scriptComments reads # grain: lines (joined into a description) and
// the first # grain-args: line from a script's first describeLines
// lines. Silo's # silo: and # silo-args: are read too, so a script
// written for Silo describes itself unchanged.
func scriptComments(head []byte) (desc, usage string) {
	sc := bufio.NewScanner(bytes.NewReader(head))
	var lines []string
	for i := 0; i < describeLines && sc.Scan(); i++ {
		l := strings.TrimSpace(sc.Text())
		for _, p := range []string{"grain", "silo"} {
			switch {
			case strings.HasPrefix(l, "# "+p+"-args:"):
				if usage == "" {
					usage = strings.TrimSpace(strings.TrimPrefix(l, "# "+p+"-args:"))
				}
			case strings.HasPrefix(l, "# "+p+":"):
				if t := strings.TrimSpace(strings.TrimPrefix(l, "# "+p+":")); t != "" {
					lines = append(lines, t)
				}
			}
		}
	}
	return strings.Join(lines, " "), usage
}

// Check is the declaration's validation: the snapshot's refusals as
// errors, and Warnings' findings as warnings, each prefixed with the
// script's name.
func (d *Declaration) Check() (errs, warnings []string) {
	resolved, err := d.Resolve()
	if err != nil {
		errs = append(errs, err.Error())
	}
	for _, r := range resolved {
		if _, err := d.snapshotOne(r); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		for _, w := range Warnings(r.Path) {
			warnings = append(warnings, fmt.Sprintf("script %s: %s", r.Name, w))
		}
	}
	return errs, warnings
}
