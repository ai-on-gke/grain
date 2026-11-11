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

// Command security-attacks runs and judges the user-attack regression
// suites listed in tests/security/attacks.yaml, one verdict per attack
// path, for nightly.yml's security-attacks job (a Stage 1 qualification
// job, design/playbook/releases.playbook.md).
//
// Usage:
//
//	security-attacks check [-root DIR]
//	security-attacks run   [-root DIR] [-summary FILE] [-log FILE] [-failed FILE] [-timeout D]
//	security-attacks judge -mode MODE -log FILE... [-root DIR] [-summary FILE] [-failed FILE]
//
// check fails when the manifest and the tree disagree: a test package
// under tests/security that `packages` does not list (so a new suite
// cannot be silently left out of the job), or a test a path names that no
// longer exists.
//
// run runs check, then every in-process path's package with `go test
// -json`: a package `packages` lists whole, any other with -run narrowed
// to the tests its paths name. judge reads the `go test -v` logs a
// real-sandbox job wrote and judges the paths of one live mode.
//
// Both fail closed. A path passes only when every test it names ran and
// passed; a failure, a test that never ran (a rename, a build failure, a
// panic, a log that is not there) and a skip all count against it, except
// a skip whose reason starts "known gap:" or "infrastructure:", which are
// reported by name. Every result goes to stdout, to GitHub annotations,
// and as a markdown table to -summary ($GITHUB_STEP_SUMMARY); the names of
// the failed paths go to -failed, one per line. Exit 1 when any path
// failed, 2 on a usage or manifest error.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ManifestPath is where the manifest lives, relative to the repository.
const ManifestPath = "tests/security/attacks.yaml"

// securityRoot is the tree whose every test package has to be run.
const securityRoot = "tests/security"

const (
	modeInProcess = "in-process"
	modeLiveVM    = "live-vm"
	modeLiveK8s   = "live-k8s"
)

// liveJob is the nightly.yml job that runs each live mode.
var liveJob = map[string]string{
	modeLiveVM:  "kvm-sandboxes",
	modeLiveK8s: "real-kubernetes",
}

type Manifest struct {
	Packages []string `yaml:"packages"`
	Paths    []Path   `yaml:"paths"`
}

type Path struct {
	Name    string   `yaml:"name"`
	Task    int      `yaml:"task"`
	Title   string   `yaml:"title"`
	Mode    string   `yaml:"mode"`
	Package string   `yaml:"package"`
	Tests   []string `yaml:"tests"`
}

func loadManifest(root string) (Manifest, error) {
	var m Manifest
	b, err := os.ReadFile(filepath.Join(root, ManifestPath))
	if err != nil {
		return m, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("%s: %w", ManifestPath, err)
	}
	return m, nil
}

