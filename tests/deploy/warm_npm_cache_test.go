package deploy

// scripts/warm-npm-cache.sh, run rather than read.
//
// The script is the fetch half of every `npm ci` in
// .grain/workflows/tests.yml, split off from the check half so that the
// two can fail in two different ways: it runs as an `infrastructure:`
// step, so its own failure ends the job `errored` -- no verdict about
// the tree -- while the offline install after it can only fail on the
// tree. See pkg/ci.Step.Infrastructure and that workflow's own header.
//
// What has to be tested by running it is the judgement inside it. `npm
// ci` exits 1 whether the registry would not answer or the lockfile
// does not resolve, and the script decides which of those happened:
// exit 1 for the registry (the job errors, nothing is said about the
// commit) and exit 0 for the lockfile (the job carries on, and the
// offline install below fails as the verdict it is). Getting that
// backwards is the whole risk of the arrangement -- exit 0 too often
// and a network outage reddens branches again, exit 1 too often and a
// broken lockfile passes CI wearing a conclusion nobody blames -- and
// neither direction is visible in the file.
//
// So npm is a stub here. These tests fetch nothing, install nothing and
// need no registry: what they assert is which way the script jumps on
// each of the answers npm really gives.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// npmStub writes a fake `npm` onto a PATH of its own and returns that
// directory. The stub answers the three things the script asks npm for:
// `npm config get registry`, `npm ci` and `npm ping`.
func npmStub(t *testing.T, output string, ciStatus, pingStatus int) string {
	t.Helper()
	dir := t.TempDir()
	stub := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  config) echo 'http://registry.example.invalid/' ;;\n" +
		"  ping) exit " + strconv.Itoa(pingStatus) + " ;;\n" +
		"  ci) cat <<'NPMOUT'\n" + output + "\nNPMOUT\n" +
		"      exit " + strconv.Itoa(ciStatus) + " ;;\n" +
		"  *) echo \"npm stub: unexpected $*\" >&2; exit 111 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(stub), 0o755); err != nil {
		t.Fatalf("writing the npm stub: %v", err)
	}
	return dir
}

// warm runs the script over this repository's own ui/ with that stub in
// front of the real npm, and returns its exit status and everything it
// printed.
func warm(t *testing.T, stubDir string) (int, string) {
	t.Helper()
	return warmWithEnv(t, stubDir)
}

// warmWithEnv is warm with more of the environment the job around this
// step really has -- which is the whole of what the test below is about.
//
// It runs the script again when the stub could not be executed because it
// was "Text file busy". That is golang/go#22315, not anything the script
// did: these tests run in parallel, and a fork for one of them can inherit
// another's still-open descriptor on the stub it has just written, for as
// long as that child takes to exec. Executing the stub inside that window
// fails with ETXTBSY. It failed one CI run twice over, both times on
// TestAnUnknownFailureAgainstALiveRegistryIsTheTreesToAnswerFor.
func warmWithEnv(t *testing.T, stubDir string, env ...string) (int, string) {
	t.Helper()
	for attempt := 1; ; attempt++ {
		cmd := exec.Command(filepath.Join(repoRoot(t), "scripts", "warm-npm-cache.sh"), "ui")
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(), "PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		cmd.Env = append(cmd.Env, env...)
		out, err := cmd.CombinedOutput()
		if strings.Contains(string(out), "Text file busy") && attempt < 5 {
			time.Sleep(time.Duration(attempt) * 50 * time.Millisecond)
			continue
		}
		status := 0
		if exit, ok := err.(*exec.ExitError); ok {
			status = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("running warm-npm-cache.sh: %v\n%s", err, out)
		}
		return status, string(out)
	}
}

// The ordinary run: npm filled the cache, so the install after this one
// has everything it needs and the job carries on.
func TestAWarmThatWorkedSaysSoAndSucceeds(t *testing.T) {
	t.Parallel()
	status, out := warm(t, npmStub(t, "added 329 packages in 15s", 0, 0))
	if status != 0 {
		t.Fatalf("a successful npm ci left the warm step at %d, which would error a job that is fine:\n%s", status, out)
	}
	if !strings.Contains(out, "now holds what ui/package-lock.json names") {
		t.Errorf("the warm step said nothing about what it did:\n%s", out)
	}
}

// The fetch half has to fetch, and the job it runs in is doing its best
// to stop it: .grain/workflows/tests.yml sets `npm_config_offline` as a
// job-level `env:`, so it reaches this step as surely as it reaches the
// installs it is meant for. An offline `npm ci` here serves out of the
// very cache this script exists to fill, fails ENOTCACHED on the first
// thing missing from it, and -- ENOTCACHED being no transport code, with
// the registry answering a ping perfectly well -- the script then reads
// its own muzzling as a lockfile that does not resolve and hands the
// verdict to the install below, which fails the same way. That is a red
// verdict about a tree with nothing wrong with it, and it is what main's
// frontend job was giving once the guest image stopped shipping a warm
// ~/.npm to hide it.
func TestTheWarmStepFetchesEvenInsideAJobThatInstallsOffline(t *testing.T) {
	t.Parallel()
	// A stub that reports the offline settings the script handed it,
	// which is the only thing this test is asking about.
	dir := t.TempDir()
	stub := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  config) echo 'http://registry.example.invalid/' ;;\n" +
		"  ping) exit 0 ;;\n" +
		"  ci) echo \"npm stub: offline=${npm_config_offline-unset} OFFLINE=${NPM_CONFIG_OFFLINE-unset}\"; exit 0 ;;\n" +
		"  *) echo \"npm stub: unexpected $*\" >&2; exit 111 ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(stub), 0o755); err != nil {
		t.Fatalf("writing the npm stub: %v", err)
	}
	status, out := warmWithEnv(t, dir, "npm_config_offline=true", "NPM_CONFIG_OFFLINE=true")
	if status != 0 {
		t.Fatalf("the warm step exited %d with npm reporting no failure at all:\n%s", status, out)
	}
	if strings.Contains(out, "offline=true") || strings.Contains(out, "OFFLINE=true") {
		t.Errorf("the warm step ran npm with offline mode still on, so it fetched nothing and the "+
			"offline install after it fails ENOTCACHED on a lockfile that is fine:\n%s", out)
	}
}

