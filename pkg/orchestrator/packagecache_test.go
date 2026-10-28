package orchestrator

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/kubectlgate"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/pkgcache"
)

// commandedSandbox is a sandbox that records every command the
// controller runs inside it, which is the whole of what configuring the
// package cache does.
type commandedSandbox struct {
	commands [][]string
	stdins   []string
	// exitCode and stderr are what every command answers with, from the
	// failFrom'th onwards -- which is how a test fails the tool commands
	// without failing the file writes before them.
	exitCode int
	stderr   string
	failFrom int
	// stdout is what every command in this sandbox prints, which for the
	// one command that reads anything is the daemon.json already there.
	stdout string
}

func (s *commandedSandbox) Name() string { return "sandbox-1" }
func (s *commandedSandbox) Tools(context.Context) ([]mcp.Tool, error) {
	return nil, nil
}
func (s *commandedSandbox) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}
func (s *commandedSandbox) Release(context.Context) error { return nil }
func (s *commandedSandbox) RunInSandbox(_ context.Context, argv []string, stdin string) (string, string, int) {
	s.commands = append(s.commands, argv)
	s.stdins = append(s.stdins, stdin)
	if len(s.commands) <= s.failFrom {
		return s.stdout, "", 0
	}
	return s.stdout, s.stderr, s.exitCode
}

// plainSandbox has no command transport at all -- a directory on the
// controller's own machine, whose /etc is this host's and not grain's to
// rewrite.
type plainSandbox struct{}

func (plainSandbox) Name() string { return "sandbox-2" }
func (plainSandbox) Tools(context.Context) ([]mcp.Tool, error) {
	return nil, nil
}
func (plainSandbox) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}
func (plainSandbox) Release(context.Context) error { return nil }

// systemCommands is how many of ConfigurePackageCache's commands configure
// the system files and services -- apt's file, pip's file, ghcr's file, gcr's file,
// the child-container settings file, docker daemon's read/write, and docker
// daemon's reload -- as against the per-tool ones (pkgcache.SandboxCommands)
// that follow them and run as the sandbox's own account.
//
// The docker *client* is not among them, deliberately: a default proxy in
// ~/.docker/config.json reaches every container the sandbox runs, as one
// proxy for the whole internet, and the cache is an allowlisted plain-HTTP
// archive service. See configureDockerCache.
const systemCommands = 8

func TestConfigurePackageCacheSkipsASandboxItCannotCommand(t *testing.T) {
	configured, err := ConfigurePackageCache(context.Background(), plainSandbox{}, "http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("a sandbox with no command transport is not an error: %v", err)
	}
	if configured {
		t.Error("ConfigurePackageCache claimed to have configured a host directory")
	}
}

