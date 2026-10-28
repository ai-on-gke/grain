package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/pkgcache"
)

// noSudoRunner stands in for `docker exec` into the stock sandbox image,
// which runs as uid 1000 and has no sudo. It runs the real scripts with
// a PATH holding nothing but stubs: `id` answers uid, every command that
// touches /etc only records itself, and there is no `sudo` at all, so a
// non-root script reaching for it fails exactly as it did on a real host:
//
//	sh: 7: sudo: not found
type noSudoRunner struct {
	t        *testing.T
	uid, dir string
	scripts  []string
}

func newNoSudoRunner(t *testing.T, dir, uid string) *noSudoRunner {
	return &noSudoRunner{t: t, uid: uid, dir: dir}
}

func (r *noSudoRunner) Run(ctx context.Context, argv []string, stdin string) (string, string, int) {
	if len(argv) > 2 && argv[0] == "sh" && argv[1] == "-c" {
		r.scripts = append(r.scripts, argv[2])
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", `exec "$@"`, "sh"}, argv...)...)
	cmd.Env = []string{"PATH=" + r.dir, "FAKE_UID=" + r.uid, "LOG=" + filepath.Join(r.dir, "log")}
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return stdout.String(), stderr.String(), exitErr.ExitCode()
		}
		return stdout.String(), err.Error(), -1
	}
	return stdout.String(), stderr.String(), 0
}

// noSudoImage is the stub directory both runners share. `sh` is linked
// in because the runner's PATH is nothing else; the commands the scripts
// run as root write one line each to $LOG instead of touching the host.
func noSudoImage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no sh: %v", err)
	}
	if err := os.Symlink(sh, filepath.Join(dir, "sh")); err != nil {
		t.Fatal(err)
	}
	writePATHStub(t, dir, "id", "#!/bin/sh\necho \"$FAKE_UID\"\n")
	writePATHStub(t, dir, "dirname", "#!/bin/sh\necho /\n")
	for _, name := range []string{"mkdir", "install", "update-ca-certificates"} {
		writePATHStub(t, dir, name, "#!/bin/sh\necho \""+name+" uid=$FAKE_UID $*\" >>\"$LOG\"\n"+
			"while read -r _; do :; done\n")
	}
	return dir
}

// The regression test for the linux-container platform's package cache:
// on an image with no sudo, every write into /etc must still land, as
// root, while the per-tool configuration stays the agent's own.
func TestContainerSandboxConfiguresThePackageCacheWithoutSudo(t *testing.T) {
	dir := noSudoImage(t)
	agent, root := newNoSudoRunner(t, dir, "1000"), newNoSudoRunner(t, dir, "0")
	sb := &containerSandbox{owner: NewContainerSandboxes(ContainerConfig{Image: "img", Workspace: "/home/agent"}),
		name: "2-1", container: ContainerNamePrefix + "2-1", runner: agent, root: root}

	base := "http://10.241.0.1:9729"
	configured, err := ConfigurePackageCacheTransparent(context.Background(), sb, base, []byte("-----BEGIN CERTIFICATE-----\n"), "")
	if err != nil {
		t.Fatalf("ConfigurePackageCacheTransparent: %v", err)
	}
	if !configured {
		t.Fatal("ConfigurePackageCacheTransparent reported doing nothing")
	}

	log, err := os.ReadFile(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatalf("nothing was written as root: %v", err)
	}
	files, err := pkgcache.SandboxFiles(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, pkgcache.SandboxCAFiles([]byte("x"))...)
	for _, file := range files {
		if want := "install uid=0 -m " + file.Mode + " /dev/stdin " + file.Path; !strings.Contains(string(log), want) {
			t.Errorf("no %q in what ran:\n%s", want, log)
		}
	}
	if want := "install uid=0 -m 0644 /dev/stdin " + pkgcache.DockerConfigPath; !strings.Contains(string(log), want) {
		t.Errorf("no %q in what ran:\n%s", want, log)
	}
	if !strings.Contains(string(log), "update-ca-certificates uid=0") {
		t.Errorf("update-ca-certificates did not run as root:\n%s", log)
	}
	if strings.Contains(string(log), "uid=1000") {
		t.Errorf("something meant for root ran as the agent:\n%s", log)
	}

	// `go env -w` and `npm config set` write the user's own configuration,
	// so they are the agent's and never root's.
	for _, script := range root.scripts {
		if script == configureToolScript {
			t.Errorf("a tool's own configuration ran as root, where it would configure root's home")
		}
	}
	var tools int
	for _, script := range agent.scripts {
		if script == configureToolScript {
			tools++
		} else {
			t.Errorf("the agent ran a root script:\n%s", script)
		}
	}
	commands, _ := pkgcache.SandboxCommands(base)
	if tools != len(commands) {
		t.Errorf("the agent ran %d tool commands, want %d", tools, len(commands))
	}
}

// And the fake is faithful: the same image, reached only as its own user
// -- which is all the container backend did before -- fails the way the
// 10-04 walkthrough's journal did.
func TestTheNoSudoImageFailsWithoutARootRoute(t *testing.T) {
	dir := noSudoImage(t)
	sandbox := agentOnlySandbox{runner: newNoSudoRunner(t, dir, "1000")}
	_, err := ConfigurePackageCache(context.Background(), sandbox, "http://10.241.0.1:9729")
	if err == nil || !strings.Contains(err.Error(), "sudo: not found") {
		t.Fatalf("ConfigurePackageCache = %v, want a sudo: not found", err)
	}
}

// agentOnlySandbox is a SandboxCommander and nothing more: a sandbox
// reached only as the image's own user.
type agentOnlySandbox struct {
	plainSandbox
	runner sandboxRunner
}

func (s agentOnlySandbox) RunInSandbox(ctx context.Context, argv []string, stdin string) (string, string, int) {
	return s.runner.Run(ctx, argv, stdin)
}

func TestContainerSandboxRootRunnerExecsAsUIDZero(t *testing.T) {
	sb := &containerSandbox{owner: NewContainerSandboxes(ContainerConfig{Docker: "/usr/bin/docker", Image: "img", Workspace: "/w"}),
		name: "x", container: "grain-sandbox-x"}
	got, ok := sb.rootRunner().(*mcp.ContainerExecRunner)
	if !ok {
		t.Fatalf("rootRunner = %T, want a docker exec", sb.rootRunner())
	}
	if *got != (mcp.ContainerExecRunner{Docker: "/usr/bin/docker", Container: "grain-sandbox-x", User: "0"}) {
		t.Errorf("rootRunner = %+v", *got)
	}
}
