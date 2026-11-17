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

package checks

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// siblingFixture builds an in-memory workspace plus a minimal unified diff. Paths listed in
// changed are emitted as modified (present at base and head), new (head only) or deleted
// (base only), decided by which side holds the file.
type siblingFixture struct {
	base    map[string]string
	head    map[string]string
	changed []string
}

func (f siblingFixture) run(t *testing.T, attempt int, notes string) ([]Finding, map[string][]int, int) {
	t.Helper()
	var b strings.Builder
	for _, p := range f.changed {
		_, inBase := f.base[p]
		_, inHead := f.head[p]
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", p, p)
		switch {
		case inHead && !inBase:
			fmt.Fprintf(&b, "new file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +1,1 @@\n+x\n", p)
		case inBase && !inHead:
			fmt.Fprintf(&b, "deleted file mode 100644\n--- a/%s\n+++ /dev/null\n@@ -1,1 +0,0 @@\n-x\n", p)
		default:
			fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n@@ -1,1 +1,1 @@\n-x\n+y\n", p, p)
		}
	}
	ws := &InMemoryWorkspace{Diff: b.String(), BaseFiles: f.base, HeadFiles: f.head}
	fs, hz, n, err := CheckSiblingExportHazard(context.Background(), ws, ParseUnifiedDiff(b.String()), attempt, notes)
	if err != nil {
		t.Fatalf("CheckSiblingExportHazard: %v", err)
	}
	return fs, hz, n
}

func withShared(shared map[string]string, own map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range shared {
		out[k] = v
	}
	for k, v := range own {
		out[k] = v
	}
	return out
}

func summaries(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Summary)
	}
	sort.Strings(out)
	return out
}

const testGoMod = "module example.com/os\n\ngo 1.24\n"

// (a) Same-named symbols in unrelated packages are not callers; an importer of the changed package
// is (paired positive).
func TestSiblingHazardIgnoresSameNamedSymbolsInUnrelatedPackages(t *testing.T) {
	baseTimer := `package timerwheel
type Config struct{ Levels int }
type WaitError struct{ Err error }
func (e *WaitError) Unwrap() error { return e.Err }
func (c Config) Validate() error { return nil }
`
	headTimer := `package timerwheel
type Config struct{ Levels int }
type WaitError struct{ Err error }
func (e *WaitError) Unwrap() []error { return []error{e.Err} }
func (c Config) Validate(strict bool) error { return nil }
`
	// Self-contained subsystems with their own Config/Unwrap/Validate, comments, and method decls.
	unrelated := map[string]string{
		"go.mod": testGoMod,
		"helios/mm/slab/core.go": `package slab
// Unwrap returns the underlying sentinel error.
type ConfigError struct{ Err error }
func (e *ConfigError) Unwrap() error { return e.Err }
type Config struct{ Size int }
func (c Config) Validate() error { return nil }
func use(e *ConfigError) error { _ = Config{1}.Validate(); return e.Unwrap() }
`,
		"helios/net/loopback/core.go": `package loopback
type Config struct{ Ports int }
func (c *Config) Validate() error { return nil }
func check(c *Config) error { return c.Validate() }
`,
	}
	neg := siblingFixture{
		base:    withShared(unrelated, map[string]string{"helios/sched/timerwheel/core.go": baseTimer}),
		head:    withShared(unrelated, map[string]string{"helios/sched/timerwheel/core.go": headTimer}),
		changed: []string{"helios/sched/timerwheel/core.go"},
	}
	fs, hz, n := neg.run(t, 1, "")
	if len(fs) != 0 || len(hz) != 0 || n != 0 {
		t.Fatalf("unrelated same-named symbols: got findings=%v hazardSites=%v maxPkgs=%d, want none", summaries(fs), hz, n)
	}

	// Paired positive: one real importer calling the re-signatured methods.
	importer := `package sleepq
import tw "example.com/os/helios/sched/timerwheel"
func wait(e *tw.WaitError, c tw.Config) error {
	_ = e.Unwrap()
	return c.Validate()
}
`
	pos := neg
	pos.head = withShared(neg.head, map[string]string{"helios/sched/sleepq/core.go": importer})
	fs, hz, n = pos.run(t, 1, "")
	got := summaries(fs)
	want := []string{
		"Modified exported symbol Config.Validate (changed function/method signature) has 1 untouched sibling caller package(s).",
		"Modified exported symbol WaitError.Unwrap (changed function/method signature) has 1 untouched sibling caller package(s).",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("importer findings:\n got %q\nwant %q", got, want)
	}
	if n != 1 || len(hz["helios/sched/sleepq/core.go"]) != 2 {
		t.Fatalf("maxPkgs=%d hazardSites=%v, want 1 package with 2 sites", n, hz)
	}
}

