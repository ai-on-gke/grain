package mcp

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// shrinkDefaultRunCommandTimeout runs the tests below against a bound
// they can actually wait out, instead of the real five-minute default.
func shrinkDefaultRunCommandTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := defaultRunCommandTimeout
	defaultRunCommandTimeout = d
	t.Cleanup(func() { defaultRunCommandTimeout = old })
}

// TestRunCommandSaysWhenTheDefaultBoundKilledIt is the local half of
// docs/agent-ergonomics.md's finding 3. A command the bound killed comes
// back as exit=-1, which is also what "the command could not be started"
// looks like, and the bound that killed it is one the call never passed
// and cannot otherwise see -- so the answer has to name it, say it was
// grain's default rather than anything the command asked for, and say
// what to do instead of running the same command again.
func TestRunCommandSaysWhenTheDefaultBoundKilledIt(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not installed")
	}
	shrinkDefaultRunCommandTimeout(t, 250*time.Millisecond)

	client := newTestClient(t, t.TempDir())
	res, err := client.CallTool(context.Background(), "run_command", map[string]any{"command": "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Text()
	if !strings.Contains(text, "Killed after 250ms by run_command's default bound") {
		t.Errorf("timed-out run_command does not say the default bound killed it:\n%s", text)
	}
	for _, want := range []string{"it passed no `timeout`", "up to 600000", "narrow the command"} {
		if !strings.Contains(text, want) {
			t.Errorf("timed-out run_command answer does not mention %q:\n%s", want, text)
		}
	}
}

// TestRunCommandNamesACallersOwnTimeoutWhenItKillsIt: the same notice,
// worded for a caller that did choose the bound -- telling that run its
// build was killed by "run_command's default" would send it looking for
// a default that had nothing to do with it.
func TestRunCommandNamesACallersOwnTimeoutWhenItKillsIt(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not installed")
	}
	client := newTestClient(t, t.TempDir())
	res, err := client.CallTool(context.Background(), "run_command", map[string]any{
		"command": "sleep 30", "timeout": float64(1000),
	})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Text(); !strings.Contains(text, "Killed after 1s by the `timeout` this call passed") {
		t.Errorf("run_command killed by a caller-supplied timeout does not say so:\n%s", text)
	}
}

// TestRunCommandDoesNotClaimATimeoutWhenTheCommandFailedItself pins the
// other direction, which is the one that would do real damage: a notice
// on a command that ended on its own would have a run raising its
// "timeout" to fix a compile error.
func TestRunCommandDoesNotClaimATimeoutWhenTheCommandFailedItself(t *testing.T) {
	client := newTestClient(t, t.TempDir())
	res, err := client.CallTool(context.Background(), "run_command", map[string]any{"command": "exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Text(); strings.Contains(text, "[grain]") {
		t.Errorf("run_command that exited on its own carries a grain notice:\n%s", text)
	}
}

// TestSSHRunCommandSaysWhenTheBoundKilledIt is finding 3's remote half.
// The guest reports the bound with `timeout`'s own reserved status, and
// a bare "exit=124" is not something an agent can be expected to read.
func TestSSHRunCommandSaysWhenTheBoundKilledIt(t *testing.T) {
	requireSleepCoreutil(t)
	shrinkDefaultRunCommandTimeout(t, time.Second)

	client := newSSHTestClient(t, t.TempDir())
	res, err := client.CallTool(context.Background(), "run_command", map[string]any{"command": "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Text()
	if !strings.Contains(text, "exit=124") {
		t.Fatalf("want the guest's own timeout status in the answer:\n%s", text)
	}
	if !strings.Contains(text, "Killed after 1s by run_command's default bound") {
		t.Errorf("timed-out remote run_command does not say the bound killed it:\n%s", text)
	}
}

// TestSSHRunCommandKillsACommandThatIgnoresSIGTERM is the bound actually
// being a bound. Measured on a real grain guest before this changed,
// plain `timeout N` against a command trapping TERM waits for that
// command to finish of its own accord -- a full minute for a minute-long
// sleep -- and only then reports 124, so the tool call stayed open for as
// long as the command felt like. `--kill-after` ends it, and the 137 that
// produces gets its own explanation, since 128+SIGKILL is no more
// self-explanatory than 124 was.
func TestSSHRunCommandKillsACommandThatIgnoresSIGTERM(t *testing.T) {
	requireSleepCoreutil(t)
	shrinkDefaultRunCommandTimeout(t, time.Second)

	client := newSSHTestClient(t, t.TempDir())
	start := time.Now()
	res, err := client.CallTool(context.Background(), "run_command", map[string]any{
		"command": "trap '' TERM; sleep 60",
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > runCommandKillGrace+15*time.Second {
		t.Errorf("a SIGTERM-ignoring command held run_command for %s, want the bound plus %s",
			elapsed, runCommandKillGrace)
	}
	text := res.Text()
	if !strings.Contains(text, "exit=137") {
		t.Fatalf("want exit=137 for a command that had to be SIGKILLed:\n%s", text)
	}
	if !strings.Contains(text, "exit=137 is SIGKILL") {
		t.Errorf("a SIGKILLed remote command gets no explanation of the status:\n%s", text)
	}
	if !strings.Contains(text, "OOM killer") {
		t.Errorf("the 137 notice does not name the other thing that sends SIGKILL:\n%s", text)
	}
}

// requireSleepCoreutil guards the two tests above, which need a command
// that takes measurable time for the bound to end.
//
// It deliberately does not also require `timeout`, which it used to: the
// bound these tests exercise is applied inside the guest, and the guest
// localExecRunner stands in for always has a `timeout` because the double
// puts one on its PATH (guestEnv, ssh_tools_test.go). Requiring the host
// to have its own was how the 124 and 137 notices quietly stopped being
// covered on macOS, which has no `timeout` at all and is therefore the
// platform where a mistake in this path would go unseen longest.
func requireSleepCoreutil(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not installed")
	}
}
