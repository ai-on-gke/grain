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
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testAllowlist = `
clis:
  agy:
    path: /opt/jetski/cli
    paths_under: [ROOT]
    cwd_under: [ROOT/scratch]
    env: [HOME, GRAIN_*]
    pass_env: [HTTPS_PROXY]
    set_env: {AGY_TELEMETRY: "off"}
capabilities:
  corp-search:
    command: /usr/local/bin/corp-search-mcp
    args: [--stdio]
    set_env: {CORP_TOKEN_FILE: /home/me/.corp-token}
`

// testRoot stands in for /var/lib/grain: safepath.Within resolves a root,
// so it has to exist. It holds scratch/ and a link that escapes it.
func testRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scratch", "task-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	return root
}

// mustParse parses s with ROOT replaced by a fresh testRoot, and returns
// both.
func mustParse(t *testing.T, s string) (*Allowlist, string) {
	t.Helper()
	root := testRoot(t)
	a, err := ParseAllowlist([]byte(strings.ReplaceAll(s, "ROOT", root)))
	if err != nil {
		t.Fatalf("ParseAllowlist: %v", err)
	}
	return a, root
}

// mkfiles creates each of names under root, a directory for a name
// ending in "/" and an empty file otherwise: hostd refuses a path that
// does not exist yet.
func mkfiles(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(root, n)
		if strings.HasSuffix(n, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func wantRefused(t *testing.T, err error, substr string) {
	t.Helper()
	var r *RefusedError
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if !strings.Contains(r.Reason, substr) {
		t.Errorf("refusal %q does not mention %q", r.Reason, substr)
	}
}

func TestCheckRefusesWhatTheAllowlistDoesNotName(t *testing.T) {
	a, _ := mustParse(t, testAllowlist)
	for _, req := range []Request{
		{Kind: KindCLI, Name: "claude"},
		{Kind: KindCLI, Name: "corp-search"}, // a capability's name is not a CLI's
		{Kind: KindCapability, Name: "agy"},
		{Kind: "shell", Name: "agy"},
		{Kind: KindCLI, Name: "../agy"},
	} {
		_, err := a.Check(&req, nil)
		wantRefused(t, err, "not on this host's allowlist")
	}
}

func TestCheckRefusesArgvInjectionAndPathTraversal(t *testing.T) {
	a, root := mustParse(t, testAllowlist)
	for _, arg := range []string{
		"/etc/shadow",
		root + "/../../etc/shadow",
		root + "y/x",            // a sibling sharing the prefix is not under it
		root + "/escape/shadow", // a link under the root that points out of it
		"--mcp-config=/etc/grain/hostd.yaml",
		"--mcp-config=../../etc/passwd",
		"../x",
		"./x",
		"~/.ssh/id_rsa",
		"prompt\x00--yolo",
		// The shapes #5030's review showed passing: a relative path that
		// climbs, a short option's attached value, a bare name=value, and
		// another user's home.
		"sub/../../../../etc/passwd",
		"-o/etc/passwd",
		"-o=/etc/passwd",
		"out=/etc/passwd",
		"~root/.ssh/id_rsa",
		"~root",
		"--key=~root/.ssh/id_rsa",
		"..",
		"--dir=..",
		"a:../b",
		root + "/scratch/../../etc", // a ".." even under a root
		"etc/passwd",                // any "/" is a path, and must be absolute
		"fix the bug in /etc/hosts handling, see ../README",
	} {
		_, err := a.Check(&Request{Kind: KindCLI, Name: "agy", Args: []string{"-p", arg}}, nil)
		if err == nil {
			t.Errorf("argument %q was accepted", arg)
			continue
		}
		wantRefused(t, err, "argument 1")
	}
	// What a run actually passes is accepted: flags and their values, and
	// staged files under the data dir. The prompt goes over stdin
	// (pkg/agent/*/hostd_argv_test.go check the real argv).
	mkfiles(t, root, "cli-staging/run-1/mcp.json", "home/agy/settings.json", "out.json")
	spec, err := a.Check(&Request{Kind: KindCLI, Name: "agy", Args: []string{
		"--output-format", "stream-json", "--print-timeout", "1h59m0s", "--tools", "",
		"--allowedTools", "mcp__grain-sandbox__read_file,mcp__grain-sandbox__run_command",
		"--model", "claude-opus-4-5@20251101", "-p", "--max-turns=40",
		"--mcp-config", root + "/cli-staging/run-1/mcp.json",
		"--settings=" + root + "/home/agy/settings.json",
		"-o" + root + "/out.json",
		"out=" + root + "/out.json",
	}}, nil)
	if err != nil {
		t.Fatalf("a run's argv was refused: %v", err)
	}
	if spec.Path != "/opt/jetski/cli" {
		t.Errorf("binary = %q, want the entry's own", spec.Path)
	}
}

func TestCheckRefusesArgsToACapabilityAndAWorkingDirOutsideItsRoots(t *testing.T) {
	a, root := mustParse(t, testAllowlist)
	_, err := a.Check(&Request{Kind: KindCapability, Name: "corp-search", Args: []string{"--debug"}}, nil)
	wantRefused(t, err, "takes no arguments")

	for _, dir := range []string{"/", root, root + "/scratch/../data", root + "/escape", "relative"} {
		_, err := a.Check(&Request{Kind: KindCLI, Name: "agy", Dir: dir}, nil)
		wantRefused(t, err, "working directory")
	}
	spec, err := a.Check(&Request{Kind: KindCLI, Name: "agy", Dir: root + "/scratch/task-1"}, nil)
	if err != nil || spec.Dir != root+"/scratch/task-1" {
		t.Fatalf("Check = %+v, %v", spec, err)
	}
	// A capability has no cwd_under, so it starts in / and may not choose.
	spec, err = a.Check(&Request{Kind: KindCapability, Name: "corp-search"}, nil)
	if err != nil || spec.Dir != "/" || !slices.Equal(spec.Args, []string{"--stdio"}) {
		t.Fatalf("Check = %+v, %v", spec, err)
	}
}

func TestCheckScrubsTheEnvironmentAndRefusesExtraVariables(t *testing.T) {
	a, root := mustParse(t, testAllowlist)
	host := []string{"PATH=/usr/bin", "HOME=/home/me", "HTTPS_PROXY=http://proxy:3128",
		"GITHUB_TOKEN=ghp_secret", "GOOGLE_APPLICATION_CREDENTIALS=/home/me/key.json"}

	for _, env := range [][]string{
		{"GITHUB_TOKEN=x"},
		{"LD_PRELOAD=" + root + "/evil.so"},
		{"GRAIN=x"}, // GRAIN_* does not match GRAIN
		{"HOME"},
		{"HOME=/x\x00y"},
	} {
		_, err := a.Check(&Request{Kind: KindCLI, Name: "agy", Env: env}, host)
		if err == nil {
			t.Errorf("env %q was accepted", env)
			continue
		}
		var r *RefusedError
		if errors.As(err, &r) && strings.Contains(r.Reason, "secret") {
			t.Errorf("refusal %q repeats a value", r.Reason)
		}
	}

	mkfiles(t, root, "home/agy/")
	spec, err := a.Check(&Request{Kind: KindCLI, Name: "agy",
		Env: []string{"HOME=" + root + "/home/agy", "GRAIN_TASK=5"}}, host)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(spec.Env, "\n")
	for _, want := range []string{"PATH=/usr/bin", "HOME=" + root + "/home/agy", "GRAIN_TASK=5",
		"HTTPS_PROXY=http://proxy:3128", "AGY_TELEMETRY=off"} {
		if !strings.Contains(got, want) {
			t.Errorf("env lacks %s:\n%s", want, got)
		}
	}
	for _, not := range []string{"GITHUB_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "HOME=/home/me"} {
		if strings.Contains(got, not) {
			t.Errorf("env carries %s from hostd's own environment:\n%s", not, got)
		}
	}

	// An entry's credentials go to that entry alone.
	spec, err = a.Check(&Request{Kind: KindCapability, Name: "corp-search"}, host)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(spec.Env, "\n"); !strings.Contains(got, "CORP_TOKEN_FILE=") || strings.Contains(got, "HTTPS_PROXY") {
		t.Errorf("capability env = %s", got)
	}
}

const envAllowlist = `
clis:
  agy:
    path: /opt/jetski/cli
    paths_under: [ROOT]
    env: [HOME, GRAIN_*, AGY_*, CONFIG_FILE, PROXY_URL, H*, P*, L*]
    pass_env: [HTTPS_PROXY]
    set_env: {AGY_TELEMETRY: "off"}
  loader:
    path: /opt/loader
    paths_under: [ROOT]
    env: [PATH, LD_LIBRARY_PATH]
`

func TestCheckRefusesEnvTheEntryOwnsOrThatPointsOutsideItsRoots(t *testing.T) {
	a, root := mustParse(t, envAllowlist)
	host := []string{"PATH=/usr/bin", "HOME=/home/me", "HTTPS_PROXY=http://proxy:3128"}
	for _, tc := range []struct {
		name string
		env  string
		want string
	}{
		// A name the entry itself supplies is not the daemon's to replace,
		// even where a pattern in env would admit it.
		{"agy", "AGY_TELEMETRY=enabled", "set_env or pass_env"},
		{"agy", "HTTPS_PROXY=http://evil:3128", "set_env or pass_env"}, // H* admits it
		// Identity variables and LD_* only by their exact name: H*, P* and
		// L* admit none of them.
		{"agy", "PATH=" + root + "/bin", "not one this entry accepts"},
		{"agy", "LD_PRELOAD=" + root + "/evil.so", "not one this entry accepts"},
		{"agy", "LD_LIBRARY_PATH=" + root, "not one this entry accepts"},
		{"agy", "LANG=C", "not one this entry accepts"},
		{"loader", "LD_PRELOAD=" + root + "/evil.so", "not one this entry accepts"},
		{"loader", "HOME=" + root, "not one this entry accepts"},
		// A value that looks like a path must sit under paths_under.
		{"agy", "HOME=/home/me", "paths_under"},
		{"agy", "HOME=~root", "paths_under"},
		{"agy", "HOME=" + root + "/../etc", "paths_under"},
		{"agy", "HOME=" + root + "/escape", "paths_under"},
		{"agy", "CONFIG_FILE=./config.json", "paths_under"},
		{"agy", "CONFIG_FILE=../config.json", "paths_under"},
		{"agy", "CONFIG_FILE=sub/../../../etc/passwd", "climbs out"},
		{"loader", "PATH=" + root + "/bin:/usr/bin", "paths_under"},
		{"loader", "LD_LIBRARY_PATH=/lib", "paths_under"},
	} {
		_, err := a.Check(&Request{Kind: KindCLI, Name: tc.name, Env: []string{tc.env}}, host)
		if err == nil {
			t.Errorf("%s: env %q was accepted", tc.name, tc.env)
			continue
		}
		wantRefused(t, err, tc.want)
		if v := tc.env[strings.Index(tc.env, "=")+1:]; strings.Contains(err.Error(), v) {
			t.Errorf("%s: refusal %q repeats the value", tc.name, err)
		}
	}

	// agy's per-run HOME, named exactly, under the data dir, and values
	// that are not paths at all, pass.
	mkfiles(t, root, "home/agy/run-1/", "bin/", "lib/")
	spec, err := a.Check(&Request{Kind: KindCLI, Name: "agy", Env: []string{
		"HOME=" + root + "/home/agy/run-1", "AGY_CLI_DISABLE_AUTO_UPDATE=true",
		"PROXY_URL=http://proxy:3128/path", "GRAIN_TASK=5", "HOSTNAME_HINT=x",
	}}, host)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(spec.Env, "\n"); !strings.Contains(got, "HOME="+root+"/home/agy/run-1") || !strings.Contains(got, "AGY_TELEMETRY=off") {
		t.Errorf("env = %s", got)
	}
	// An entry that names PATH and LD_LIBRARY_PATH exactly may be sent
	// them, still under its roots.
	if _, err := a.Check(&Request{Kind: KindCLI, Name: "loader", Env: []string{
		"PATH=" + root + "/bin:" + root + "/scratch", "LD_LIBRARY_PATH=" + root + "/lib",
	}}, host); err != nil {
		t.Errorf("an entry naming PATH and LD_LIBRARY_PATH exactly: %v", err)
	}
}

func TestParseAllowlistRefusesEntriesItCouldNotEnforce(t *testing.T) {
	for _, bad := range []string{
		"clis: {agy: {path: cli}}",
		"clis: {agy: {path: /opt/../bin/sh}}",
		"clis: {agy: {command: /bin/sh}}",
		"capabilities: {x: {path: /bin/sh}}",
		"clis: {agy: {path: /bin/agy, paths_under: [/]}}",
		"clis: {agy: {path: /bin/agy, env: [LD_PRELOAD=x]}}",
		"clis: {agy: {path: /bin/agy, typo: 1}}",
		"clis: {'a b': {path: /bin/agy}}",
	} {
		if _, err := ParseAllowlist([]byte(bad)); err == nil {
			t.Errorf("ParseAllowlist(%q) accepted it", bad)
		}
	}
	a, err := ParseAllowlist(nil)
	if err != nil || len(a.Offers()) != 0 {
		t.Errorf("an empty allowlist = %+v, %v; want one that offers nothing", a, err)
	}
}

func TestLoadAllowlistRefusesOneSomebodyElseCouldHaveWritten(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "hostd.yaml")
	if err := os.WriteFile(p, []byte(strings.ReplaceAll(testAllowlist, "ROOT", testRoot(t))), 0o644); err != nil {
		t.Fatal(err)
	}
	me := uint32(os.Getuid())
	if _, err := LoadAllowlist(p, []uint32{me}); err != nil {
		t.Fatalf("a 0644 allowlist its owner may own: %v", err)
	}
	// Production allows root alone, so a file the daemon's own user wrote
	// -- the only user a container running as it could write as -- is
	// refused.
	if me != 0 {
		if _, err := LoadAllowlist(p, []uint32{0}); err == nil || !strings.Contains(err.Error(), "owned by uid") {
			t.Errorf("an allowlist owned by uid %d was accepted for root-only: %v", me, err)
		}
	}
	for _, mode := range []os.FileMode{0o664, 0o646} {
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadAllowlist(p, []uint32{me}); err == nil || !strings.Contains(err.Error(), "writable") {
			t.Errorf("mode %s: err = %v, want a refusal", mode, err)
		}
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAllowlist(link, []uint32{me}); err == nil {
		t.Error("a symlinked allowlist was accepted")
	}
}

// The daemon writes under paths_under, so it can name a path there and
// change what the name points at after Check has looked. The spec hostd
// execs therefore names what each path resolved to, through every link,
// and a path that does not exist yet or is itself a link -- one the
// daemon could make, or remake, point anywhere -- is refused.
func TestCheckPassesTheResolvedPathSoALinkPlantedLaterIsNotFollowed(t *testing.T) {
	a, root := mustParse(t, envAllowlist)
	mkfiles(t, root, "real/run-1/mcp.json", "real/home/")
	// "stage" is a link inside the root, to a directory inside it: allowed,
	// and resolved away.
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "stage")); err != nil {
		t.Fatal(err)
	}
	stage, real := root+"/stage", root+"/real"
	spec, err := a.Check(&Request{Kind: KindCLI, Name: "agy",
		Args: []string{
			"--mcp-config=" + stage + "/run-1/mcp.json",
			"-o" + stage + "/run-1/mcp.json",
			"-o=" + stage + "/run-1/mcp.json",
			"--add-dir", stage + "/home",
			"--model", "x",
		},
		Env: []string{"HOME=" + stage + "/home", "CONFIG_FILE=" + stage + "/run-1/mcp.json:" + stage + "/home"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"--mcp-config=" + real + "/run-1/mcp.json",
		"-o" + real + "/run-1/mcp.json",
		"-o=" + real + "/run-1/mcp.json",
		"--add-dir", real + "/home",
		"--model", "x",
	}
	if !slices.Equal(spec.Args, wantArgs) {
		t.Errorf("args = %q, want %q", spec.Args, wantArgs)
	}
	for _, want := range []string{"HOME=" + real + "/home", "CONFIG_FILE=" + real + "/run-1/mcp.json:" + real + "/home"} {
		if !slices.Contains(spec.Env, want) {
			t.Errorf("env %q lacks %s", spec.Env, want)
		}
	}

	// Now the daemon points the link out of the root. The spec it was
	// given never goes through it.
	if err := os.Remove(filepath.Join(root, "stage")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(root, "stage")); err != nil {
		t.Fatal(err)
	}
	for _, s := range append(append([]string(nil), spec.Args...), spec.Env...) {
		if strings.Contains(s, stage) {
			t.Errorf("spec still names the link: %q", s)
		}
	}
	if b, err := os.ReadFile(real + "/run-1/mcp.json"); err != nil || len(b) != 0 {
		t.Errorf("the checked file now reads %q, %v; want the empty one staged", b, err)
	}
}

func TestCheckRefusesAPathThatDoesNotExistOrIsALink(t *testing.T) {
	a, root := mustParse(t, envAllowlist)
	mkfiles(t, root, "real/file", "real/dir/")
	if err := os.Symlink(filepath.Join(root, "real", "file"), filepath.Join(root, "real", "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		root + "/late",          // missing: the daemon could create it as a link after the check
		root + "/real/late",     // the same under an existing directory
		root + "/missing/late",  // and under a missing one
		root + "/real/link",     // a link, even to a file under the root
		root + "/real/file/sub", // under a file
	} {
		for _, args := range [][]string{{p}, {"--out=" + p}, {"-o" + p}} {
			_, err := a.Check(&Request{Kind: KindCLI, Name: "agy", Args: args}, nil)
			if err == nil {
				t.Errorf("argument %q was accepted", args[0])
				continue
			}
			wantRefused(t, err, "argument 0")
		}
		_, err := a.Check(&Request{Kind: KindCLI, Name: "agy", Env: []string{"CONFIG_FILE=" + p}}, nil)
		if err == nil {
			t.Errorf("env CONFIG_FILE=%s was accepted", p)
			continue
		}
		wantRefused(t, err, "paths_under")
	}

	// A file planted after Check for a name it refused is never handed to
	// the child, because no spec was made. One planted for a name it
	// accepted is not either: that name was the resolved file, not a link.
	late := root + "/late"
	if _, err := a.Check(&Request{Kind: KindCLI, Name: "agy", Args: []string{late}}, nil); err == nil {
		t.Fatal("a path that does not exist yet was accepted")
	}
	if err := os.Symlink("/etc/passwd", late); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Check(&Request{Kind: KindCLI, Name: "agy", Args: []string{late}}, nil); err == nil {
		t.Error("a link planted under the root that points out of it was accepted")
	}

	// A root that is itself a link, named whole, is the operator's own and
	// passes as what it resolves to.
	linkRoot := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(root, linkRoot); err != nil {
		t.Fatal(err)
	}
	b, err := ParseAllowlist([]byte("clis:\n  x:\n    path: /bin/x\n    paths_under: [" + linkRoot + "]\n"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := b.Check(&Request{Kind: KindCLI, Name: "x", Args: []string{linkRoot, linkRoot + "/real/dir"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{root, root + "/real/dir"}; !slices.Equal(spec.Args, want) {
		t.Errorf("args = %q, want %q", spec.Args, want)
	}
}

// Settings' Test starts a host capability server with ProbeEnv set, and
// no entry lists it in env: a capability entry accepts it anyway, and a
// CLI entry does not.
func TestCheckAcceptsTheProbeVariableOnACapabilityOnly(t *testing.T) {
	a, _ := mustParse(t, testAllowlist+`
  grain-host-script:
    command: /usr/local/bin/grain-host-script
`)
	for _, name := range []string{"corp-search", "grain-host-script"} {
		spec, err := a.Check(&Request{Kind: KindCapability, Name: name, Env: []string{ProbeEnv + "=1"}}, nil)
		if err != nil {
			t.Fatalf("%s: probe refused: %v", name, err)
		}
		if !slices.Contains(spec.Env, ProbeEnv+"=1") {
			t.Errorf("%s: env = %q, lacks %s=1", name, spec.Env, ProbeEnv)
		}
	}
	_, err := a.Check(&Request{Kind: KindCapability, Name: "corp-search", Env: []string{"GRAIN_OTHER=1"}}, nil)
	wantRefused(t, err, "not one this entry accepts")

	cli, _ := mustParse(t, envAllowlist)
	_, err = cli.Check(&Request{Kind: KindCLI, Name: "loader", Env: []string{ProbeEnv + "=1"}}, nil)
	wantRefused(t, err, "not one this entry accepts")
}

// capacity: and labels: are read from the allowlist, and refused when the
// daemon could not use them.
func TestParseAllowlistReadsCapacityAndLabels(t *testing.T) {
	a, err := ParseAllowlist([]byte("capacity: 3\nlabels:\n  os: linux\n  gpu: \"yes\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Capacity != 3 || a.Labels["os"] != "linux" || a.Labels["gpu"] != "yes" {
		t.Errorf("allowlist = %+v, want capacity 3 and both labels", a)
	}
	for _, bad := range []string{"capacity: -1\n", "labels:\n  \"a b\": c\n", "labels:\n  k: \"x,y\"\n"} {
		if _, err := ParseAllowlist([]byte(bad)); err == nil {
			t.Errorf("ParseAllowlist(%q) succeeded, want refused", bad)
		}
	}
}

func TestEnvOpenAdmitsArbitraryNamesAndEnforcesFrozenDenySet(t *testing.T) {
	a, root := mustParse(t, `
clis:
  open:
    path: /opt/cli
    paths_under: [ROOT]
    env: ["*"]
    pass_env: [HTTPS_PROXY]
    set_env: {FIXED_FLAG: "1"}
  open_with_home:
    path: /opt/agy
    paths_under: [ROOT]
    env: [HOME, PYTHONPATH, "*"]
`)
	mkfiles(t, root, "home/run-1/", "pylib/", "cfg.json")
	host := []string{"PATH=/usr/bin", "HOME=/home/me", "USER=me", "HTTPS_PROXY=http://proxy:3128"}

	// Arbitrary new CLI variables and valid paths under paths_under pass.
	spec, err := a.Check(&Request{
		Kind: KindCLI,
		Name: "open",
		Env: []string{
			"NEW_CLI_VAR_2027=enabled",
			"GEMINI_API_KEY=secret-key",
			"STAGE_CONFIG=" + root + "/cfg.json",
		},
	}, host)
	if err != nil {
		t.Fatalf("open env check failed: %v", err)
	}
	for _, want := range []string{
		"PATH=/usr/bin",
		"HOME=/home/me",
		"HTTPS_PROXY=http://proxy:3128",
		"FIXED_FLAG=1",
		"NEW_CLI_VAR_2027=enabled",
		"GEMINI_API_KEY=secret-key",
		"STAGE_CONFIG=" + root + "/cfg.json",
	} {
		if !slices.Contains(spec.Env, want) {
			t.Errorf("spec.Env %q lacks %q", spec.Env, want)
		}
	}

	// Every member of the frozen deny set is refused under env: ["*"].
	denied := []string{
		// Identity variables:
		"PATH=" + root,
		"HOME=" + root + "/home/run-1",
		"USER=other",
		"LOGNAME=other",
		"LANG=C",
		"TZ=UTC",
		// Dynamic loader variables:
		"LD_PRELOAD=" + root + "/cfg.json",
		"LD_LIBRARY_PATH=" + root,
		"LD_AUDIT=" + root + "/cfg.json",
		// Shell and interpreter injection variables:
		"BASH_ENV=" + root + "/cfg.json",
		"ENV=" + root + "/cfg.json",
		"NODE_OPTIONS=--require=foo",
		"PYTHONSTARTUP=" + root + "/cfg.json",
		"PYTHONPATH=" + root + "/pylib",
		"PERL5OPT=-Mstrict",
		"RUBYOPT=-w",
	}
	for _, kv := range denied {
		_, err := a.Check(&Request{Kind: KindCLI, Name: "open", Env: []string{kv}}, host)
		wantRefused(t, err, "not one this entry accepts")
	}

	// set_env and pass_env still cannot be overridden under env: ["*"].
	for _, kv := range []string{"HTTPS_PROXY=http://evil:3128", "FIXED_FLAG=0"} {
		_, err := a.Check(&Request{Kind: KindCLI, Name: "open", Env: []string{kv}}, host)
		wantRefused(t, err, "set_env or pass_env")
	}

	// Path-like values still must sit under paths_under under env: ["*"].
	for _, kv := range []string{
		"ANY_PATH=/etc/passwd",
		"ANY_PATH=./cfg.json",
		"ANY_PATH=../cfg.json",
		"ANY_PATH=~/cfg.json",
		"ANY_PATH=" + root + "/escape",
	} {
		_, err := a.Check(&Request{Kind: KindCLI, Name: "open", Env: []string{kv}}, host)
		wantRefused(t, err, "paths_under")
	}

	// Exact names keep working alongside "*", while other denied names stay refused.
	spec, err = a.Check(&Request{
		Kind: KindCLI,
		Name: "open_with_home",
		Env: []string{
			"HOME=" + root + "/home/run-1",
			"PYTHONPATH=" + root + "/pylib",
			"AGY_EXTRA_VAR=ok",
		},
	}, host)
	if err != nil {
		t.Fatalf("open_with_home check failed: %v", err)
	}
	if !slices.Contains(spec.Env, "HOME="+root+"/home/run-1") || !slices.Contains(spec.Env, "PYTHONPATH="+root+"/pylib") {
		t.Errorf("spec.Env = %q, want overridden HOME and PYTHONPATH", spec.Env)
	}
	for _, kv := range []string{"PATH=" + root, "LD_PRELOAD=" + root + "/cfg.json", "NODE_OPTIONS=--inspect"} {
		_, err := a.Check(&Request{Kind: KindCLI, Name: "open_with_home", Env: []string{kv}}, host)
		wantRefused(t, err, "not one this entry accepts")
	}

	// Malformed wildcard patterns are still rejected at parse time.
	for _, bad := range []string{
		`clis: {x: {path: /bin/x, env: ["**"]}}`,
		`clis: {x: {path: /bin/x, env: ["*FOO"]}}`,
	} {
		if _, err := ParseAllowlist([]byte(bad)); err == nil {
			t.Errorf("ParseAllowlist(%q) succeeded, want refused", bad)
		}
	}
}

func TestEnvPatternsNeverAdmitInjectionVariables(t *testing.T) {
	a, root := mustParse(t, `
clis:
  patterned:
    path: /opt/cli
    paths_under: [ROOT]
    env: [PYTHON*, NODE_*, BASH_*, PERL*, RUBY*, E*]
`)
	mkfiles(t, root, "cfg.json")
	host := []string{"PATH=/usr/bin", "HOME=/home/me"}

	// A NAME_* pattern admits the names it matches outside the deny set...
	if _, err := a.Check(&Request{
		Kind: KindCLI,
		Name: "patterned",
		Env:  []string{"PYTHONUNBUFFERED=1", "NODE_ENV=production", "BASH_SILENCE=1", "EDITOR=true"},
	}, host); err != nil {
		t.Fatalf("patterned check failed: %v", err)
	}
	// ...but never an injection variable, which only its exact name admits.
	for _, kv := range []string{
		"PYTHONPATH=" + root,
		"PYTHONSTARTUP=" + root + "/cfg.json",
		"NODE_OPTIONS=--require=foo",
		"BASH_ENV=" + root + "/cfg.json",
		"PERL5OPT=-Mstrict",
		"RUBYOPT=-w",
		"ENV=" + root + "/cfg.json",
	} {
		_, err := a.Check(&Request{Kind: KindCLI, Name: "patterned", Env: []string{kv}}, host)
		wantRefused(t, err, "not one this entry accepts")
	}
}

func TestParseAllowlistIgnoresXAnnotationKeysAndKeepsStrictnessElsewhere(t *testing.T) {
	root := testRoot(t)
	yamlWithAnnotations := strings.ReplaceAll(`
x-notes:
  owner: workstation-team
  items: [1, 2, 3]
x-shared-roots: &roots [ROOT]
clis:
  agy:
    path: /opt/agy/bin/agy
    paths_under: *roots
    cwd_under: *roots
    env: ["*"]
    x-installed-by: grain-agentd
  x-custom-cli:
    path: /opt/custom/cli
    x-meta: {experimental: true}
capabilities:
  corp-search:
    command: /usr/local/bin/corp-search
    x-ticket: issue-12345
`, "ROOT", root)
	a, err := ParseAllowlist([]byte(yamlWithAnnotations))
	if err != nil {
		t.Fatalf("ParseAllowlist with x- keys failed: %v", err)
	}
	if a.Lookup(KindCLI, "agy") == nil || a.Lookup(KindCLI, "x-custom-cli") == nil || a.Lookup(KindCapability, "corp-search") == nil {
		t.Fatalf("offers = %+v, want agy, x-custom-cli and corp-search", a.Offers())
	}
	if !slices.Equal(a.CLIs["agy"].PathsUnder, []string{root}) {
		t.Errorf("x-shared-roots anchor resolved to %v, want [%s]", a.CLIs["agy"].PathsUnder, root)
	}
	if onlyX, err := ParseAllowlist([]byte("x-only: true\n")); err != nil || len(onlyX.Offers()) != 0 {
		t.Errorf("an allowlist with only x- keys = %+v, %v", onlyX, err)
	}

	// Non-x- unknown keys are still strictly refused, even beside x- keys.
	for _, bad := range []string{
		"x-note: ok\ntypo_top: 1\n",
		"x-note: ok\nclis:\n  agy:\n    path: /bin/agy\n    x-ok: 1\n    extra_arg: true\n",
		"capabilities:\n  c:\n    command: /bin/c\n    x-ok: 1\n    max_dur: 5m\n",
		"clis:\n  agy:\n    path: /bin/agy\n    set_env:\n      x-not-env: 1\n",
	} {
		if _, err := ParseAllowlist([]byte(bad)); err == nil {
			t.Errorf("ParseAllowlist(%q) succeeded, want strict refusal", bad)
		}
	}
}

func TestParseAllowlistExpandsHomeRelativePaths(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	mkfiles(t, home, ".local/bin/agy", ".grain/hostd-stage/work/")

	a, err := ParseAllowlist([]byte(`
clis:
  agy:
    path: ~/.local/bin/agy
    paths_under: [~/.grain/hostd-stage]
    cwd_under: [~/.grain/hostd-stage]
    env: [HOME, "*"]
`))
	if err != nil {
		t.Fatalf("ParseAllowlist with ~/ paths failed: %v", err)
	}
	wantBin := filepath.Join(home, ".local/bin/agy")
	wantStage := filepath.Join(home, ".grain/hostd-stage")
	e := a.Lookup(KindCLI, "agy")
	if e.Path != wantBin || !slices.Equal(e.PathsUnder, []string{wantStage}) || !slices.Equal(e.CwdUnder, []string{wantStage}) {
		t.Fatalf("expanded entry = %+v, want path %s and stage %s", e, wantBin, wantStage)
	}
	workDir := filepath.Join(wantStage, "work")
	spec, err := a.Check(&Request{
		Kind: KindCLI,
		Name: "agy",
		Args: []string{"--add-dir", workDir},
		Env:  []string{"HOME=" + workDir, "EXTRA_FLAG=1"},
		Dir:  workDir,
	}, nil)
	if err != nil {
		t.Fatalf("Check against ~/ entry failed: %v", err)
	}
	if spec.Path != wantBin || spec.Dir != workDir {
		t.Errorf("spec = %+v, want path %s and dir %s", spec, wantBin, workDir)
	}

	// Unsafe ~ forms and capability command: with ~/ stay refused.
	for _, bad := range []string{
		"clis: {agy: {path: ~}}",
		"clis: {agy: {path: ~/}}",
		"clis: {agy: {path: ~other/bin/agy}}",
		"clis: {agy: {path: ~/.local/../bin/agy}}",
		"clis: {agy: {path: /bin/agy, paths_under: [~/../etc]}}",
		"clis: {agy: {path: /bin/agy, cwd_under: [~other/work]}}",
		"capabilities: {c: {command: ~/.local/bin/cap}}",
	} {
		if _, err := ParseAllowlist([]byte(bad)); err == nil {
			t.Errorf("ParseAllowlist(%q) succeeded, want refused", bad)
		}
	}
}

func TestCLIEntryYAMLWritesEnvOpenOnlyWhenPeerAdvertisesIt(t *testing.T) {
	home := "/home/alice"
	stage := "/home/alice/.grain/hostd-stage"
	closed := []string{"CODEX_HOME", "OPENAI_API_KEY"}
	agyClosed := []string{"HOME", "AGY_CLI_DISABLE_AUTO_UPDATE", "GEMINI_API_KEY"}

	modern := ThisHello("v3.5.0")
	gotCodex := CLIEntryYAML("codex", "/home/alice/.local/bin/codex", home, []string{stage}, closed, nil, modern)
	if !strings.Contains(gotCodex, "path: ~/.local/bin/codex\n") ||
		!strings.Contains(gotCodex, "paths_under: [~/.grain/hostd-stage]\n") ||
		!strings.Contains(gotCodex, `env: ["*"]`+"\n") {
		t.Errorf("modern codex snippet:\n%s", gotCodex)
	}
	gotAgy := CLIEntryYAML("agy", "/home/alice/.local/bin/agy", home, []string{stage}, agyClosed, nil, modern)
	if !strings.Contains(gotAgy, `env: [HOME, "*"]`+"\n") {
		t.Errorf("modern agy snippet must retain exact HOME beside *:\n%s", gotAgy)
	}

	old := Hello{Protocol: 4}
	gotOld := CLIEntryYAML("codex", "/home/alice/.local/bin/codex", home, []string{stage}, closed, nil, old)
	if strings.Contains(gotOld, `"*"`) || strings.Contains(gotOld, "~/") ||
		!strings.Contains(gotOld, "env: [CODEX_HOME, OPENAI_API_KEY]\n") {
		t.Errorf("legacy host snippet used env_open syntax:\n%s", gotOld)
	}
}

func TestParseAllowlistLoginsMap(t *testing.T) {
	absent, err := ParseAllowlist([]byte("clis: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if absent.Logins != nil {
		t.Fatalf("absent logins: = %#v, want nil so built-in defaults apply", absent.Logins)
	}

	empty, err := ParseAllowlist([]byte("logins: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Logins == nil || len(empty.Logins) != 0 {
		t.Fatalf("empty logins: = %#v, want non-nil empty map", empty.Logins)
	}

	a, err := ParseAllowlist([]byte("logins:\n  codex: ~/.codex/auth.json\n  custom-cli: /opt/creds/login.json\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Logins["codex"] != "~/.codex/auth.json" || a.Logins["custom-cli"] != "/opt/creds/login.json" {
		t.Fatalf("logins = %#v", a.Logins)
	}

	for _, bad := range []string{
		"logins:\n  \"bad name\": ~/.codex/auth.json\n",
		"logins:\n  \"\": ~/.codex/auth.json\n",
		"logins:\n  codex: \"\"\n",
		"logins:\n  codex: /\n",
		"logins:\n  codex: ~\n",
		"logins:\n  codex: ~/\n",
		"logins:\n  codex: ~/../etc/passwd\n",
		"logins:\n  codex: ~other/.codex/auth.json\n",
		"logins:\n  codex: relative/auth.json\n",
		"logins:\n  codex: /home/me/../etc/passwd\n",
		"loginz:\n  codex: ~/.codex/auth.json\n",
	} {
		if _, err := ParseAllowlist([]byte(bad)); err == nil {
			t.Errorf("ParseAllowlist(%q) succeeded, want refused", bad)
		}
	}
}
