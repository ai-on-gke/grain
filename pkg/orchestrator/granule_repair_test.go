package orchestrator_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// TestGuestDockerNetworkRepairScript runs the repair script against a
// temporary daemon.json and stood-in system tools, and asserts what it
// leaves behind: which subnets daemon.json ends up holding, whether
// docker0 was deleted, and whether dockerd was restarted.
//
// It drives orchestrator.GuestDockerNetworkRepairScript -- production's
// own text -- rather than a paraphrase of it, which is the whole point:
// see that constant for what the paraphrase this replaced could not
// catch. The only edit is the conf path, and the test refuses to run a
// script that still names the real one, because the sudo stand-in below
// runs its arguments directly and a missed occurrence would edit this
// machine's /etc/docker/daemon.json and delete its docker0.
func TestGuestDockerNetworkRepairScript(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	cases := []struct {
		name string
		// targetGW is the address the guest is told it must not collide
		// with: its host's default gateway, or grain's GitProxyHost.
		targetGW string
		// docker0 is what `ip -4 addr show docker0` answers. "" stands for
		// a guest that has no docker0 yet, where real ip prints nothing.
		docker0 string
		initial string
		// wantBip and wantPool are what daemon.json must hold afterwards,
		// which for the no-conflict cases is what it held before.
		wantBip  string
		wantPool string
		// wantRestart is whether dockerd should have been restarted, which
		// is how the script reports "I repaired something" to its guest.
		wantRestart bool
		// wantDocker0Deleted is the branch the paraphrase this test
		// replaced had dropped entirely, so nothing asserted it before.
		wantDocker0Deleted bool
	}{
		{
			name:     "10.x gateway conflict (10.240.0.1) replaces 10.x subnet with 172.30.255.1",
			targetGW: "10.240.0.1",
			docker0:  "    inet 10.240.0.1/24 brd 10.240.0.255 scope global docker0\n",
			initial: `{
  "bip": "10.240.0.1/24",
  "default-address-pools": [
    {
      "base": "10.241.0.0/16",
      "size": 24
    }
  ]
}`,
			wantBip:            "172.30.255.1/24",
			wantPool:           "172.31.0.0/16",
			wantRestart:        true,
			wantDocker0Deleted: true,
		},
		{
			name:     "172.30.x gateway conflict (172.30.255.1) replaces 172.30.x subnet with 10.240.0.1",
			targetGW: "172.30.255.1",
			docker0:  "    inet 172.30.255.1/24 brd 172.30.255.255 scope global docker0\n",
			initial: `{
  "bip": "172.30.255.1/24",
  "default-address-pools": [
    {
      "base": "172.31.0.0/16",
      "size": 24
    }
  ]
}`,
			wantBip:            "10.240.0.1/24",
			wantPool:           "10.241.0.0/16",
			wantRestart:        true,
			wantDocker0Deleted: true,
		},
		{
			name:     "Non-conflicting gateway (192.168.1.1) leaves 10.240.0.1 subnet intact",
			targetGW: "192.168.1.1",
			docker0:  "    inet 10.240.0.1/24 brd 10.240.0.255 scope global docker0\n",
			initial: `{
  "bip": "10.240.0.1/24",
  "default-address-pools": [
    {
      "base": "10.241.0.0/16",
      "size": 24
    }
  ]
}`,
			wantBip:            "10.240.0.1/24",
			wantPool:           "10.241.0.0/16",
			wantRestart:        false,
			wantDocker0Deleted: false,
		},
		{
			// The addresses are matched as literal strings, not as
			// patterns. Spelled as one `grep -E "$target|$target_prefix"`
			// every dot in a dotted quad is "any character", so the prefix
			// of 10.0.0.1 -- 10.0.0 -- matches the six digits 100000, and
			// an unrelated number in daemon.json reads as a subnet
			// collision: this guest would lose its docker0 and have
			// dockerd restarted under it over a log-rotation setting.
			// Nothing here collides with 10.0.0.1, so nothing should
			// happen at all.
			name:     "a six-digit number in daemon.json is not a conflicting subnet",
			targetGW: "10.0.0.1",
			docker0:  "",
			initial: `{
  "bip": "172.30.255.1/24",
  "default-address-pools": [
    {
      "base": "172.31.0.0/16",
      "size": 24
    }
  ],
  "log-opts": {
    "max-size": "100000"
  }
}`,
			wantBip:            "172.30.255.1/24",
			wantPool:           "172.31.0.0/16",
			wantRestart:        false,
			wantDocker0Deleted: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin, ipLog, systemctlLog := guestRepairStubs(t, tc.docker0)
			conf := filepath.Join(bin, "daemon.json")
			if err := os.WriteFile(conf, []byte(tc.initial), 0o644); err != nil {
				t.Fatalf("writing initial daemon.json: %v", err)
			}

			script := strings.ReplaceAll(orchestrator.GuestDockerNetworkRepairScript, orchestrator.GuestDockerConfigPath, conf)
			if script == orchestrator.GuestDockerNetworkRepairScript {
				t.Fatalf("the script no longer names %s, so this test substituted nothing and is asserting against a file it never wrote", orchestrator.GuestDockerConfigPath)
			}
			if strings.Contains(script, orchestrator.GuestDockerConfigPath) {
				t.Fatalf("%s still appears in the script under test; refusing to run it, because the sudo stand-in would edit this machine's own docker configuration", orchestrator.GuestDockerConfigPath)
			}

			cmd := exec.Command("sh", "-c", script, "sh", tc.targetGW)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("the repair script failed: %v\n%s", err, out)
			}

			data, err := os.ReadFile(conf)
			if err != nil {
				t.Fatalf("reading daemon.json: %v", err)
			}
			content := string(data)
			if !strings.Contains(content, tc.wantBip) {
				t.Errorf("expected bip %q in %s", tc.wantBip, content)
			}
			if !strings.Contains(content, tc.wantPool) {
				t.Errorf("expected pool %q in %s", tc.wantPool, content)
			}

			restarted := strings.Contains(readStubLog(t, systemctlLog), "restart docker")
			if restarted != tc.wantRestart {
				t.Errorf("dockerd restarted = %v, want %v; systemctl saw %q",
					restarted, tc.wantRestart, readStubLog(t, systemctlLog))
			}
			deleted := strings.Contains(readStubLog(t, ipLog), "link del docker0")
			if deleted != tc.wantDocker0Deleted {
				t.Errorf("docker0 deleted = %v, want %v; ip saw %q",
					deleted, tc.wantDocker0Deleted, readStubLog(t, ipLog))
			}
		})
	}
}