// (b) A package nobody imports has no sibling callers, even when other packages declare and call
// identically named functions.
func TestSiblingHazardPackageWithNoImportersHasNoFinding(t *testing.T) {
	shared := map[string]string{
		"go.mod":                     testGoMod,
		"helios/initd/fstab/core.go": "package fstab\nfunc ParseManifest(b []byte) error { return nil }\nfunc init() { _ = ParseManifest(nil) }\n",
		"helios/boot/csr/core.go":    "package csr\n// ParseManifest is mentioned here too.\nfunc ParseManifest() {}\n",
	}
	f := siblingFixture{
		base:    withShared(shared, map[string]string{"helios/initd/devmount/core.go": "package devmount\nfunc ParseManifest(b []byte) error { return nil }\n"}),
		head:    withShared(shared, map[string]string{"helios/initd/devmount/core.go": "package devmount\nfunc ParseManifest(b []byte, strict bool) error { return nil }\n"}),
		changed: []string{"helios/initd/devmount/core.go"},
	}
	fs, hz, n := f.run(t, 1, "")
	if len(fs) != 0 || len(hz) != 0 || n != 0 {
		t.Fatalf("package without importers: got findings=%v hazardSites=%v maxPkgs=%d, want none", summaries(fs), hz, n)
	}

	// Paired positive: the same change with one importer.
	f.head = withShared(f.head, map[string]string{
		"helios/initd/mountd/core.go": "package mountd\nimport \"example.com/os/helios/initd/devmount\"\nfunc run() error { return devmount.ParseManifest(nil) }\n",
	})
	fs, _, n = f.run(t, 1, "")
	if len(fs) != 1 || n != 1 || !strings.Contains(fs[0].Summary, "ParseManifest (changed function/method signature) has 1 untouched") {
		t.Fatalf("single importer: got %v maxPkgs=%d, want 1 finding with 1 package", summaries(fs), n)
	}
}

