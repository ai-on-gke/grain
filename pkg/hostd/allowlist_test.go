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