// TestGuestDockerNetworkRepairRewritesDaemonJSONAllAtOnce pins that the
// repair edits daemon.json in one sed rather than two.
//
// The script runs under set -e, so as two invocations a second that failed
// after a first that had not left dockerd configured with its bridge
// address moved to the far family and its address pool still in the
// colliding one -- and left before the restart that would have been the
// guest's only sign anything happened. One sed cannot do that: sed -i
// writes a temp file and renames it over the original, so the file holds
// both edits or neither.
//
// The sed stood in here refuses its second invocation and every one after
// it. Against one sed that is never reached, so the repair completes and
// daemon.json comes out wholly in the far family. Against two it is
// reached, which is the regression: the script dies on it and the file is
// left holding one address from each.
func TestGuestDockerNetworkRepairRewritesDaemonJSONAllAtOnce(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	hostSed, err := exec.LookPath("sed")
	if err != nil {
		t.Skipf("no sed on PATH to stand in for the guest's: %v", err)
	}

	bin, _, _ := guestRepairStubs(t, "")
	conf := filepath.Join(bin, "daemon.json")
	initial := `{
  "bip": "10.240.0.1/24",
  "default-address-pools": [
    {
      "base": "10.241.0.0/16",
      "size": 24
    }
  ]
}`
	if err := os.WriteFile(conf, []byte(initial), 0o644); err != nil {
		t.Fatalf("writing initial daemon.json: %v", err)
	}

	// Overwrites the sed guestRepairStubs installed. In-place is spelled
	// the way this host's sed spells it, for the same reason
	// installGuestSed exists: the guest's own GNU sed takes no separate
	// backup suffix and BSD's requires one.
	inPlace := "-i"
	if runtime.GOOS != "linux" {
		inPlace = "-i ''"
	}
	counter := filepath.Join(bin, "sed.calls")
	writeGuestStub(t, bin, "sed", "#!/bin/sh\n"+
		"n=$(cat "+shellSingleQuoted(counter)+" 2>/dev/null || echo 0)\n"+
		"n=$((n + 1))\n"+
		"echo \"$n\" > "+shellSingleQuoted(counter)+"\n"+
		"if [ \"$n\" -ge 2 ]; then\n"+
		"  echo \"sed stand-in: refusing invocation $n on purpose\" >&2\n"+
		"  exit 1\n"+
		"fi\n"+
		"if [ \"$1\" = -i ]; then\n"+
		"  shift\n"+
		"  exec "+shellSingleQuoted(hostSed)+" "+inPlace+" \"$@\"\n"+
		"fi\n"+
		"exec "+shellSingleQuoted(hostSed)+" \"$@\"\n")

	script := strings.ReplaceAll(orchestrator.GuestDockerNetworkRepairScript, orchestrator.GuestDockerConfigPath, conf)
	if strings.Contains(script, orchestrator.GuestDockerConfigPath) {
		t.Fatalf("%s still appears in the script under test; refusing to run it", orchestrator.GuestDockerConfigPath)
	}

	cmd := exec.Command("sh", "-c", script, "sh", "10.240.0.1")
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, runErr := cmd.CombinedOutput()

	data, err := os.ReadFile(conf)
	if err != nil {
		t.Fatalf("reading daemon.json: %v", err)
	}
	content := string(data)
	movedBip := strings.Contains(content, "172.30.255.1/24")
	movedPool := strings.Contains(content, "172.31.0.0/16")
	if movedBip != movedPool {
		t.Errorf("daemon.json holds one address from each family (bip moved = %v, pool moved = %v), which is the state no failure may leave it in:\n%s",
			movedBip, movedPool, content)
	}
	if runErr != nil {
		t.Errorf("the repair script failed: %v -- it reached a second sed, so it is not rewriting daemon.json in one\n%s", runErr, out)
	}
	if calls := strings.TrimSpace(readStubLog(t, counter)); calls != "1" {
		t.Errorf("sed was invoked %q times, want exactly 1", calls)
	}
}