// (c) Genuine importers using a removed func or changed signature are counted per package:
// aliased imports count, touched packages and importers that never reference the symbol do not.
func TestSiblingHazardCountsGenuineImportersByImportPath(t *testing.T) {
	baseAPI := "package store\nfunc RelayProposal(title string) error { return nil }\nfunc DropProposal(id int) {}\nfunc Keep() {}\n"
	headAPI := "package store\nfunc RelayProposal(title string, strict bool) error { return nil }\nfunc Keep() {}\n"
	base := map[string]string{
		"go.mod":                   testGoMod,
		"pkg/store/api.go":         baseAPI,
		"pkg/finish/relay.go":      "package finish\nimport \"example.com/os/pkg/store\"\nfunc run() { _ = store.RelayProposal(\"t\"); store.DropProposal(1) }\n",
		"pkg/daemon/ctl.go":        "package daemon\nimport st \"example.com/os/pkg/store\"\nfunc sync() { _ = st.RelayProposal(\"t\") }\n",
		"pkg/cli/cmd.go":           "package cli\nimport \"example.com/os/pkg/store\"\nfunc cmd() { _ = store.RelayProposal(\"t\") }\n",
		"pkg/audit/audit.go":       "package audit\nimport \"example.com/os/pkg/store\"\nfunc a() { store.Keep() }\n",
		"pkg/other/relay.go":       "package other\nfunc RelayProposal(title string) error { return nil }\nfunc DropProposal(id int) {}\n",
		"pkg/finish/relay_test.go": "package finish\nimport \"example.com/os/pkg/store\"\nfunc t() { store.DropProposal(2) }\n",
	}
	head := withShared(base, map[string]string{
		"pkg/store/api.go": headAPI,
		"pkg/cli/cmd.go":   "package cli\nimport \"example.com/os/pkg/store\"\nfunc cmd() { _ = store.RelayProposal(\"t\", true) }\n",
	})
	f := siblingFixture{base: base, head: head, changed: []string{"pkg/store/api.go", "pkg/cli/cmd.go"}}
	fs, hz, n := f.run(t, 1, "")
	got := summaries(fs)
	want := []string{
		"Modified exported symbol DropProposal (removed exported func; declared at pkg/store/api.go:3 in base) has 1 untouched sibling caller package(s).",
		"Modified exported symbol RelayProposal (changed function/method signature) has 2 untouched sibling caller package(s).",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("findings:\n got %q\nwant %q", got, want)
	}
	if n != 3 {
		t.Fatalf("maxSiblingPkgs = %d, want 3 (finish, daemon, cli incl. the touched one)", n)
	}
	if _, ok := hz["pkg/audit/audit.go"]; ok {
		t.Fatalf("importer that never references the changed symbols must not be a hazard site: %v", hz)
	}
	if _, ok := hz["pkg/other/relay.go"]; ok {
		t.Fatalf("same-named func in a non-importing package must not be a hazard site: %v", hz)
	}
	// Caller notes and later attempts still clear the blocking findings but keep the counts.
	if fs, _, n := f.run(t, 1, "callers updated in a stacked task"); len(fs) != 0 || n != 3 {
		t.Fatalf("waived: findings=%v maxPkgs=%d, want 0 findings and maxPkgs 3", summaries(fs), n)
	}
}

// (d) Declarations moved into a newly added file of the same package, and brand-new packages,
// are not removals; deleting a declaration that an importer uses is (paired positive).
func TestSiblingHazardNewFilesAndNewPackagesAreNotRemovals(t *testing.T) {
	baseCore := `package timerwheel
type WaitError struct{ Err error }
func (e *WaitError) Unwrap() error { return e.Err }
type SleepQueue struct{}
func (q *SleepQueue) Len() int { return 0 }
func New() *SleepQueue { return nil }
`
	headCore := "package timerwheel\nfunc New() *SleepQueue { return nil }\n"
	headEngine := `package timerwheel
type WaitError struct{ Err error }
func (e *WaitError) Unwrap() error { return e.Err }
type SleepQueue struct{}
func (q *SleepQueue) Len() int { return 0 }
`
	importer := "package sched\nimport \"example.com/os/helios/sched/timerwheel\"\nfunc n(e *timerwheel.WaitError) int { _ = e.Unwrap(); return timerwheel.New().Len() }\n"
	base := map[string]string{"go.mod": testGoMod, "helios/sched/timerwheel/core.go": baseCore, "helios/sched/core.go": importer}
	moved := siblingFixture{
		base: base,
		head: withShared(base, map[string]string{
			"helios/sched/timerwheel/core.go":   headCore,
			"helios/sched/timerwheel/engine.go": headEngine,
			"helios/brandnew/api.go":            "package brandnew\nfunc Serve() {}\n",
			"helios/brandnew/user/use.go":       "package user\nimport \"example.com/os/helios/brandnew\"\nfunc u() { brandnew.Serve() }\n",
		}),
		changed: []string{
			"helios/sched/timerwheel/core.go", "helios/sched/timerwheel/engine.go",
			"helios/brandnew/api.go", "helios/brandnew/user/use.go",
		},
	}
	if fs, hz, _ := moved.run(t, 1, ""); len(fs) != 0 || len(hz) != 0 {
		t.Fatalf("moved declarations / new package: got %v hazardSites=%v, want none", summaries(fs), hz)
	}

	// Paired positive: the same PR without engine.go really removes WaitError and SleepQueue.Len.
	removed := moved
	removed.head = withShared(base, map[string]string{"helios/sched/timerwheel/core.go": headCore})
	removed.changed = []string{"helios/sched/timerwheel/core.go"}
	fs, _, _ := removed.run(t, 1, "")
	got := summaries(fs)
	// SleepQueue itself is never named as timerwheel.SleepQueue by the importer (it only calls
	// New().Len()), so the struct removal has no qualified caller; the method removal does.
	for _, sym := range []string{"SleepQueue.Len (removed exported func", "WaitError (removed exported struct", "WaitError.Unwrap (removed exported func"} {
		if !strings.Contains(strings.Join(got, "\n"), "Modified exported symbol "+sym) {
			t.Fatalf("missing removal finding for %s in %q", sym, got)
		}
	}
	if len(fs) != 3 {
		t.Fatalf("got %d findings %q, want 3", len(fs), got)
	}
	for _, f := range fs {
		if f.FilePath != "helios/sched/timerwheel/core.go" {
			t.Fatalf("removal reported on %q, want the declaring file", f.FilePath)
		}
	}

	// Deleting the whole file the declarations lived in is also a removal.
	deleted := siblingFixture{
		base:    withShared(base, map[string]string{"helios/sched/timerwheel/engine.go": "package timerwheel\nfunc Drain() {}\n"}),
		head:    withShared(base, map[string]string{"helios/sched/core.go": importer + "func d() { timerwheel.Drain() }\n"}),
		changed: []string{"helios/sched/timerwheel/engine.go"},
	}
	delete(deleted.head, "helios/sched/timerwheel/engine.go")
	fs, _, _ = deleted.run(t, 1, "")
	if len(fs) != 1 || !strings.Contains(fs[0].Summary, "Drain (removed exported func") {
		t.Fatalf("deleted file: got %q, want Drain removal", summaries(fs))
	}
}