func TestConfigurePackageCacheWritesEveryFileAsRoot(t *testing.T) {
	sandbox := &commandedSandbox{}
	configured, err := ConfigurePackageCache(context.Background(), sandbox, "http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("ConfigurePackageCache: %v", err)
	}
	if !configured {
		t.Fatal("ConfigurePackageCache reported doing nothing")
	}
	// apt, pip, ghcr hosts, gcr hosts, docker daemon read-merge-write, and
	// the reload after it. The per-tool commands after those are their own
	// test below, and are not run through root.
	commands, err := pkgcache.SandboxCommands("http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("SandboxCommands: %v", err)
	}
	if len(sandbox.commands) != systemCommands+len(commands) {
		t.Fatalf("ran %d commands: %v", len(sandbox.commands), sandbox.commands)
	}
	var written []string
	for _, argv := range sandbox.commands[:systemCommands] {
		script := argv[2]
		if strings.Contains(script, "install -m") {
			written = append(written, argv[len(argv)-2])
		}
	}
	if len(written) != 6 || written[0] != pkgcache.AptConfigPath || written[1] != pkgcache.PipConfigPath ||
		written[2] != pkgcache.GHCRHostsConfigPath || written[3] != pkgcache.GCRHostsConfigPath ||
		written[4] != pkgcache.ChildContainerEnvPath || written[5] != pkgcache.DockerConfigPath {
		t.Errorf("wrote %v, want apt's, pip's, ghcr's, gcr's, the child-container settings' and docker's configuration", written)
	}
	if !strings.Contains(sandbox.stdins[0], "10.9.0.1:41234") {
		t.Errorf("the apt configuration does not name the cache:\n%s", sandbox.stdins[0])
	}
	if !strings.Contains(sandbox.stdins[1], "index-url = http://10.9.0.1:41234/pypi/simple/") {
		t.Errorf("the pip configuration does not point at the cache:\n%s", sandbox.stdins[1])
	}
	if !strings.Contains(sandbox.stdins[2], "http://10.9.0.1:41234/v2/ghcr") {
		t.Errorf("the ghcr configuration does not point at the cache:\n%s", sandbox.stdins[2])
	}
	if !strings.Contains(sandbox.stdins[3], "http://10.9.0.1:41234/v2/gcr") {
		t.Errorf("the gcr configuration does not point at the cache:\n%s", sandbox.stdins[3])
	}
	// The child-container settings, which are the one file here nothing
	// applies on the run's behalf: per-ecosystem names pointed at this
	// service, and no proxy among them.
	if !strings.Contains(sandbox.stdins[4], "PIP_INDEX_URL=http://10.9.0.1:41234/pypi/simple/") ||
		!strings.Contains(sandbox.stdins[4], "GOPROXY=http://10.9.0.1:41234/go,direct") {
		t.Errorf("the child-container settings do not point at the cache:\n%s", sandbox.stdins[4])
	}
	if !strings.Contains(sandbox.stdins[6], `"registry-mirrors"`) ||
		!strings.Contains(sandbox.stdins[6], "http://10.9.0.1:41234") {
		t.Errorf("the docker daemon configuration does not name the cache as a registry mirror:\n%s", sandbox.stdins[6])
	}
	// Nothing here writes ~/.docker/config.json, and nothing should: a
	// proxy in that file is handed to every container the sandbox builds
	// or runs, as http_proxy for the whole internet, and the cache serves
	// an allowlist of archives over plain HTTP. Asserted on what was
	// written rather than left to a count, so that putting it back is a
	// failing test rather than a quiet regression.
	//
	// Nor does anything written here *set* a proxy variable by any other
	// route: the child-container file hands a build per-ecosystem names and
	// would undo the whole point of them if it smuggled an HTTP_PROXY in
	// beside. Matched on an assignment at the start of a line rather than on
	// the name anywhere, because that file's own comment says the word while
	// explaining why it is not there.
	for i, stdin := range sandbox.stdins {
		if strings.Contains(stdin, `"proxies"`) {
			t.Errorf("command %d writes a docker proxy configuration, which every container would inherit:\n%s", i, stdin)
		}
		for _, name := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
			for _, line := range strings.Split(stdin, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), name+"=") {
					t.Errorf("command %d sets %s, which is a proxy for the whole internet rather than "+
						"a setting per ecosystem:\n%s", i, name, stdin)
				}
			}
		}
	}
	if !strings.Contains(sandbox.commands[7][2], "systemctl reload docker") {
		t.Errorf("nothing makes a running dockerd re-read its configuration: %q", sandbox.commands[7][2])
	}
}

func TestConfigurePackageCacheKeepsTheSandboxsOwnDockerConfiguration(t *testing.T) {
	sandbox := &commandedSandbox{stdout: `{"bip":"172.30.255.1/24"}`}
	if _, err := ConfigurePackageCache(context.Background(), sandbox, "http://10.9.0.1:41234"); err != nil {
		t.Fatalf("ConfigurePackageCache: %v", err)
	}
	if !strings.Contains(sandbox.stdins[6], `"bip": "172.30.255.1/24"`) {
		t.Errorf("the merge dropped the guest's own docker bridge configuration:\n%s", sandbox.stdins[6])
	}
}

func TestConfigurePackageCacheReportsADockerConfigItCannotParse(t *testing.T) {
	sandbox := &commandedSandbox{stdout: "this is not json"}
	configured, err := ConfigurePackageCache(context.Background(), sandbox, "http://10.9.0.1:41234")
	if err == nil {
		t.Fatal("a daemon.json this cannot merge into should be reported rather than overwritten")
	}
	if configured {
		t.Error("ConfigurePackageCache claimed to have configured docker")
	}
	for i, stdin := range sandbox.stdins {
		if strings.Contains(sandbox.commands[i][len(sandbox.commands[i])-2], pkgcache.DockerConfigPath) && stdin != "" {
			t.Errorf("daemon.json was written anyway:\n%s", stdin)
		}
	}
}