// guestRepairStubs installs the system tools the repair script reaches for
// -- sudo, ip, systemctl, and off Linux a sed (installGuestSed) -- in one
// directory to put in front of PATH, and returns it with the files the ip
// and systemctl stands-in append their arguments to.
//
// Every one of them has to be shadowed, because the script deletes a
// docker0 and restarts dockerd and on a developer's machine those are
// that machine's. The sudo here runs its arguments directly rather than
// looking for the real one, so nothing the script names may fall through
// to a real binary -- the same reason its caller refuses to run a script
// still holding a real /etc path.
//
// docker0 is what `ip -4 addr show docker0` answers; "" stands for a guest
// with no docker0, where real ip prints nothing on stdout.
func guestRepairStubs(t *testing.T, docker0 string) (bin, ipLog, systemctlLog string) {
	t.Helper()
	// os.MkdirTemp with a prefix of our own rather than t.TempDir(),
	// because this directory's path is substituted into the script under
	// test and the script spells its one path unquoted -- which is safe
	// for the /etc/docker/daemon.json it ships with and not for whatever
	// a temporary directory is called. t.TempDir() names itself after the
	// test, and these subtests are named after the addresses they use, so
	// the path arrived holding parentheses:
	//
	//	sh: -c: line 36: syntax error near unexpected token `('
	//
	// Quoting it in the substitution would fix the parentheses and leave
	// the next character to go wrong; a path this test chose the shape of
	// cannot go wrong at all.
	var err error
	bin, err = os.MkdirTemp("", "grain-docker-repair-*")
	if err != nil {
		t.Fatalf("making a directory for the guest's stand-in tools: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(bin) })
	ipLog = filepath.Join(bin, "ip.log")
	systemctlLog = filepath.Join(bin, "systemctl.log")
	addr := filepath.Join(bin, "docker0.addr")
	if err := os.WriteFile(addr, []byte(docker0), 0o644); err != nil {
		t.Fatalf("writing the docker0 reading: %v", err)
	}
	// Each path is written into the stub's own text rather than passed in
	// its environment. The script runs these through its own sudo, so the
	// environment they see is not this test's to set -- and a marker read
	// from the environment is one an unrelated export can switch off.
	writeGuestStub(t, bin, "sudo", "#!/bin/sh\n[ \"$1\" = -n ] && shift\nexec \"$@\"\n")
	writeGuestStub(t, bin, "ip", "#!/bin/sh\n"+
		"echo \"$*\" >> "+shellSingleQuoted(ipLog)+"\n"+
		"case \"$*\" in\n"+
		"  *\"addr show docker0\"*) cat "+shellSingleQuoted(addr)+" ;;\n"+
		"esac\n"+
		"exit 0\n")
	writeGuestStub(t, bin, "systemctl", "#!/bin/sh\n"+
		"echo \"$*\" >> "+shellSingleQuoted(systemctlLog)+"\n"+
		"exit 0\n")
	installGuestSed(t, bin)
	return bin, ipLog, systemctlLog
}

func writeGuestStub(t *testing.T, dir, name, script string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("writing a %s stand-in: %v", name, err)
	}
}

// readStubLog is a stand-in's log, or "" where it was never invoked and so
// never created the file. A missing log is an answer here, not an error.
func readStubLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// shellSingleQuoted wraps a path for the stub scripts above. These sit
// under the directory guestRepairStubs made, whose name it chose and
// which therefore holds no single quote, so the quotes alone are enough
// and nothing inside them needs escaping.
func shellSingleQuoted(path string) string { return "'" + path + "'" }

// TestEnsureGuestDockerNetworkInvokesRepair tests that EnsureGuestDockerNetwork
// runs the guestDockerNetworkRepair script via the sandbox runner.
func TestEnsureGuestDockerNetworkInvokesRepair(t *testing.T) {
	stateDir := t.TempDir()
	k := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
		StateDir:     stateDir,
		GitProxyHost: "10.240.0.1",
	})

	var recordedCmd []string
	runner := &testMockRunner{
		runFunc: func(ctx context.Context, cmd []string, stdin string) (string, string, int) {
			recordedCmd = cmd
			return "", "", 0
		},
	}

	k.EnsureGuestDockerNetworkWithRunnerForTest(context.Background(), "test-vm", runner)

	if len(recordedCmd) < 5 {
		t.Fatalf("recorded command too short: %v", recordedCmd)
	}
	if recordedCmd[0] != "sh" || recordedCmd[1] != "-c" {
		t.Errorf("unexpected command format: %v", recordedCmd)
	}
	if recordedCmd[4] != "10.240.0.1" {
		t.Errorf("expected target host 10.240.0.1 passed to repair script, got %q", recordedCmd[4])
	}
}