// (e) Adding an exported struct field only breaks positional composite literals of that type.
// Keyed literals, zero literals and field reads keep compiling. Removing or re-typing a field
// breaks selectors and keyed literals naming it.
func TestSiblingHazardStructFieldSemantics(t *testing.T) {
	baseCfg := "package cfs\ntype Config struct {\n\tMinSliceNS int\n\tWeight int\n}\n"
	addField := "package cfs\ntype Config struct {\n\tMinSliceNS int\n\tWeight int\n\tMaxSliceNS int\n}\n"
	compatible := map[string]string{
		"go.mod":              testGoMod,
		"helios/sched/a/a.go": "package a\nimport \"example.com/os/helios/sched/cfs\"\nvar c = cfs.Config{MinSliceNS: 1, Weight: 2}\nvar z = cfs.Config{}\nfunc w(c cfs.Config) int { return c.Weight }\n",
		// Unrelated package with its own positional Config literal.
		"helios/net/b/b.go": "package b\ntype Config struct{ A, B int }\nvar c = Config{1, 2}\n",
	}
	f := siblingFixture{
		base:    withShared(compatible, map[string]string{"helios/sched/cfs/core.go": baseCfg}),
		head:    withShared(compatible, map[string]string{"helios/sched/cfs/core.go": addField}),
		changed: []string{"helios/sched/cfs/core.go"},
	}
	if fs, hz, n := f.run(t, 1, ""); len(fs) != 0 || len(hz) != 0 || n != 0 {
		t.Fatalf("keyed/zero literals and field reads: got %v hazardSites=%v maxPkgs=%d, want none", summaries(fs), hz, n)
	}

	// Paired positive: positional literal and an elided positional element literal.
	f.head = withShared(f.head, map[string]string{
		"helios/sched/c/c.go": "package c\nimport \"example.com/os/helios/sched/cfs\"\nvar c = cfs.Config{1, 2}\n",
		"helios/sched/d/d.go": "package d\nimport \"example.com/os/helios/sched/cfs\"\nvar cs = []*cfs.Config{{1, 2}}\n",
	})
	fs, hz, n := f.run(t, 1, "")
	if len(fs) != 1 || n != 2 || !strings.Contains(fs[0].Summary, "added exported struct field MaxSliceNS; breaks positional literals only) has 2 untouched") {
		t.Fatalf("positional literals: got %q maxPkgs=%d, want 1 finding over 2 packages", summaries(fs), n)
	}
	if len(hz["helios/sched/c/c.go"]) != 1 || len(hz["helios/sched/d/d.go"]) != 1 || len(hz["helios/sched/a/a.go"]) != 0 {
		t.Fatalf("hazard sites = %v", hz)
	}

	// Removing a field breaks selectors and keyed literals naming it.
	rmField := "package cfs\ntype Config struct {\n\tMinSliceNS int\n}\n"
	g := siblingFixture{
		base:    withShared(compatible, map[string]string{"helios/sched/cfs/core.go": baseCfg}),
		head:    withShared(compatible, map[string]string{"helios/sched/cfs/core.go": rmField}),
		changed: []string{"helios/sched/cfs/core.go"},
	}
	fs, hz, _ = g.run(t, 1, "")
	if len(fs) != 1 || !strings.Contains(fs[0].Summary, "re-typed struct field Weight) has 1 untouched") {
		t.Fatalf("removed field: got %q, want 1 finding over helios/sched/a", summaries(fs))
	}
	if len(hz["helios/sched/a/a.go"]) != 2 {
		t.Fatalf("removed field hazard sites = %v, want keyed literal + selector lines in a.go", hz)
	}
}