var testFunc = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\(\w+ \*testing\.T\)`)

// packageTests returns the top-level tests declared in dir's _test.go files.
func packageTests(dir string) (map[string]bool, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	tests := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, m := range testFunc.FindAllSubmatch(b, -1) {
			tests[string(m[1])] = true
		}
	}
	return tests, nil
}

// securityTestPackages returns every directory under tests/security holding
// a _test.go file, as ./tests/security/....
func securityTestPackages(root string) ([]string, error) {
	seen := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, securityRoot), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != filepath.Join(root, securityRoot) && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_") || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), "_test.go") {
			rel, err := filepath.Rel(root, filepath.Dir(p))
			if err != nil {
				return err
			}
			seen["./"+filepath.ToSlash(rel)] = true
		}
		return nil
	})
	pkgs := make([]string, 0, len(seen))
	for p := range seen {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	return pkgs, err
}

// Check reports every way the manifest and the tree disagree.
func Check(root string, m Manifest) []string {
	var problems []string
	pkgs, err := securityTestPackages(root)
	if err != nil {
		return []string{fmt.Sprintf("walking %s: %v", securityRoot, err)}
	}
	for _, p := range pkgs {
		if !slices.Contains(m.Packages, p) {
			problems = append(problems, fmt.Sprintf("%s holds tests but %s's packages does not list it, so nightly's security-attacks job would never run it", p, ManifestPath))
		}
	}
	for _, p := range m.Packages {
		if !slices.Contains(pkgs, p) {
			problems = append(problems, fmt.Sprintf("packages lists %s, which holds no tests under %s", p, securityRoot))
		}
	}
	names := map[string]bool{}
	declared := map[string]map[string]bool{}
	for _, p := range m.Paths {
		switch {
		case p.Name == "":
			problems = append(problems, "a path has no name")
			continue
		case names[p.Name]:
			problems = append(problems, fmt.Sprintf("path %s is listed twice", p.Name))
		}
		names[p.Name] = true
		if p.Mode != modeInProcess && liveJob[p.Mode] == "" {
			problems = append(problems, fmt.Sprintf("path %s has mode %q, want %s, %s or %s", p.Name, p.Mode, modeInProcess, modeLiveVM, modeLiveK8s))
		}
		if p.Task == 0 || p.Title == "" {
			problems = append(problems, fmt.Sprintf("path %s needs a task and a title, which the job summary reports it by", p.Name))
		}
		if !strings.HasPrefix(p.Package, "./") {
			problems = append(problems, fmt.Sprintf("path %s's package %q is not a ./ path", p.Name, p.Package))
			continue
		}
		if len(p.Tests) == 0 {
			problems = append(problems, fmt.Sprintf("path %s names no tests", p.Name))
		}
		tests, ok := declared[p.Package]
		if !ok {
			if tests, err = packageTests(filepath.Join(root, filepath.FromSlash(p.Package))); err != nil {
				problems = append(problems, fmt.Sprintf("path %s: reading %s: %v", p.Name, p.Package, err))
				continue
			}
			declared[p.Package] = tests
		}
		for _, t := range p.Tests {
			if !tests[t] {
				problems = append(problems, fmt.Sprintf("path %s names %s, which %s does not declare", p.Name, t, p.Package))
			}
		}
	}
	return problems
}

// Status is a test's or a path's outcome.
type Status string

const (
	Pass     Status = "pass"
	Fail     Status = "fail"
	Skip     Status = "skip"
	KnownGap Status = "known gap"
	Infra    Status = "infrastructure"
	Missing  Status = "did not run"
	Running  Status = "did not finish"
	Live     Status = "live"
)

// result is one top-level test's outcome and what it printed.
type result struct {
	status Status
	output []string
	own    []string // the lines of output the top-level test printed itself, not its subtests
}

var reasonLine = regexp.MustCompile(`^\s+\S+\.go:\d+: (.*)$`)

// skipReason is a skipped test's reason: the last file:line message the
// top-level test printed itself, which is its t.Skip call's own. A
// subtest's message -- a parallel subtest's can come after the parent's
// skip -- is not.
func (r *result) skipReason() string {
	for i := len(r.own) - 1; i >= 0; i-- {
		if m := reasonLine.FindStringSubmatch(r.own[i]); m != nil {
			return m[1]
		}
	}
	return ""
}

// finish settles a skipped test's status from its reason (skipReason). An
// earlier t.Log, or a subtest's output, that happens to start "known
// gap:" does not excuse a skip for some other reason.
func (r *result) finish() {
	if r.status != Skip {
		return
	}
	reason := strings.ToLower(r.skipReason())
	switch {
	case strings.HasPrefix(reason, "known gap:"):
		r.status = KnownGap
	case strings.HasPrefix(reason, "infrastructure:"):
		r.status = Infra
	}
}

func (r *result) reason() string {
	switch r.status {
	case Skip, KnownGap, Infra:
		if s := r.skipReason(); s != "" {
			return s
		}
	}
	for _, line := range r.output {
		if m := reasonLine.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	for i := len(r.output) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(r.output[i]); s != "" && !strings.HasPrefix(s, "---") && !strings.HasPrefix(s, "===") {
			return s
		}
	}
	return ""
}

// run is one package's go test results.
type pkgRun struct {
	tests  map[string]*result
	order  []string
	exit   int
	failed string // why the package failed with no test to blame, if it did
}

func newPkgRun() *pkgRun { return &pkgRun{tests: map[string]*result{}} }

func (p *pkgRun) test(name string) *result {
	r, ok := p.tests[name]
	if !ok {
		r = &result{status: Running}
		p.tests[name] = r
		p.order = append(p.order, name)
	}
	return r
}

type testEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// parseJSON reads go test -json events for one package, copying every
// line of output to log.
func parseJSON(r io.Reader, log io.Writer) *pkgRun {
	run := newPkgRun()
	var pkgOut []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev testEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || ev.Action == "" {
			fmt.Fprintln(log, sc.Text())
			pkgOut = append(pkgOut, sc.Text())
			continue
		}
		if ev.Output != "" {
			io.WriteString(log, ev.Output)
		}
		top, sub, _ := strings.Cut(ev.Test, "/")
		switch {
		case ev.Test == "":
			if ev.Output != "" {
				pkgOut = append(pkgOut, strings.TrimRight(ev.Output, "\n"))
			}
			if ev.Action == "fail" || ev.Action == "build-fail" {
				run.failed = lastMeaningful(pkgOut)
			}
		case ev.Action == "output":
			r, line := run.test(top), strings.TrimRight(ev.Output, "\n")
			r.output = append(r.output, line)
			if sub == "" {
				r.own = append(r.own, line)
			}
		case sub != "":
			// Subtests are their parent's business: it fails when they do.
		case ev.Action == "run":
			run.test(top)
		case ev.Action == "pass":
			run.test(top).status = Pass
		case ev.Action == "fail":
			run.test(top).status = Fail
		case ev.Action == "skip":
			run.test(top).status = Skip
		}
	}
	for _, r := range run.tests {
		r.finish()
	}
	return run
}

var (
	verdictLine = regexp.MustCompile(`^--- (PASS|FAIL|SKIP): (Test[^\s/]*) `)
	headerLine  = regexp.MustCompile(`^=== (?:RUN|NAME|CONT|PAUSE)\s+(Test\S*)$`)
)

// parseText reads `go test -v` output: only unindented --- lines are
// top-level verdicts. An indented line belongs to the test the last ===
// header named -- -v streams a test's messages ahead of its verdict -- or,
// in output that is not streamed, to the verdict just above it.
func parseText(r io.Reader) *pkgRun {
	run := newPkgRun()
	var cur *result
	own := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := verdictLine.FindStringSubmatch(line); m != nil {
			cur, own = run.test(m[2]), true
			cur.status = map[string]Status{"PASS": Pass, "FAIL": Fail, "SKIP": Skip}[m[1]]
			continue
		}
		if m := headerLine.FindStringSubmatch(line); m != nil {
			top, sub, _ := strings.Cut(m[1], "/")
			cur, own = run.test(top), sub == ""
			continue
		}
		if cur != nil && strings.HasPrefix(line, "    ") {
			cur.output = append(cur.output, line)
			// Unstreamed, a subtest's lines are indented four spaces more
			// than its parent's.
			if own && !strings.HasPrefix(line, "        ") {
				cur.own = append(cur.own, line)
			}
			continue
		}
		cur = nil
	}
	for _, r := range run.tests {
		r.finish()
	}
	return run
}

func lastMeaningful(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		s := strings.TrimSpace(lines[i])
		if s != "" && s != "FAIL" && !strings.HasPrefix(s, "FAIL\t") && !strings.HasPrefix(s, "ok ") {
			return s
		}
	}
	return "the package failed"
}

// Verdict is one attack path's outcome.
type Verdict struct {
	Path   Path
	Status Status
	Detail string
}

// judgePath settles a path from its package's run (nil: nothing ran).
func judgePath(p Path, run *pkgRun) Verdict {
	v := Verdict{Path: p, Status: Pass, Detail: fmt.Sprintf("%d test(s) passed", len(p.Tests))}
	var bad, gaps, infra []string
	for _, t := range p.Tests {
		var r *result
		if run != nil {
			r = run.tests[t]
		}
		switch {
		case r == nil:
			why := "did not run"
			if run != nil && run.failed != "" {
				why += " (" + run.failed + ")"
			} else if run == nil {
				why += " (no result for its package)"
			}
			bad = append(bad, t+" "+why)
		case r.status == Pass:
		case r.status == KnownGap:
			gaps = append(gaps, t+": "+r.reason())
		case r.status == Infra:
			infra = append(infra, t+": "+r.reason())
		case r.status == Skip:
			bad = append(bad, t+" skipped ("+r.reason()+"), and a skip is not a pass")
		case r.status == Running:
			bad = append(bad, t+" did not finish (timeout or panic)")
		default:
			why := t + " failed"
			if reason := r.reason(); reason != "" {
				why += ": " + reason
			}
			bad = append(bad, why)
		}
	}
	switch {
	case len(bad) > 0:
		v.Status, v.Detail = Fail, strings.Join(bad, "; ")
	case len(infra) > 0:
		v.Status, v.Detail = Infra, strings.Join(infra, "; ")
	case len(gaps) > 0:
		v.Status, v.Detail = KnownGap, strings.Join(gaps, "; ")
	}
	return v
}

// judgeOthers settles a whole-run package's tests no path names, and the
// package's own exit, as a path of their own.
func judgeOthers(pkg string, named map[string]bool, run *pkgRun) Verdict {
	p := Path{Name: pkg + ": other tests", Title: "every other test in " + pkg, Mode: modeInProcess, Package: pkg}
	v := Verdict{Path: p, Status: Pass}
	var bad []string
	count := 0
	for _, t := range run.order {
		if named[t] {
			continue
		}
		count++
		switch r := run.tests[t]; r.status {
		case Pass, KnownGap, Infra:
		case Skip:
			bad = append(bad, t+" skipped ("+r.reason()+"), and a skip is not a pass")
		case Running:
			bad = append(bad, t+" did not finish (timeout or panic)")
		default:
			bad = append(bad, t+" failed: "+r.reason())
		}
	}
	if len(bad) == 0 && run.exit != 0 {
		why := run.failed
		if why == "" {
			why = "no test to blame"
		}
		bad = append(bad, fmt.Sprintf("go test exited %d: %s", run.exit, why))
	}
	if len(bad) > 0 {
		v.Status, v.Detail = Fail, strings.Join(bad, "; ")
	} else {
		v.Detail = fmt.Sprintf("%d test(s)", count)
	}
	return v
}

// report writes every verdict out and returns how many failed.
func report(verdicts []Verdict, heading string, stdout io.Writer, summary, failedFile string) (int, error) {
	var md strings.Builder
	fmt.Fprintf(&md, "## %s\n\n| Attack path | Task | Mode | Result | Detail |\n| --- | --- | --- | --- | --- |\n", heading)
	var failed []string
	for _, v := range verdicts {
		task := "-"
		if v.Path.Task != 0 {
			task = fmt.Sprintf("#%d", v.Path.Task)
		}
		mark := map[Status]string{Pass: "PASS", Fail: "FAIL", KnownGap: "PASS (known gap)", Infra: "INFRA", Live: "LIVE"}[v.Status]
		fmt.Fprintf(&md, "| `%s` %s | %s | %s | **%s** | %s |\n", v.Path.Name, mdEscape(v.Path.Title), task, v.Path.Mode, mark, mdEscape(v.Detail))
		fmt.Fprintf(stdout, "%-17s %s (%s): %s\n", mark, v.Path.Name, task, v.Detail)
		switch v.Status {
		case Fail:
			failed = append(failed, v.Path.Name)
			fmt.Fprintf(stdout, "::error title=security-attacks %s::%s\n", v.Path.Name, annotation(v.Detail))
		case KnownGap, Infra:
			fmt.Fprintf(stdout, "::warning title=security-attacks %s::%s: %s\n", v.Path.Name, v.Status, annotation(v.Detail))
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(&md, "\n**%d attack path(s) failed:** %s\n", len(failed), strings.Join(failed, ", "))
	} else {
		fmt.Fprintf(&md, "\nNo attack path failed.\n")
	}
	if summary != "" {
		f, err := os.OpenFile(summary, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return len(failed), err
		}
		_, err = io.WriteString(f, md.String()+"\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return len(failed), err
		}
	}
	if failedFile != "" {
		var b strings.Builder
		for _, name := range failed {
			b.WriteString(name + "\n")
		}
		if err := os.WriteFile(failedFile, []byte(b.String()), 0o644); err != nil {
			return len(failed), err
		}
	}
	if len(failed) > 0 {
		fmt.Fprintf(stdout, "security-attacks: %d of %d attack path(s) FAILED: %s\n", len(failed), len(verdicts), strings.Join(failed, ", "))
	} else {
		fmt.Fprintf(stdout, "security-attacks: all %d attack path(s) passed\n", len(verdicts))
	}
	return len(failed), nil
}

func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.ReplaceAll(s, "\n", " ")
}

func annotation(s string) string {
	if len(s) > 900 {
		s = s[:900] + "..."
	}
	return strings.NewReplacer("%", "%25", "\r", "%0D", "\n", "%0A").Replace(s)
}

// runner runs `go test` for one package; a seam for tests.
type runner func(dir string, args []string, out io.Writer) (int, error)

func goTest(dir string, args []string, out io.Writer) (int, error) {
	cmd := exec.Command("go", append([]string{"test"}, args...)...)
	cmd.Dir = dir
	// go test needs the job's PATH, GOFLAGS and caches, and the suites
	// read their opt-ins (GRAIN_REAL_*) from it.
	cmd.Env = os.Environ()
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// RunInProcess runs every in-process path's package and judges it, with
// the live paths listed by the job that runs them.
func RunInProcess(root string, m Manifest, timeout time.Duration, log io.Writer, progress io.Writer, goTest runner) []Verdict {
	type plan struct {
		pkg   string
		whole bool
		tests []string
	}
	var plans []plan
	byPkg := map[string]int{}
	for _, pkg := range m.Packages {
		byPkg[pkg] = len(plans)
		plans = append(plans, plan{pkg: pkg, whole: true})
	}
	for _, p := range m.Paths {
		if p.Mode != modeInProcess {
			continue
		}
		i, ok := byPkg[p.Package]
		if !ok {
			i = len(plans)
			byPkg[p.Package] = i
			plans = append(plans, plan{pkg: p.Package})
		}
		plans[i].tests = append(plans[i].tests, p.Tests...)
	}
	runs := map[string]*pkgRun{}
	for _, pl := range plans {
		args := []string{"-json", "-count=1", "-timeout", timeout.String()}
		if !pl.whole {
			quoted := make([]string, len(pl.tests))
			for i, t := range pl.tests {
				quoted[i] = regexp.QuoteMeta(t)
			}
			args = append(args, "-run", "^("+strings.Join(quoted, "|")+")$")
		}
		args = append(args, pl.pkg)
		fmt.Fprintf(progress, "go test %s\n", strings.Join(args, " "))
		pr, pw := io.Pipe()
		done := make(chan *pkgRun)
		go func() { done <- parseJSON(pr, log) }()
		start := time.Now()
		exit, err := goTest(root, args, pw)
		pw.Close()
		run := <-done
		if err != nil {
			run.failed = "go test could not start: " + err.Error()
			exit = -1
		}
		run.exit = exit
		runs[pl.pkg] = run
		fmt.Fprintf(progress, "  %s: exit %d after %s, %d test(s)\n", pl.pkg, exit, time.Since(start).Round(time.Second), len(run.tests))
	}
	var verdicts []Verdict
	named := map[string]map[string]bool{}
	for _, p := range m.Paths {
		if p.Mode != modeInProcess {
			verdicts = append(verdicts, Verdict{Path: p, Status: Live, Detail: "run inside nightly's " + liveJob[p.Mode] + " job; its summary has the verdict"})
			continue
		}
		v := judgePath(p, runs[p.Package])
		if run := runs[p.Package]; run != nil && run.exit != 0 && v.Status != Fail && byPkg[p.Package] >= len(m.Packages) {
			// A -run narrowed package exits non-zero only for a test of
			// these paths or the package itself; blame every path in it
			// that did not explain the exit.
			v.Status = Fail
			why := run.failed
			if why == "" {
				why = "no test to blame"
			}
			v.Detail = fmt.Sprintf("go test %s exited %d: %s", p.Package, run.exit, why)
		}
		verdicts = append(verdicts, v)
		if named[p.Package] == nil {
			named[p.Package] = map[string]bool{}
		}
		for _, t := range p.Tests {
			named[p.Package][t] = true
		}
	}
	for _, pkg := range m.Packages {
		verdicts = append(verdicts, judgeOthers(pkg, named[pkg], runs[pkg]))
	}
	return verdicts
}

// JudgeLive judges the paths of one live mode from the go test -v logs
// the job that runs them wrote. A log that is missing judges nothing,
// so every test is reported as not run.
func JudgeLive(m Manifest, mode string, logs []string) []Verdict {
	merged := newPkgRun()
	for _, l := range logs {
		f, err := os.Open(l)
		if err != nil {
			merged.failed = "no log at " + l
			continue
		}
		run := parseText(f)
		f.Close()
		for _, name := range run.order {
			merged.tests[name] = run.tests[name]
			merged.order = append(merged.order, name)
		}
	}
	var verdicts []Verdict
	for _, p := range m.Paths {
		if p.Mode == mode {
			verdicts = append(verdicts, judgePath(p, merged))
		}
	}
	return verdicts
}

func main() {
	os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: security-attacks check|run|judge [flags]")
		return 2
	}
	fl := flag.NewFlagSet("security-attacks "+args[0], flag.ContinueOnError)
	fl.SetOutput(stderr)
	root := fl.String("root", ".", "the repository checkout")
	summary := fl.String("summary", "", "append the markdown report to this file ($GITHUB_STEP_SUMMARY)")
	failed := fl.String("failed", "", "write the failed paths' names to this file, one per line")
	timeout := fl.Duration("timeout", 40*time.Minute, "run: go test -timeout for each package")
	mode := fl.String("mode", "", "judge: the live mode to judge ("+modeLiveVM+" or "+modeLiveK8s+")")
	var logs stringList
	fl.Var(&logs, "log", "run: write every test's raw output to this file; judge: a go test -v log to judge (repeatable)")
	if err := fl.Parse(args[1:]); err != nil {
		return 2
	}
	if args[0] == "run" && len(logs) > 1 {
		fmt.Fprintln(stderr, "run takes at most one -log")
		return 2
	}
	m, err := loadManifest(*root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	switch args[0] {
	case "check":
		problems := Check(*root, m)
		for _, p := range problems {
			fmt.Fprintf(stdout, "::error title=security-attacks manifest::%s\n", annotation(p))
		}
		if len(problems) > 0 {
			return 1
		}
		fmt.Fprintf(stdout, "%s covers every test package under %s (%d paths)\n", ManifestPath, securityRoot, len(m.Paths))
		return 0
	case "run":
		if problems := Check(*root, m); len(problems) > 0 {
			for _, p := range problems {
				fmt.Fprintf(stdout, "::error title=security-attacks manifest::%s\n", annotation(p))
			}
			return 1
		}
		log := io.Discard
		if len(logs) == 1 {
			f, err := os.Create(logs[0])
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 2
			}
			defer f.Close()
			log = f
		}
		verdicts := RunInProcess(*root, m, *timeout, log, stdout, goTest)
		n, err := report(verdicts, "Security attack paths (in-process)", stdout, *summary, *failed)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if n > 0 {
			return 1
		}
		return 0
	case "judge":
		if liveJob[*mode] == "" || len(logs) == 0 {
			fmt.Fprintf(stderr, "judge needs -mode %s|%s and at least one -log\n", modeLiveVM, modeLiveK8s)
			return 2
		}
		verdicts := JudgeLive(m, *mode, logs)
		if len(verdicts) == 0 {
			fmt.Fprintf(stderr, "%s names no %s path\n", ManifestPath, *mode)
			return 2
		}
		n, err := report(verdicts, "Security attack paths ("+*mode+")", stdout, *summary, *failed)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if n > 0 {
			return 1
		}
		return 0
	}
	fmt.Fprintf(stderr, "unknown command %q: want check, run or judge\n", args[0])
	return 2
}