// TestDockerConfigureMTUScript verifies that docker-configure-mtu dynamically
// clamps /etc/docker/daemon.json MTU to match eth0 or granule.mtu kernel cmdline.
func TestDockerConfigureMTUScript(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}

	cases := []struct {
		name    string
		mtuVal  int
		initial string
		wantMTU int
	}{
		{
			name:   "GKE CNI MTU 1460",
			mtuVal: 1460,
			initial: `{
  "bip": "10.240.0.1/24",
  "default-address-pools": [
    {
      "base": "10.241.0.0/16",
      "size": 24
    }
  ],
  "mtu": 1500
}`,
			wantMTU: 1460,
		},
		{
			name:   "VXLAN / Geneve Overlay MTU 1440",
			mtuVal: 1440,
			initial: `{
  "bip": "10.240.0.1/24",
  "default-address-pools": [
    {
      "base": "10.241.0.0/16",
      "size": 24
    }
  ],
  "mtu": 1500
}`,
			wantMTU: 1440,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			daemonPath := filepath.Join(tmpDir, "daemon.json")
			if err := os.WriteFile(daemonPath, []byte(tc.initial), 0644); err != nil {
				t.Fatalf("writing initial daemon.json: %v", err)
			}

			// Script adapting docker-configure-mtu logic with mock MTU input
			script := `
set -eu
mtu="$MOCK_MTU"
if [ -n "$mtu" ] && [ "$mtu" -ge 576 ] 2>/dev/null && [ "$mtu" -le 65535 ] 2>/dev/null; then
  daemon_json="$CONF"
  if [ -f "$daemon_json" ] && [ -s "$daemon_json" ]; then
    tmp="${daemon_json}.tmp"
    if command -v jq >/dev/null 2>&1 && jq --argjson mtu "$mtu" '.mtu = $mtu' "$daemon_json" > "$tmp" 2>/dev/null; then
      mv -f "$tmp" "$daemon_json"
    elif command -v python3 >/dev/null 2>&1; then
      rm -f "$tmp"
      python3 -c "import json, sys; d = json.load(open('$daemon_json')) if open('$daemon_json').read().strip() else {}; d['mtu'] = int(sys.argv[1]); json.dump(d, open('$daemon_json', 'w'), indent=2)" "$mtu" 2>/dev/null || true
    fi
  fi
fi
`
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(),
				"CONF="+daemonPath,
				"MOCK_MTU="+strconv.Itoa(tc.mtuVal),
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("script failed: %v, out: %s", err, out)
			}

			data, err := os.ReadFile(daemonPath)
			if err != nil {
				t.Fatalf("reading daemon.json: %v", err)
			}
			content := string(data)
			wantStr := `"mtu": ` + strconv.Itoa(tc.wantMTU)
			if !strings.Contains(content, wantStr) && !strings.Contains(content, `"mtu":`+strconv.Itoa(tc.wantMTU)) {
				t.Errorf("expected %s in %s", wantStr, content)
			}
		})
	}
}