// TypeScript callers must import the changed module through a resolvable relative specifier.
func TestSiblingHazardTypeScriptResolvesRelativeImports(t *testing.T) {
	shared := map[string]string{
		"ui/src/other/api.ts":   "export function loadTasks(limit: number): string[] { return []; }\nloadTasks(1);\n",
		"ui/src/feature/use.ts": "import { loadTasks } from '../other/api';\nloadTasks(2);\n",
	}
	f := siblingFixture{
		base:    withShared(shared, map[string]string{"ui/src/core/api.ts": "export function loadTasks(limit: number): string[] { return []; }\n"}),
		head:    withShared(shared, map[string]string{"ui/src/core/api.ts": "export function loadTasks(limit: number, cursor: string): string[] { return []; }\n"}),
		changed: []string{"ui/src/core/api.ts"},
	}
	if fs, hz, _ := f.run(t, 1, ""); len(fs) != 0 || len(hz) != 0 {
		t.Fatalf("same-named TS export in another module: got %q hazardSites=%v, want none", summaries(fs), hz)
	}
	f.head = withShared(f.head, map[string]string{
		"ui/src/pages/list.ts": "import { loadTasks } from '../core/api';\nexport const rows = loadTasks(5);\n",
	})
	fs, hz, _ := f.run(t, 1, "")
	if len(fs) != 1 || !strings.Contains(fs[0].Summary, "loadTasks (changed TypeScript export signature) has 1 untouched") {
		t.Fatalf("TS importer: got %q", summaries(fs))
	}
	if len(hz["ui/src/pages/list.ts"]) != 2 {
		t.Fatalf("TS hazard sites = %v", hz)
	}
}

func TestGoCallerResolverImportPathFromNearestGoMod(t *testing.T) {
	ws := &InMemoryWorkspace{HeadFiles: map[string]string{
		"go.mod":       "module example.com/root // comment\n",
		"tools/go.mod": "module \"example.com/tools\"\n",
	}}
	r := newGoCallerResolver(ws)
	ctx := context.Background()
	for dir, want := range map[string]string{
		"":              "example.com/root",
		"pkg/a":         "example.com/root/pkg/a",
		"tools":         "example.com/tools",
		"tools/gen/sub": "example.com/tools/gen/sub",
	} {
		if got := r.importPathFor(ctx, dir); got != want {
			t.Errorf("importPathFor(%q) = %q, want %q", dir, got, want)
		}
	}
}

