package cli

// args_test.go: a word this CLI has no use for is refused rather than
// dropped on the floor (args.go, task 275).
//
// The cost of the old silence, and the case this file is built around,
// is `grain pause -set "the queue is wedged, back in an hour"`: -set is
// a boolean, so the sentence went nowhere, and the deployment stopped
// with grain's own "paused by hand" written where the operator's reason
// should have been. Every check below is the same shape -- the command
// fails, its message names the word that was going to be ignored, and
// nothing was done.

import (
	"flag"
	"strings"
	"testing"
)

// newTestFlagSet is a flag set with a name and nothing else: the checks
// take one only to read the command's own spelling off it.
func newTestFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	return fs
}

// The whole task CLI, one stray word each. The word is always the last
// one written, so the message can be checked for it without a table of
// expected sentences: what matters is that the command refused, and that
// it said which word it refused over.
func TestCommandsRefuseArgumentsTheyHaveNoUseFor(t *testing.T) {
	t.Parallel()
	server := listTestServer(t)
	for _, args := range [][]string{
		{"list", "junk"},
		{"get", "1", "extra"},
		{"create", "-title", "Filed from a shell", "-no-repo", "junk"},
		{"update", "1", "-title", "Renamed", "extra"},
		{"approve", "2", "extra"},
		{"withdraw", "1", "extra"},
		{"defer", "1", "extra"},
		{"undefer", "1", "extra"},
		{"capability", "1", "self-debug", "attach", "extra"},
		{"close", "1", "extra"},
		{"reopen", "1", "extra"},
		{"retry", "1", "extra"},
		{"retry-merge", "1", "extra"},
		{"run-now", "1", "extra"},
		{"config", "junk"},
		{"settings", "junk"},
		{"metrics", "junk"},
		{"failures", "junk"},
		{"pause", "junk"},
		{"ci", "list", "junk"},
		{"ci", "run", "-repo", "acme/widgets", "-branch", "main", "junk"},
		{"ci", "show", "1", "extra"},
		{"ci", "cancel", "1", "extra"},
		{"repo", "list", "junk"},
		{"repo", "capabilities", "acme/widgets", "extra"},
		{"repo", "prompt-extension", "acme/widgets", "extra"},
		{"repo", "setup-command", "acme/widgets", "extra"},
		{"repo", "unfixable-checks", "acme/widgets", "extra"},
		{"repo", "work-ownership", "acme/widgets", "extra"},
		{"repo", "workflows", "acme/widgets", "extra"},
		{"repo", "add", "acme/widgets", "extra"},
		{"repo", "remove", "acme/widgets", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			err := runCLIWithAccessCode(append([]string{"-server", server}, args...))
			if err == nil {
				t.Fatalf("`grain %s` was accepted, with its last word going nowhere",
					strings.Join(args, " "))
			}
			if word := args[len(args)-1]; !strings.Contains(err.Error(), word) {
				t.Errorf("`grain %s` failed with %q, which does not name %q",
					strings.Join(args, " "), err, word)
			}
		})
	}
}

// `grain comment <id> <body...>` is the exception, and stays one: every
// word after the id is the body.
func TestCommentStillTakesItsBodyAsWords(t *testing.T) {
	server := listTestServer(t)
	if err := runCLIWithAccessCode([]string{"-server", server, "comment", "1", "several", "words", "of", "body"}); err != nil {
		t.Fatalf("grain comment 1 <body...>: %v", err)
	}
	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "get", "1"}); err != nil {
			t.Fatalf("grain get 1: %v", err)
		}
	})
	if !strings.Contains(out, "several words of body") {
		t.Errorf("grain get 1 printed %q, want the comment's whole body", out)
	}
}

// The one the report was written about. It gets a message of its own,
// because the mistake has an obvious intent -- and the deployment must
// still be running afterwards, since a pause recorded without the reason
// somebody typed is worse than no pause at all.
func TestPauseRefusesAReasonWrittenWithoutItsFlag(t *testing.T) {
	server := pauseServer(t, nil)
	reason := "the queue is wedged, back in an hour"

	err := runCLIWithAccessCode([]string{"-server", server, "pause", "-set", reason})
	if err == nil {
		t.Fatal("`grain pause -set <reason>` was accepted, and the reason went nowhere")
	}
	for _, want := range []string{reason, "-reason"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("`grain pause -set <reason>` failed with %q, which does not mention %q", err, want)
		}
	}

	out := captureStdout(t, func() {
		if err := runCLIWithAccessCode([]string{"-server", server, "pause"}); err != nil {
			t.Fatalf("grain pause: %v", err)
		}
	})
	if !strings.Contains(out, "the pipeline is running") {
		t.Errorf("`grain pause` printed %q: the refused command paused the deployment anyway", out)
	}
}

// The host-local commands are the same rule: they edit a colocated
// deployment's files rather than talking to its API, but a word written
// after one of them is just as lost.
func TestHostLocalCommandsRefuseArgumentsTheyHaveNoUseFor(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	for name, run := range map[string]func() error{
		"grain state status": func() error { return runState([]string{"-data-dir", dataDir, "status", "junk"}) },
		"grain state key show": func() error {
			return runState([]string{"-data-dir", dataDir, "key", "show", "junk"})
		},
		"grain secrets list": func() error { return runSecrets([]string{"-data-dir", dataDir, "list", "junk"}) },
		"grain demo": func() error {
			_, err := parseDemoFlags([]string{"junk"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil {
				t.Fatalf("`%s junk` was accepted, with the word going nowhere", name)
			}
			if !strings.Contains(err.Error(), "junk") {
				t.Errorf("`%s junk` failed with %q, which does not name the word", name, err)
			}
		})
	}
}

// The wording itself, since it is the whole point: it names the command,
// how many arguments that command reads, and the word it is refusing.
func TestArgumentCheckWording(t *testing.T) {
	fs := newTestFlagSet("grain get")
	if err := atMostArgs(fs, []string{"1"}, 1); err != nil {
		t.Fatalf("one argument on a command that takes one: %v", err)
	}
	err := atMostArgs(fs, []string{"1", "extra"}, 1)
	if err == nil {
		t.Fatal("two arguments on a command that takes one were accepted")
	}
	for _, want := range []string{"grain get", "one argument", `"extra"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err, want)
		}
	}

	if err := noArgs(newTestFlagSet("grain list"), nil); err != nil {
		t.Fatalf("no arguments at all: %v", err)
	}
	err = noArgs(newTestFlagSet("grain list"), []string{"junk"})
	if err == nil {
		t.Fatal("an argument on a command that takes none was accepted")
	}
	for _, want := range []string{"grain list", "no arguments", `"junk"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not mention %q", err, want)
		}
	}
}