// The registry did not answer. This is the case the whole split exists
// for: the step must fail, so that `infrastructure: true` can turn its
// failure into an errored job rather than a red branch.
func TestARegistryThatWillNotAnswerFailsTheWarmStep(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"ECONNREFUSED", "ENOTFOUND", "EAI_AGAIN", "ETIMEDOUT", "E503", "E429", "ERR_SOCKET_TIMEOUT"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			// The registry answers a ping in every one of these, so it
			// is npm's own code that has to carry the decision: a
			// registry can be up and still be failing the fetch this
			// job needs.
			status, out := warm(t, npmStub(t, "npm error code "+code+"\nnpm error request to https://registry.npmjs.org/ failed", 1, 0))
			if status != 1 {
				t.Fatalf("npm failed with %s and the warm step exited %d: that hands a network failure to the tree's verdict\n%s", code, status, out)
			}
			if !strings.Contains(out, "this deployment's problem") {
				t.Errorf("%s: the warm step did not say whose failure this was:\n%s", code, out)
			}
		})
	}
}

// npm's `network` line without a code grain recognises, which is the
// other shape a transport failure arrives in.
func TestANetworkFailureWithNoCodeStillFailsTheWarmStep(t *testing.T) {
	t.Parallel()
	status, out := warm(t, npmStub(t, "npm error network request to https://registry.npmjs.org/react failed, reason: socket hang up", 1, 0))
	if status != 1 {
		t.Fatalf("npm reported a network failure and the warm step exited %d:\n%s", status, out)
	}
}

// The lockfile is the problem, and the registry is up to prove it. The
// step must *succeed*: its job is to fetch, it could not, and the
// reason is a commit's to answer for -- so the verdict is left to the
// offline install after it rather than being given here, where it would
// wear a conclusion nobody blames.
func TestALockfileThatDoesNotResolveLeavesTheVerdictToTheInstall(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"E404", "E403", "EUSAGE", "ERESOLVE", "EBADENGINE", "EINTEGRITY"} {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			status, out := warm(t, npmStub(t, "npm error code "+code+"\nnpm error 404 Not Found - GET https://registry.npmjs.org/not-a-package", 1, 0))
			if status != 0 {
				t.Fatalf("npm failed with %s while the registry was up, and the warm step exited %d: an infrastructure conclusion would hide a broken lockfile\n%s", code, status, out)
			}
			if !strings.Contains(out, "leaving the") && !strings.Contains(out, "Leaving the") {
				t.Errorf("%s: the warm step did not say it was deferring the verdict:\n%s", code, out)
			}
		})
	}
}

// A failure the script has never seen, with a registry that does not
// answer a ping: the ping is what catches the codes the list above does
// not have, and it has to be enough on its own.
func TestAnUnknownFailureAgainstADeadRegistryIsInfrastructure(t *testing.T) {
	t.Parallel()
	status, out := warm(t, npmStub(t, "npm error code ESOMETHINGNEW", 1, 1))
	if status != 1 {
		t.Fatalf("npm failed in a way this script does not know while the registry was unreachable, and the warm step exited %d:\n%s", status, out)
	}
	if !strings.Contains(out, "does not answer a ping") {
		t.Errorf("the warm step did not say it had asked the registry:\n%s", out)
	}
}

// The same unknown failure, with a registry that is answering. Nothing
// says this is infrastructure, so it is the tree's: the default is
// deliberate, and it is the direction a wrong guess is survivable in.
func TestAnUnknownFailureAgainstALiveRegistryIsTheTreesToAnswerFor(t *testing.T) {
	t.Parallel()
	status, out := warm(t, npmStub(t, "npm error code ESOMETHINGNEW", 1, 0))
	if status != 0 {
		t.Fatalf("the warm step exited %d over a failure it cannot explain against a registry that is up: that is a verdict quietly not existing\n%s", status, out)
	}
}

// npm exiting 0 from an install that did not finish -- "Exit handler
// never called!", npm/cli's own bug line, which this repository has seen
// from a `npm ci` that could not reach its registry. Believing that exit
// code would hand the offline install a cache with holes in it, and the
// red tick for a network failure would land on the commit after all.
func TestAnInstallThatExitedZeroWithoutFinishingIsNotTakenAsWarm(t *testing.T) {
	t.Parallel()
	status, out := warm(t, npmStub(t, "npm error Exit handler never called!\nnpm error This is an error with npm itself.", 0, 1))
	if status != 1 {
		t.Fatalf("npm exited 0 with errors in its output against a dead registry, and the warm step called that warm (%d):\n%s", status, out)
	}
}
