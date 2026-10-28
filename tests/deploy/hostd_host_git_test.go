package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hostd"
)

// hostGitEnv is what hostd_allowlist_content reads for a Google-track
// allowlist.
var hostGitEnv = []string{"GRAIN_INSTALL_TRACK=google", "HOSTD_AGY_PATH=/google/bin/agy", "GRAIN_DATA_DIR=/d",
	"GRAIN_SANDBOX_DIR=/s", "HOSTD_PROXY_VARS=HTTPS_PROXY", "HOST_GRAIN_BIN=/usr/local/bin/grain"}

// On both tracks the allowlist offers host-git: grain's own server,
// with a fixed command line, that runs an ambient git connection's
// gh, ggh or `git credential fill` on the host for the daemon in its
// container (`grain git-ambient`, and cloudtop-install.sh --git). It is
// a file hostd itself accepts.
func TestTheAllowlistOffersHostGit(t *testing.T) {
	content := runBash(t, shellFunction(t, "hostd_host_git_entry")+shellFunction(t, "hostd_citc_entry")+shellFunction(t, "hostd_allowlist_content")+"hostd_allowlist_content", hostGitEnv...)
	a, err := hostd.ParseAllowlist([]byte(content))
	if err != nil {
		t.Fatalf("hostd refuses the Google allowlist: %v\n%s", err, content)
	}
	e := a.Lookup(hostd.KindCapability, "host-git")
	if e == nil {
		t.Fatalf("no host-git capability entry:\n%s", content)
	}
	if e.Command != "/usr/local/bin/grain" || strings.Join(e.Args, " ") != "capability host-git" ||
		strings.Join(e.PassEnv, ",") != "HTTPS_PROXY" || len(e.Env) != 0 {
		t.Errorf("host-git entry = %+v", e)
	}
	// The daemon names the entry and nothing else.
	if _, err := a.Check(&hostd.Request{Kind: hostd.KindCapability, Name: "host-git", Args: []string{"api", "repos/o/r"}}, nil); err == nil {
		t.Error("host-git took arguments from the daemon")
	}

	core := runBash(t, shellFunction(t, "hostd_allowlist_content")+"hostd_allowlist_content",
		"GRAIN_INSTALL_TRACK=oss", "HOSTD_AGY_PATH=", "HOST_GRAIN_BIN=/usr/local/bin/grain")
	coreAllow, err := hostd.ParseAllowlist([]byte(core))
	if err != nil {
		t.Fatalf("hostd refuses the core allowlist: %v\n%s", err, core)
	}
	ce := coreAllow.Lookup(hostd.KindCapability, "host-git")
	if ce == nil {
		t.Fatalf("the core track's allowlist has no host-git entry:\n%s", core)
	}
	if ce.Command != "/usr/local/bin/grain" || strings.Join(ce.Args, " ") != "capability host-git" ||
		len(ce.PassEnv) != 0 || len(ce.Env) != 0 {
		t.Errorf("core host-git entry = %+v", ce)
	}
}

// A re-run keeps an allowlist an earlier run wrote, but adds host-git to
// one that predates it (on either track), when the file has the shape
// setup.sh writes; one of any other shape is left alone, with the lines
// to add.
func TestARerunAddsHostGitToAnOlderAllowlist(t *testing.T) {
	dir := t.TempDir()
	allow := filepath.Join(dir, "hostd.yaml")
	fns := logFn + "chown() { :; }\n" + shellFunction(t, "hostd_host_git_entry") + shellFunction(t, "ensure_hostd_host_git")
	env := append([]string{"HOSTD_ALLOWLIST=" + allow}, hostGitEnv...)

	older := "clis:\n  agy:\n    path: /google/bin/agy\ncapabilities:\n#  corp-search:\n#    command: /x\n"
	for _, before := range []string{older, strings.Replace(older, "capabilities:\n#  corp-search:\n#    command: /x\n", "capabilities: {}\n", 1)} {
		if err := os.WriteFile(allow, []byte(before), 0o644); err != nil {
			t.Fatal(err)
		}
		out := runBash(t, fns+"ensure_hostd_host_git", env...)
		contains(t, out, "added the host-git capability entry")
		data, _ := os.ReadFile(allow)
		a, err := hostd.ParseAllowlist(data)
		if err != nil || a.Lookup(hostd.KindCapability, "host-git") == nil || a.Lookup(hostd.KindCLI, "agy") == nil {
			t.Fatalf("after the re-run (%v):\n%s", err, data)
		}
		// And a second re-run changes nothing.
		runBash(t, fns+"ensure_hostd_host_git", env...)
		if again, _ := os.ReadFile(allow); string(again) != string(data) {
			t.Errorf("a second re-run changed the allowlist:\n%s", again)
		}
	}

	// A core-track re-run upgrades an older core-track allowlist too.
	coreOlder := "clis: {}\ncapabilities:\n  grain-gchat:\n    command: /usr/local/bin/grain\n    args: [capability, gchat]\n"
	if err := os.WriteFile(allow, []byte(coreOlder), 0o644); err != nil {
		t.Fatal(err)
	}
	coreOut := runBash(t, fns+"ensure_hostd_host_git", "HOSTD_ALLOWLIST="+allow, "GRAIN_INSTALL_TRACK=oss")
	contains(t, coreOut, "added the host-git capability entry")
	if data, _ := os.ReadFile(allow); func() bool {
		a, err := hostd.ParseAllowlist(data)
		return err != nil || a.Lookup(hostd.KindCapability, "host-git") == nil
	}() {
		t.Fatalf("core-track re-run did not add host-git:\n%s", data)
	}

	edited := "capabilities:\n  corp-search:\n    command: /x\nclis:\n  agy:\n    path: /google/bin/agy\n"
	if err := os.WriteFile(allow, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runBash(t, fns+"ensure_hostd_host_git", env...)
	contains(t, out, "has no host-git capability entry")
	contains(t, out, "args: [capability, host-git]")
	if data, _ := os.ReadFile(allow); string(data) != edited {
		t.Errorf("an allowlist of another shape was edited:\n%s", data)
	}
}