// TestWriteAsRootScriptActuallyWritesTheFile runs the script rather than
// reading it, because the thing it got wrong for its whole first life was
// which positional parameter the path arrives in -- a mistake no
// assertion about argv can see, and one that turned every dispatch's
// package-cache configuration into a logged warning.
func TestWriteAsRootScriptActuallyWritesTheFile(t *testing.T) {
	dir := t.TempDir()
	// A `sudo` of our own on PATH, so the non-root branch is exercised by
	// a test that is not root and the root branch by one that is.
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\n[ \"$1\" = -n ] && shift\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("writing a sudo stub: %v", err)
	}
	// And, on a host whose userland is not a sandbox's, an `install` of our
	// own too -- see gnuInstallStub.
	if runtime.GOOS != "linux" {
		writePATHStub(t, dir, "install", gnuInstallStub)
	}
	target := filepath.Join(dir, "etc", "pip.conf")
	cmd := exec.Command("sh", "-c", writeAsRootScript, "sh", target, "0644")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	cmd.Stdin = strings.NewReader("[global]\nindex-url = http://10.9.0.1:41234/pypi/simple/\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the script failed: %v\n%s", err, out)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("nothing was written: %v", err)
	}
	if !strings.Contains(string(content), "index-url") {
		t.Errorf("the file holds %q", content)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644 -- the mode is the argument after the path", info.Mode().Perm())
	}
}

// gnuInstallStub is as much of GNU coreutils' install(1) as
// writeAsRootScript uses, for the hosts that have a different one.
//
// Every sandbox grain writes into is Linux, so writeAsRootScript is a
// GNU/Linux script, and `install -m MODE /dev/stdin PATH` is GNU install
// copying from a pipe. BSD install -- macOS's -- will not open a source
// that is not a regular file:
//
//	install: /dev/stdin: Inappropriate file type or format
//
// and exits 71, sysexits.h's EX_OSERR, which is the whole of what the
// test above could say about it ("the script failed: exit status 71").
//
// Neither of the other two ways out is acceptable here. Skipping on
// darwin would leave macOS developers with no cover against precisely the
// bug this test exists for -- an argument in the wrong position, which
// nothing but running the script can see. And rewriting the script around
// something both userlands have would cost the sandbox the two properties
// `install` is chosen for: the file appears with its mode already set,
// never briefly at root's umask, and it appears as a *new* inode rather
// than a write through the old one -- that second one being
// writeInPlaceScript's job and the reason these are two scripts and not
// one. Getting the same from cat would take a `sudo -n sh -c` wrapper
// around a redirection, a temp name to move from, and a chmod, in
// production code, to fix a host no sandbox runs on.
//
// So the host stands in for a sandbox's install instead. The stub is
// deliberately strict about its arguments rather than forgiving: a path
// handed to -m has to stay an error, or the swap this test watches for
// would go through and write a file named "0644" somewhere instead.
const gnuInstallStub = `#!/bin/sh
mode=
while [ $# -gt 2 ]; do
	case "$1" in
	-m) mode=$2; shift 2 ;;
	*) break ;;
	esac
done
case "$mode" in
'' | *[!0-7]*)
	echo "install stub: want -m with an octal mode, got \"$mode\"" >&2
	exit 1
	;;
esac
if [ $# -ne 2 ]; then
	echo "install stub: want [-m MODE] SOURCE DEST, got $# operands: $*" >&2
	exit 1
fi
rm -f "$2" || exit 1
cat "$1" >"$2" || exit 1
chmod "$mode" "$2" || exit 1
`