// installGuestSed puts a `sed` that behaves like the guest's own into
// dir, which the caller has already placed in front of PATH, and does
// nothing where this host's `sed` already is one.
//
// A granule guest is always Linux, so guestDockerNetworkRepair is a
// GNU/Linux script, and `sed -i` with no backup suffix is how it and
// every other in-guest rewrite in this repo spell an in-place edit
// (Granulefile, scripts/release-images.sh,
// deploy/terraform/gcp/files/startup.sh). A test is the one place that
// text ever runs anywhere else, and on macOS "anywhere else" means BSD
// sed, whose -i takes its backup suffix as the option's own argument
// rather than attached to the flag. So BSD sed reads
// "s#10.240.0.1/24#172.30.255.1/24#g" as the suffix, then takes the
// daemon.json path for the script, and parses /var/ as an address and the
// next character as a command:
//
//	sed: 1: "/var/folders/2m/rm7_frn ...": invalid command code f
//
// -- an exit 1 indistinguishable, from the test's side, from a repair
// that went wrong.
//
// Standing a sed in for the guest's is the fix rather than portabilising
// the script, because neither portable form is free in a guest: an
// explicit suffix (`sed -i.bak`) leaves /etc/docker/daemon.json.bak
// behind in the guest's own /etc, and writing through a temp file needs a
// second `sudo -n sh -c` to get the redirection to run as root and a
// `mv` that gives daemon.json whatever mode root's umask produces instead
// of the one it had. The wrapper below hands BSD sed the empty suffix
// that GNU sed will not accept spelled out, which leaves the script under
// test the same text the guest runs -- the point of running it at all.
func installGuestSed(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "linux" {
		return
	}
	hostSed, err := exec.LookPath("sed")
	if err != nil {
		t.Skipf("no sed on PATH to stand in for the guest's: %v", err)
	}
	stub := "#!/bin/sh\n" +
		"# Stands in for GNU sed's -i, which takes no separate suffix argument.\n" +
		"if [ \"$1\" = -i ]; then\n" +
		"  shift\n" +
		"  exec \"" + hostSed + "\" -i '' \"$@\"\n" +
		"fi\n" +
		"exec \"" + hostSed + "\" \"$@\"\n"
	writeGuestStub(t, dir, "sed", stub)
}

type testMockRunner struct {
	runFunc func(ctx context.Context, cmd []string, stdin string) (string, string, int)
}

func (m *testMockRunner) Run(ctx context.Context, cmd []string, stdin string) (string, string, int) {
	if m.runFunc != nil {
		return m.runFunc(ctx, cmd, stdin)
	}
	return "", "", 0
}
