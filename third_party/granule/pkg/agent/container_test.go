package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/granule/pkg/containerspec"
	"github.com/bwsalmon/granule/pkg/execwire"
)

// TestMain keeps the container exec tests off the machine running them:
// a test host that is itself a granule guest has an effective spec and
// block devices of its own, which are not the containers these tests lay
// out.
func TestMain(m *testing.M) {
	containerEffective = ""
	containerSysBlock = filepath.Join(os.TempDir(), "granule-agent-test-no-sys-block")
	os.Exit(m.Run())
}

// fakeContainer lays out a wrapped container the way `granule wrap`
// does -- a filesystem and the spec beside it -- and returns the spec's
// path.
func fakeContainer(t *testing.T, spec containerspec.Spec) string {
	t.Helper()
	dir := t.TempDir()
	if spec.Rootfs == "" {
		spec.Rootfs = filepath.Join(dir, "rootfs")
	}
	for name, content := range map[string]string{
		"bin/sh":         "#!/bin/sh\n",
		"usr/bin/app":    "#!/bin/sh\n",
		"etc/passwd":     "root:x:0:0:root:/root:/bin/sh\nweb:x:1000:1001:web:/home/web:/bin/false\n",
		"srv/data/.keep": "",
	} {
		path := filepath.Join(spec.Rootfs, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	data, err := spec.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "container.json")
	if err := os.WriteFile(config, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return config
}

func envValue(env []string, key string) (string, bool) {
	value, found := "", false
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			value, found = v, true
		}
	}
	return value, found
}

// With nothing asked of it, a container exec is what `docker exec` gives:
// the container's root, user, working directory and environment.
func TestAContainerExecDefaultsToTheContainersOwnSettings(t *testing.T) {
	config := fakeContainer(t, containerspec.Spec{
		Entrypoint: []string{"app"},
		Env:        []string{"PATH=/usr/bin:/bin", "MODE=prod"},
		User:       "web",
		WorkingDir: "/srv/data",
	})

	l, err := containerLaunch(config, execwire.Request{Container: true, Argv: []string{"app", "--check"}})
	if err != nil {
		t.Fatal(err)
	}
	if l.Chroot == "" || !strings.HasSuffix(l.Chroot, "/rootfs") {
		t.Errorf("chroot = %q, want the wrapped filesystem", l.Chroot)
	}
	if strings.Join(l.Argv, " ") != "/usr/bin/app --check" {
		t.Errorf("argv = %q, want the program found on the container's PATH, as its path inside the chroot", l.Argv)
	}
	if l.Dir != "/srv/data" {
		t.Errorf("dir = %q, want the container's WORKDIR", l.Dir)
	}
	if l.Credential == nil || l.Credential.Uid != 1000 || l.Credential.Gid != 1001 || len(l.Credential.Groups) != 0 {
		t.Errorf("credential = %+v, want the image's web account with no supplementary groups", l.Credential)
	}
	if v, _ := envValue(l.Env, "MODE"); v != "prod" {
		t.Errorf("MODE = %q, want the container's environment", v)
	}
	if v, _ := envValue(l.Env, "HOME"); v != "/home/web" {
		t.Errorf("HOME = %q, want the image's home for web", v)
	}
	if _, ok := envValue(l.Env, "TERM"); ok {
		t.Errorf("a session without a terminal got a TERM: %q", l.Env)
	}
}