// The resolv.conf write, run rather than read for the same reason, and
// asserted on the one property that distinguishes it: the file it wrote
// is the file that was there.
//
// A container's /etc/resolv.conf is docker's own bind mount, and the
// `install` this used to use renames a new file over its target -- which
// across a mount point is EBUSY ("cannot remove '/etc/resolv.conf':
// Device or resource busy"), a warning in the log, and a sandbox that was
// never pointed at the interception resolver at all. There is no bind
// mount in a unit test, so the stand-in for one is a hard link: a write
// through the existing inode is visible through the other name, and a
// write that replaced the file is not.
func TestTheResolvConfWriteReplacesTheContentsAndNotTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\n[ \"$1\" = -n ] && shift\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("writing a sudo stub: %v", err)
	}
	target := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(target, []byte("nameserver 8.8.8.8\n"), 0o644); err != nil {
		t.Fatalf("writing the sandbox's own resolv.conf: %v", err)
	}
	elsewhere := filepath.Join(dir, "the-same-file")
	if err := os.Link(target, elsewhere); err != nil {
		t.Skipf("this filesystem has no hard links, so there is no way to tell the two apart: %v", err)
	}

	cmd := exec.Command("sh", "-c", writeInPlaceScript, "sh", target)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	cmd.Stdin = strings.NewReader("nameserver 172.17.0.1\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the script failed: %v\n%s", err, out)
	}

	for _, path := range []string{target, elsewhere} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if strings.TrimSpace(string(content)) != "nameserver 172.17.0.1" {
			t.Errorf("%s holds %q, want the cache's resolver -- and the same contents through both "+
				"names, or the file the sandbox is really using was not the one written",
				filepath.Base(path), content)
		}
	}
}