// Real git: the quoted import path must survive `git grep -F -w` through MirrorWorkspace, and
// a same-named symbol in a non-importing package must not count.
func TestSiblingHazardOverRealGitMirror(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	originDir := filepath.Join(tmp, "origin.git")
	workDir := filepath.Join(tmp, "work")
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "safe.bareRepository=all"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(workDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(tmp, "init", "--bare", "--initial-branch=main", originDir)
	runGit(tmp, "clone", originDir, workDir)
	write("go.mod", testGoMod)
	write("pkg/store/api.go", "package store\nfunc Relay(s string) error { return nil }\n")
	write("pkg/user/use.go", "package user\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/os/pkg/store\"\n)\n\nfunc u() { fmt.Println(store.Relay(\"x\")) }\n")
	write("pkg/other/other.go", "package other\nfunc Relay(s string) error { return nil }\nvar _ = Relay(\"y\")\n")
	write("pkg/storex/x.go", "package storex\nimport \"example.com/os/pkg/store/v2\"\nvar _ = v2.Relay\n")
	write("tools/go.mod", "module example.com/tools\n")
	write("tools/gen/gen.go", "package gen\n")
	runGit(workDir, "add", ".")
	runGit(workDir, "commit", "-m", "base")
	runGit(workDir, "push", "origin", "main")
	runGit(workDir, "checkout", "-b", "feature")
	write("pkg/store/api.go", "package store\nfunc Relay(s string, strict bool) error { return nil }\n")
	runGit(workDir, "commit", "-am", "change Relay")
	runGit(workDir, "push", "origin", "feature")

	mirror, err := OpenMirrorWorkspace(ctx, filepath.Join(tmp, "cache"), originDir, "", "main", "feature")
	if err != nil {
		t.Fatalf("OpenMirrorWorkspace: %v", err)
	}
	raw, err := mirror.BaseHeadDiff(ctx)
	if err != nil {
		t.Fatalf("BaseHeadDiff: %v", err)
	}
	fs, hz, n, err := CheckSiblingExportHazard(ctx, mirror, ParseUnifiedDiff(raw), 1, "")
	if err != nil {
		t.Fatalf("CheckSiblingExportHazard: %v", err)
	}
	if len(fs) != 1 || n != 1 || !strings.Contains(fs[0].Summary, "Relay (changed function/method signature) has 1 untouched") {
		t.Fatalf("got findings=%q maxPkgs=%d, want 1 finding over pkg/user", summaries(fs), n)
	}
	if len(hz) != 1 || len(hz["pkg/user/use.go"]) != 1 || hz["pkg/user/use.go"][0] != 9 {
		t.Fatalf("hazard sites = %v, want pkg/user/use.go:9 only", hz)
	}

	// The same check through SandboxWorkspace (the `grain mcpserver` path) over the live checkout.
	sandbox := &SandboxWorkspace{
		CheckoutDir: workDir,
		BaseBranch:  "main",
		RunCommand: func(ctx context.Context, command string) (string, bool, error) {
			out, err := exec.CommandContext(ctx, "bash", "-c", command).CombinedOutput()
			if _, exit := err.(*exec.ExitError); exit {
				return string(out), true, nil
			}
			return string(out), false, err
		},
	}
	raw, err = sandbox.BaseHeadDiff(ctx)
	if err != nil {
		t.Fatalf("sandbox BaseHeadDiff: %v", err)
	}
	sfs, shz, sn, err := CheckSiblingExportHazard(ctx, sandbox, ParseUnifiedDiff(raw), 1, "")
	if err != nil || len(sfs) != 1 || sn != 1 || len(shz["pkg/user/use.go"]) != 1 {
		t.Fatalf("sandbox: findings=%q maxPkgs=%d hz=%v err=%v, want the mirror result", summaries(sfs), sn, shz, err)
	}

	// Both workspaces locate the nearest go.mod in one call, including nested modules.
	for name, loc := range map[string]goModuleLocator{"mirror": mirror, "sandbox": sandbox} {
		for dir, want := range map[string]string{"pkg/user": "", "tools/gen": "tools", "": ""} {
			modDir, content, ok, err := loc.NearestGoModAtHead(ctx, dir)
			if err != nil || !ok || modDir != want || !strings.HasPrefix(string(content), "module ") {
				t.Errorf("%s.NearestGoModAtHead(%q) = (%q, %q, %v, %v), want dir %q", name, dir, modDir, content, ok, err, want)
			}
		}
	}
}