// What the request says wins over the container's defaults, the way
// `docker exec -u -w -e` does.
func TestAContainerExecTakesTheRequestsUserDirAndEnv(t *testing.T) {
	config := fakeContainer(t, containerspec.Spec{
		Cmd:  []string{"/usr/bin/app"},
		Env:  []string{"MODE=prod"},
		User: "web",
	})

	l, err := containerLaunch(config, execwire.Request{
		Container: true, Line: "echo hi", User: "root", Dir: "/srv", Env: []string{"MODE=debug"}, TTY: true, Term: "xterm-256color",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(l.Argv, "|") != "/bin/sh|-c|echo hi" {
		t.Errorf("argv = %q, want the line run by the image's own shell", l.Argv)
	}
	if l.Dir != "/srv" {
		t.Errorf("dir = %q", l.Dir)
	}
	if l.Credential == nil || l.Credential.Uid != 0 {
		t.Errorf("credential = %+v, want the image's root", l.Credential)
	}
	if v, _ := envValue(l.Env, "MODE"); v != "debug" {
		t.Errorf("MODE = %q, want the request's entry over the container's", v)
	}
	if v, _ := envValue(l.Env, "TERM"); v != "xterm-256color" {
		t.Errorf("TERM = %q, want the client's", v)
	}
}

func TestAContainerExecRefusesWhatItCannotRun(t *testing.T) {
	config := fakeContainer(t, containerspec.Spec{Cmd: []string{"/usr/bin/app"}})
	for name, tc := range map[string]struct {
		config string
		req    execwire.Request
		want   string
	}{
		"no container":         {filepath.Join(t.TempDir(), "absent.json"), execwire.Request{Container: true}, "runs no wrapped container"},
		"argv and line":        {config, execwire.Request{Container: true, Line: "x", Argv: []string{"x"}}, "not both"},
		"not on the PATH":      {config, execwire.Request{Container: true, Argv: []string{"nosuch"}}, "not on its own PATH"},
		"a guest path":         {config, execwire.Request{Container: true, Argv: []string{"/usr/bin/nosuch"}}, "nosuch"},
		"no such directory":    {config, execwire.Request{Container: true, Dir: "/nosuch"}, "working directory"},
		"a relative directory": {config, execwire.Request{Container: true, Dir: "srv"}, "absolute"},
		"no such user":         {config, execwire.Request{Container: true, User: "nobodyhere"}, "nobodyhere"},
		"a bad env entry":      {config, execwire.Request{Container: true, Env: []string{"NOEQUALS"}}, "KEY=value"},
	} {
		if _, err := containerLaunch(tc.config, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want one mentioning %q", name, err, tc.want)
		}
	}
}

// Argv is a container exec's; in the guest a command runs through the
// account's shell from its line, and a request mixing the two is a
// client mistake to name rather than half-honour.
func TestArgvWithoutContainerIsRefused(t *testing.T) {
	if _, err := guestLaunch(execwire.Request{Argv: []string{"true"}}); err == nil {
		t.Fatal("argv without container was accepted")
	}
}

// End to end through Serve, against a container whose root is this
// machine's own -- the one root a test can enter without the privilege a
// chroot takes -- so that the session really runs what containerLaunch
// resolved, in the container's working directory and environment.
func TestAContainerExecRunsInTheContainersEnvironment(t *testing.T) {
	dir := t.TempDir()
	spec := containerspec.Spec{
		Rootfs:     "/",
		Cmd:        []string{"/bin/true"},
		Env:        []string{"PATH=/usr/bin:/bin", "GREETING=from the image"},
		WorkingDir: dir,
	}
	data, err := spec.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "container.json")
	if err := os.WriteFile(config, data, 0o644); err != nil {
		t.Fatal(err)
	}
	old := containerConfig
	containerConfig = config
	t.Cleanup(func() { containerConfig = old })

	res := run(t, execwire.Request{Container: true, User: currentAccount(t), Argv: []string{"sh", "-c", `echo "$GREETING"; pwd; exit 3`}}, "")
	if res.code != 3 {
		t.Errorf("exit code = %d, want 3", res.code)
	}
	if want := "from the image\n" + dir + "\n"; res.stdout != want {
		t.Errorf("stdout = %q, want %q", res.stdout, want)
	}
}

// A container exec enters the container granule-container is running --
// the effective spec, with the VM's override in it -- rather than the one
// the image was wrapped with.
func TestAContainerExecEntersTheEffectiveSpec(t *testing.T) {
	baked := containerspec.Spec{Cmd: []string{"/usr/bin/app"}, Env: []string{"PATH=/usr/bin:/bin", "MODE=prod"}, User: "web"}
	config := fakeContainer(t, baked)
	rootfs := filepath.Join(filepath.Dir(config), "rootfs")

	effectiveSpec := baked
	effectiveSpec.Rootfs = rootfs
	effectiveSpec = effectiveSpec.Apply(containerspec.Override{Env: []string{"MODE=dev"}, User: "root", WorkingDir: "/srv/data"})
	data, err := effectiveSpec.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	effective := filepath.Join(t.TempDir(), "container.json")
	if err := os.WriteFile(effective, data, 0o600); err != nil {
		t.Fatal(err)
	}
	old := containerEffective
	containerEffective = effective
	t.Cleanup(func() { containerEffective = old })

	l, err := containerLaunch(config, execwire.Request{Container: true, Argv: []string{"app"}})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := envValue(l.Env, "MODE"); v != "dev" {
		t.Errorf("MODE = %q, want the override's", v)
	}
	if l.Who != "root" || l.Dir != "/srv/data" {
		t.Errorf("launch runs as %q in %q, want root in /srv/data", l.Who, l.Dir)
	}
}