// TestReadAsRootScriptPrintsAFileOrNothing is the same for the read half:
// a sandbox with no daemon.json must come back empty rather than failing,
// since that is the common case.
func TestReadAsRootScriptPrintsAFileOrNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte("#!/bin/sh\n[ \"$1\" = -n ] && shift\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("writing a sudo stub: %v", err)
	}
	run := func(path string) (string, error) {
		cmd := exec.Command("sh", "-c", readAsRootScript, "sh", path)
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		out, err := cmd.Output()
		return string(out), err
	}
	missing, err := run(filepath.Join(dir, "no-such-file"))
	if err != nil {
		t.Fatalf("reading a file that is not there failed: %v", err)
	}
	if missing != "" {
		t.Errorf("a missing file printed %q", missing)
	}
	existing := filepath.Join(dir, "daemon.json")
	if err := os.WriteFile(existing, []byte(`{"bip":"172.30.255.1/24"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := run(existing)
	if err != nil {
		t.Fatalf("reading the file failed: %v", err)
	}
	if !strings.Contains(got, "172.30.255.1/24") {
		t.Errorf("read %q", got)
	}
}

// The ecosystems configured by their own tool rather than by a file grain
// writes: each is run only if that tool is installed, so a sandbox with no
// Go toolchain in it is left alone rather than reported as a failure.
func TestConfigurePackageCacheConfiguresTheToolsThatOwnTheirConfig(t *testing.T) {
	sandbox := &commandedSandbox{}
	if _, err := ConfigurePackageCache(context.Background(), sandbox, "http://10.9.0.1:41234"); err != nil {
		t.Fatalf("ConfigurePackageCache: %v", err)
	}
	commands, err := pkgcache.SandboxCommands("http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("SandboxCommands: %v", err)
	}
	ran := sandbox.commands[len(sandbox.commands)-len(commands):]
	if len(ran) != len(commands) {
		t.Fatalf("ran %d tool commands, want %d", len(ran), len(commands))
	}
	for i, argv := range ran {
		if !strings.Contains(argv[2], "command -v") {
			t.Errorf("tool command %d runs whether or not the tool is installed: %q", i, argv[2])
		}
		if got := argv[4:]; strings.Join(got, " ") != strings.Join(commands[i].Argv, " ") {
			t.Errorf("tool command %d = %v, want %v", i, got, commands[i].Argv)
		}
	}
	if !strings.Contains(strings.Join(ran[0], " "), "GOPROXY=http://10.9.0.1:41234/go,direct") {
		t.Errorf("the Go toolchain is not pointed at the cache: %v", ran[0])
	}
	if !strings.Contains(strings.Join(ran[0], " "), "GOCACHEPROG=/tmp/grain-gocacheprog http://10.9.0.1:41234") {
		t.Errorf("the Go toolchain GOCACHEPROG is not pointed at the cache: %v", ran[0])
	}
	if !strings.Contains(ran[0][2], pkgcache.GoCacheProgPath) {
		t.Errorf("configureToolScript does not build %s: %q", pkgcache.GoCacheProgPath, ran[0][2])
	}
}

// A tool that refuses the setting is a log line, not a failed dispatch:
// the files are already written and the other ecosystems still cache.
func TestConfigurePackageCacheSurvivesAToolThatRefuses(t *testing.T) {
	sandbox := &commandedSandbox{exitCode: 1, stderr: "go: not a valid GOPROXY", failFrom: systemCommands}
	configured, err := ConfigurePackageCache(context.Background(), sandbox, "http://10.9.0.1:41234")
	if err != nil {
		t.Fatalf("a tool that refused its setting should not fail the dispatch: %v", err)
	}
	if !configured {
		t.Error("ConfigurePackageCache reported doing nothing, though it wrote every file")
	}
}

func TestConfigurePackageCacheDoesNothingWhenOff(t *testing.T) {
	sandbox := &commandedSandbox{}
	configured, err := ConfigurePackageCache(context.Background(), sandbox, "")
	if err != nil {
		t.Fatalf("ConfigurePackageCache: %v", err)
	}
	if configured || len(sandbox.commands) != 0 {
		t.Errorf("a cache that is switched off should leave the sandbox alone, ran %v", sandbox.commands)
	}
}

func TestConfigurePackageCacheReportsAFailedWrite(t *testing.T) {
	sandbox := &commandedSandbox{exitCode: 1, stderr: "sudo: a password is required"}
	configured, err := ConfigurePackageCache(context.Background(), sandbox, "http://10.9.0.1:41234")
	if err == nil {
		t.Fatal("a sandbox that refused the write should be reported")
	}
	if configured {
		t.Error("ConfigurePackageCache claimed to have configured a sandbox it could not write to")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("the error does not carry what the sandbox said: %v", err)
	}
}

func TestConfigurePackageCacheRefusesAnUnusableAddress(t *testing.T) {
	sandbox := &commandedSandbox{}
	if _, err := ConfigurePackageCache(context.Background(), sandbox, "not a url"); err == nil {
		t.Fatal("an address no sandbox could fetch from should be refused rather than written")
	}
}

// resolvConfWrites is what a sandbox was told to put in /etc/resolv.conf,
// one entry per write of that file.
func resolvConfWrites(sandbox *commandedSandbox) []string {
	var out []string
	for i, argv := range sandbox.commands {
		for _, arg := range argv {
			if arg == pkgcache.ResolvConfPath {
				out = append(out, sandbox.stdins[i])
			}
		}
	}
	return out
}

func TestConfigurePackageCacheTransparentPointsDNSAtTheCache(t *testing.T) {
	sandbox := &commandedSandbox{}
	if _, err := ConfigurePackageCacheTransparent(context.Background(), sandbox,
		"http://10.9.0.1:41234", nil, "172.17.0.1"); err != nil {
		t.Fatalf("ConfigurePackageCacheTransparent: %v", err)
	}
	writes := resolvConfWrites(sandbox)
	if len(writes) != 1 {
		t.Fatalf("wrote %s %d times, want once", pkgcache.ResolvConfPath, len(writes))
	}
	if !strings.Contains(writes[0], "nameserver 172.17.0.1") {
		t.Errorf("resolv.conf written as %q, want the cache's resolver", writes[0])
	}
}

// A sandbox whose resolv.conf grain replaces with a nameserver nobody is
// listening on has no name resolution at all, which is worse than one
// with no interception. So when the controller has no resolver to offer
// -- which is what an empty dnsIP means -- the file has to be left
// exactly as the sandbox's own image wrote it.
func TestConfigurePackageCacheTransparentLeavesResolvConfAloneWithoutAResolver(t *testing.T) {
	sandbox := &commandedSandbox{}
	configured, err := ConfigurePackageCacheTransparent(context.Background(), sandbox,
		"http://10.9.0.1:41234", nil, "")
	if err != nil {
		t.Fatalf("ConfigurePackageCacheTransparent: %v", err)
	}
	if !configured {
		t.Fatal("the rest of the package cache configuration should still have been applied")
	}
	if writes := resolvConfWrites(sandbox); len(writes) != 0 {
		t.Errorf("rewrote the sandbox's resolver with no resolver to point it at: %q", writes)
	}
}

type envShellSandbox struct {
	env      []string
	progPath string
}

func (s *envShellSandbox) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	// Skip npm in this focused Go cache prog test so we don't mutate the test runner's ~/.npmrc.
	if len(argv) >= 5 && argv[4] == "npm" {
		return "", "", 0
	}
	rewritten := make([]string, len(argv))
	for i, a := range argv {
		if s.progPath != "" {
			a = strings.ReplaceAll(a, pkgcache.GoCacheProgPath, s.progPath)
		}
		rewritten[i] = a
	}
	cmd := exec.CommandContext(ctx, rewritten[0], rewritten[1:]...)
	cmd.Env = s.env
	cmd.Stdin = strings.NewReader(stdin)
	var outBuf, errBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = 1
		}
	}
	return outBuf.String(), errBuf.String(), code
}

func TestConfigureToolsCompilesAndConfiguresGoCacheProg(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skipf("go toolchain not installed: %v", err)
	}

	cache, err := pkgcache.NewCache(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(pkgcache.NewServer(pkgcache.Config{Cache: cache}))
	defer srv.Close()

	tmpDir := t.TempDir()
	goEnvFile := filepath.Join(tmpDir, "go.env")
	localCacheDir := filepath.Join(tmpDir, "local-gocache")
	progPath := filepath.Join(tmpDir, "grain-gocacheprog")
	env := append(os.Environ(),
		"GOENV="+goEnvFile,
		"GRAIN_GOCACHE_DIR="+localCacheDir,
	)

	// Redirect GoCacheProgPath into tmpDir so this test never overwrites or
	// deletes the sandbox's own live /tmp/grain-gocacheprog helper binary
	// while other test packages run `go build`.
	sb := &envShellSandbox{env: env, progPath: progPath}
	if err := configureTools(context.Background(), sb, "sandbox-gocache", srv.URL); err != nil {
		t.Fatalf("configureTools: %v", err)
	}

	// Verify the GOCACHEPROG helper was compiled and is executable.
	info, err := os.Stat(progPath)
	if err != nil {
		t.Fatalf("stat %s: %v", progPath, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("%s mode = %v, want executable", progPath, info.Mode())
	}

	// Verify `go env GOCACHEPROG` reads the configured helper and URL from GOENV.
	envCmd := exec.Command("go", "env", "GOCACHEPROG")
	envCmd.Env = env
	gotEnv, err := envCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go env GOCACHEPROG: %v\n%s", err, gotEnv)
	}
	wantEnv := progPath + " " + srv.URL
	if strings.TrimSpace(string(gotEnv)) != wantEnv {
		t.Fatalf("go env GOCACHEPROG = %q, want %q", strings.TrimSpace(string(gotEnv)), wantEnv)
	}

	// Run a real `go build` using that GOENV and confirm artifacts populate pkgcache.
	modDir := filepath.Join(tmpDir, "mod")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "go.mod"), []byte("module example.com/orchtest\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "lib.go"), []byte("package orchtest\n\nfunc Value() int { return 42 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buildCmd := exec.Command("go", "build", "./...")
	buildCmd.Dir = modDir
	buildCmd.Env = append(env, "GOCACHE="+filepath.Join(tmpDir, "fallback-gocache"))
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build with configured GOCACHEPROG: %v\n%s", err, out)
	}
	if cache.Entries() == 0 {
		t.Fatalf("expected pkgcache to hold Go build artifacts after go build, got 0 entries")
	}
}

// timeoutOnceSandbox answers its first command the way a `kubectl exec`
// that timed out does, and every later one with success.
type timeoutOnceSandbox struct{ commandedSandbox }

func (s *timeoutOnceSandbox) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	s.commandedSandbox.RunInSandbox(ctx, argv, stdin)
	if len(s.commands) == 1 {
		return "", "error: Timeout occurred", 1
	}
	return "", "", 0
}

func TestConfigurePackageCacheRetriesAWriteTheTransportTimedOut(t *testing.T) {
	defer kubectlgate.SetBackoffForTest(time.Millisecond)()
	sandbox := &timeoutOnceSandbox{}
	configured, err := ConfigurePackageCache(context.Background(), sandbox, "http://10.9.0.1:41234")
	if err != nil || !configured {
		t.Fatalf("ConfigurePackageCache = (%v, %v), want one timed-out write retried and the cache configured", configured, err)
	}
	if first, second := sandbox.stdins[0], sandbox.stdins[1]; first != second {
		t.Errorf("the retry wrote %q, want the same file the timed-out write was writing (%q)", second, first)
	}
}
